package modproxy

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mazrean/gocica/internal/pkg/metrics"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// entryRetention drops index entries that no build has asked for in this long.
// Without it the module blob only ever grows.
const entryRetention = 7 * 24 * time.Hour

// DefaultPrefetchConcurrency is how many objects are pulled out of the remote
// blob at once when prefetching.
//
// Fetching purely on demand caps out at the go command's own download
// concurrency, which is a handful of workers. Measured against tailscale, that
// left a warm `go mod download` at ~34s for a 256 MB module set while
// actions/setup-go, which restores an already-extracted cache, took 0.1s. The
// transfer is latency-bound, not bandwidth-bound, so the fix is more of it in
// flight.
const DefaultPrefetchConcurrency = 24

var (
	cacheHitGauge   = metrics.NewGauge("modproxy_cache_hit")
	remoteFetchGaug = metrics.NewGauge("modproxy_remote_fetch_duration")
)

// CompressionRegistry decides, per object, whether the uploader should spend CPU
// on zstd.
//
// The uploader only sees content hashes, so it cannot tell a module zip from a
// go.mod file. The cache registers zips here as it stores them; everything else
// falls back to the size threshold. Compressing an already-compressed zip buys
// nothing and buffers the whole archive in memory.
type CompressionRegistry struct {
	locker sync.RWMutex
	skip   map[string]struct{}
}

func NewCompressionRegistry() *CompressionRegistry {
	return &CompressionRegistry{skip: map[string]struct{}{}}
}

// SkipCompression marks an object as not worth compressing.
func (r *CompressionRegistry) SkipCompression(objectID string) {
	r.locker.Lock()
	defer r.locker.Unlock()

	r.skip[objectID] = struct{}{}
}

// Policy returns the core.CompressionPolicy backed by this registry.
func (r *CompressionRegistry) Policy() core.CompressionPolicy {
	return func(objectID string, size int64) bool {
		r.locker.RLock()
		_, skip := r.skip[objectID]
		r.locker.RUnlock()

		if skip {
			return false
		}

		return core.DefaultCompressionPolicy(objectID, size)
	}
}

// NewCompressionPolicy adapts a CompressionRegistry for dependency injection.
func NewCompressionPolicy(r *CompressionRegistry) core.CompressionPolicy {
	return r.Policy()
}

// Cache maps GOPROXY request paths to stored objects, backed by the remote blob.
//
// Objects are fetched from the remote lazily, one byte range at a time, rather
// than downloading the whole blob up front the way the build cache does: a build
// only imports a fraction of its module graph, and a module zip is large enough
// that transferring unused ones costs more than the extra requests save.
type Cache struct {
	logger       log.Logger
	store        *Store
	downloader   *core.Downloader
	uploader     *core.Uploader
	compressions *CompressionRegistry

	locker  sync.RWMutex
	entries map[string]*v1.IndexEntry
	now     *timestamppb.Timestamp

	fetchGroup singleflight.Group
	stored     atomic.Int64
	// closed is set when the flush starts. After that an object may still be
	// served from disk, but it must not enter the index: the flush has already
	// waited for the uploads, so a late entry would point at bytes that never
	// reached the blob.
	closed atomic.Bool
	// uploads run in the background so a 70 MB module zip is not transferred to
	// the remote while the go command's request is still open. Flush waits.
	uploads errgroup.Group
}

// NewCache restores the index from the remote blob. A nil downloader or uploader
// simply means that half of the remote is unavailable; the cache still works
// against local disk.
func NewCache(
	ctx context.Context,
	logger log.Logger,
	store *Store,
	downloader *core.Downloader,
	uploader *core.Uploader,
	compressions *CompressionRegistry,
) (*Cache, error) {
	entries := map[string]*v1.IndexEntry{}
	if downloader != nil {
		restored, err := downloader.GetEntries(ctx)
		if err != nil {
			// Degraded mode: a broken index must not stop the proxy from serving.
			logger.Warnf("read module index: %v. ignoring the remote module cache.", err)
		}
		maps.Copy(entries, restored)
	}

	logger.Infof("module proxy cache initialized with %d entries.", len(entries))

	return &Cache{
		logger:       logger,
		store:        store,
		downloader:   downloader,
		uploader:     uploader,
		compressions: compressions,
		entries:      entries,
		now:          timestamppb.Now(),
	}, nil
}

// Get returns the stored object for a request path, fetching it from the remote
// blob if it is not on disk yet. The caller owns the returned file.
func (c *Cache) Get(ctx context.Context, path string) (*os.File, int64, bool) {
	entry, ok := c.lookup(path)
	if !ok {
		cacheHitGauge.Set(0, "miss")

		return nil, 0, false
	}

	if !c.store.Has(entry.OutputId) && !c.fetch(ctx, entry.OutputId) {
		cacheHitGauge.Set(0, "remote_miss")

		return nil, 0, false
	}

	f, size, err := c.store.Open(entry.OutputId)
	if err != nil {
		c.logger.Debugf("open cached object %s: %v", entry.OutputId, err)
		cacheHitGauge.Set(0, "open_miss")

		return nil, 0, false
	}

	cacheHitGauge.Set(1, "hit")

	return f, size, true
}

func (c *Cache) lookup(path string) (*v1.IndexEntry, bool) {
	c.locker.Lock()
	defer c.locker.Unlock()

	entry, ok := c.entries[path]
	if !ok {
		return nil, false
	}
	entry.LastUsedAt = c.now

	return entry, true
}

// fetch pulls one object out of the remote blob by range and verifies it against
// its own content hash before it becomes visible. Concurrent requests for the
// same object share one transfer.
func (c *Cache) fetch(ctx context.Context, objectID string) bool {
	if c.downloader == nil {
		return false
	}

	ok, _, _ := c.fetchGroup.Do(objectID, func() (any, error) {
		if c.store.Has(objectID) {
			return true, nil
		}

		output, found := c.downloader.Output(objectID)
		if !found {
			return false, nil
		}

		w, err := c.store.NewWriter()
		if err != nil {
			c.logger.Warnf("create object writer: %v", err)

			return false, nil
		}

		remoteFetchGaug.Stopwatch(func() {
			err = c.downloader.DownloadOutput(ctx, output, w)
		}, "download")
		if err != nil {
			c.logger.Warnf("download module object: %v. falling back to upstream.", err)
			c.store.Abort(w)

			return false, nil
		}

		// The object ID is the content hash, so this also proves the restored bytes
		// are intact. Serving a corrupt zip would make the go command fail the build
		// with a checksum error it cannot recover from.
		if _, _, err := c.store.Commit(w, objectID); err != nil {
			c.logger.Warnf("commit module object: %v. falling back to upstream.", err)

			return false, nil
		}

		return true, nil
	})

	stored, _ := ok.(bool)

	return stored
}

// Store commits freshly fetched bytes, indexes them under path and hands them to
// the remote uploader.
func (c *Cache) Store(ctx context.Context, path string, w *Writer, compressible bool) (string, error) {
	objectID, size, err := c.store.Commit(w, "")
	if err != nil {
		return "", fmt.Errorf("commit object: %w", err)
	}

	if c.closed.Load() {
		// Still on disk and still servable for the rest of this run; just not
		// published.
		c.logger.Debugf("module cache already flushed; not indexing %s", path)

		return objectID, nil
	}

	if !compressible {
		c.compressions.SkipCompression(objectID)
	}

	c.locker.Lock()
	c.entries[path] = &v1.IndexEntry{
		OutputId:   objectID,
		Size:       size,
		Timenano:   time.Now().UnixNano(),
		LastUsedAt: c.now,
	}
	c.locker.Unlock()

	c.stored.Add(1)

	if c.uploader != nil {
		c.uploads.Go(func() error {
			// Detached from the request: the go command must not wait for the
			// remote, and cancelling the request must not abandon the upload.
			if err := c.upload(context.WithoutCancel(ctx), objectID, size); err != nil {
				// Drop the entry rather than fail the whole flush. An index pointing
				// at bytes that are not in the blob would break every later build;
				// losing one module just means refetching it next time.
				c.logger.Warnf("upload module object %s: %v. dropping it from the index.", objectID, err)
				c.forget(path)
			}

			return nil
		})
	}

	return objectID, nil
}

func (c *Cache) upload(ctx context.Context, objectID string, size int64) error {
	f, _, err := c.store.Open(objectID)
	if err != nil {
		return fmt.Errorf("reopen object: %w", err)
	}
	defer f.Close()

	if err := c.uploader.UploadOutput(ctx, objectID, size, f); err != nil {
		return fmt.Errorf("upload output: %w", err)
	}

	return nil
}

// forget removes a path from the index.
func (c *Cache) forget(path string) {
	c.locker.Lock()
	defer c.locker.Unlock()

	delete(c.entries, path)
}

// Prefetch pulls every indexed object into the local store in the background.
//
// Requests that arrive for an object still in flight join its transfer through
// the same singleflight, so this never duplicates work or races a live request.
func (c *Cache) Prefetch(ctx context.Context, concurrency int) {
	if c.downloader == nil {
		return
	}
	if concurrency <= 0 {
		concurrency = DefaultPrefetchConcurrency
	}

	objectIDs := c.indexedObjects()
	if len(objectIDs) == 0 {
		return
	}

	c.logger.Infof("prefetching %d module objects with %d workers.", len(objectIDs), concurrency)

	started := time.Now()
	eg := &errgroup.Group{}
	eg.SetLimit(concurrency)
	for _, objectID := range objectIDs {
		eg.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			c.fetch(ctx, objectID)

			return nil
		})
	}
	_ = eg.Wait()

	c.logger.Infof("prefetched %d module objects in %s.", len(objectIDs), time.Since(started))
}

func (c *Cache) indexedObjects() []string {
	c.locker.RLock()
	defer c.locker.RUnlock()

	seen := make(map[string]struct{}, len(c.entries))
	objectIDs := make([]string, 0, len(c.entries))
	for _, entry := range c.entries {
		if _, ok := seen[entry.OutputId]; ok {
			continue
		}
		seen[entry.OutputId] = struct{}{}
		if !c.store.Has(entry.OutputId) {
			objectIDs = append(objectIDs, entry.OutputId)
		}
	}

	return objectIDs
}

// Stored reports how many objects this run added.
func (c *Cache) Stored() int64 {
	return c.stored.Load()
}

// Flush publishes the index. It is a no-op when nothing new was stored, so a
// fully warm run never writes to the remote at all.
func (c *Cache) Flush(ctx context.Context) error {
	c.closed.Store(true)

	if c.uploader == nil {
		return nil
	}

	if c.stored.Load() == 0 {
		c.logger.Infof("no module was fetched from upstream. skipping module cache upload.")

		return nil
	}

	// Uploads never report errors: each one drops its own index entry on failure.
	_ = c.uploads.Wait()

	if err := c.uploader.Commit(ctx, c.snapshot()); err != nil {
		return fmt.Errorf("commit module cache: %w", err)
	}

	return nil
}

// snapshot returns the entries worth keeping.
func (c *Cache) snapshot() map[string]*v1.IndexEntry {
	c.locker.RLock()
	defer c.locker.RUnlock()

	limit := time.Now().Add(-entryRetention)
	kept := make(map[string]*v1.IndexEntry, len(c.entries))
	for path, entry := range c.entries {
		if entry.LastUsedAt.AsTime().After(limit) {
			kept[path] = entry
		}
	}

	return kept
}
