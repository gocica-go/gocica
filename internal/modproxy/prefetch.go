package modproxy

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// bulkPrefetch pulls the whole remote blob down in offset order.
//
// The per-object path issues one ranged request per module. Measured against
// tailscale that is ~1500 requests for a 256 MB blob, and the transfer is
// latency-bound, so the request count is the cost. Walking the blob in order
// lets the downloader coalesce neighbouring objects into 4 MiB reads instead --
// roughly 64 requests for the same bytes.
//
// Objects still land individually, each verified against its own content hash
// before it becomes visible, so a partial transfer simply leaves the rest to the
// per-object path.
func (c *Cache) bulkPrefetch(ctx context.Context, objectIDs []string) error {
	started := time.Now()

	// Claim these objects before the first byte moves, so a request arriving now
	// waits for this pass instead of racing it.
	c.beginPending(objectIDs)
	defer c.finishPending()

	var fetched int64
	err := c.downloader.DownloadAllOutputBlocks(ctx, func(_ context.Context, objectID string) (io.WriteCloser, error) {
		return c.newVerifyingWriter(objectID, &fetched)
	}, func(objectID string) bool {
		// Already served from disk this run, or left by an earlier one.
		if !c.store.Has(objectID) {
			return false
		}
		c.donePending(objectID)

		return true
	})
	if err != nil {
		return fmt.Errorf("download output blocks: %w", err)
	}

	c.logger.Infof("prefetched %d module objects in %s.", fetched, time.Since(started))

	return nil
}

// verifyingWriter stores one object from the bulk download, committing it only
// if its bytes hash to the ID the index promised.
type verifyingWriter struct {
	cache    *Cache
	objectID string
	writer   *Writer
	fetched  *int64
	once     sync.Once
}

func (c *Cache) newVerifyingWriter(objectID string, fetched *int64) (io.WriteCloser, error) {
	w, err := c.store.NewWriter()
	if err != nil {
		return nil, fmt.Errorf("create object writer: %w", err)
	}

	return &verifyingWriter{cache: c, objectID: objectID, writer: w, fetched: fetched}, nil
}

func (w *verifyingWriter) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

// Close is idempotent: the download path closes writers both explicitly and
// through a deferred cleanup.
func (w *verifyingWriter) Close() error {
	w.once.Do(func() {
		defer w.cache.donePending(w.objectID)

		if _, _, err := w.cache.store.Commit(w.writer, w.objectID); err != nil {
			// Not fatal. The object is simply absent, and the per-object path will
			// fetch it -- or the go command will go upstream.
			w.cache.logger.Warnf("prefetch %s: %v", w.objectID, err)

			return
		}
		w.cache.countFetched(w.fetched)
	})

	return nil
}
