package modproxy

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mazrean/gocica/internal/modtree"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// treeKeyPrefix namespaces extracted-tree entries inside the same index as the
// zips. Sharing the index keeps them in one blob, one cache entry and one
// restore key chain; a GOPROXY request path can never collide with it because
// module paths cannot contain a colon.
const treeKeyPrefix = "tree:"

// treeConcurrency is how many module trees are written to the module cache at
// once. Restoring is dominated by writing many small files, so this is about
// keeping the disk busy rather than the link.
const treeConcurrency = 8

func treeKey(m modtree.Module) string {
	return treeKeyPrefix + m.EscapedPath + "@" + m.EscapedVersion
}

func moduleFromTreeKey(key string) (modtree.Module, bool) {
	rest, ok := strings.CutPrefix(key, treeKeyPrefix)
	if !ok {
		return modtree.Module{}, false
	}

	path, version, ok := strings.Cut(rest, "@")
	if !ok || path == "" || version == "" {
		return modtree.Module{}, false
	}

	return modtree.Module{EscapedPath: path, EscapedVersion: version}, true
}

// RestoreTrees puts every cached module back into the module cache already
// extracted, so the go command has nothing left to unzip.
//
// Anything that fails is simply left out: the module is still served as a zip
// over the proxy, and the go command extracts it as it always did.
func (c *Cache) RestoreTrees(ctx context.Context, gomodcache string) {
	if gomodcache == "" {
		return
	}

	type job struct {
		module   modtree.Module
		objectID string
	}

	var jobs []job
	c.locker.RLock()
	for key, entry := range c.entries {
		m, ok := moduleFromTreeKey(key)
		if !ok {
			continue
		}
		jobs = append(jobs, job{module: m, objectID: entry.OutputId})
	}
	c.locker.RUnlock()

	if len(jobs) == 0 {
		return
	}

	c.logger.Infof("restoring %d extracted modules into %s.", len(jobs), gomodcache)
	started := time.Now()

	var restored atomic64
	eg := &errgroup.Group{}
	eg.SetLimit(treeConcurrency)
	for _, j := range jobs {
		eg.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			if c.restoreTree(ctx, j.module, j.objectID, gomodcache) {
				restored.add(1)
			}

			return nil
		})
	}
	_ = eg.Wait()

	// A warm run never asks the proxy for a restored module, so without this they
	// would age out of the index for not being used.
	c.touchTreeEntries(c.now)

	c.logger.Infof("restored %d extracted modules in %s.", restored.load(), time.Since(started))
}

func (c *Cache) restoreTree(ctx context.Context, m modtree.Module, objectID, gomodcache string) bool {
	if !c.store.Has(objectID) && !c.fetch(ctx, objectID) {
		return false
	}

	f, _, err := c.store.Open(objectID)
	if err != nil {
		c.logger.Debugf("open tree object %s: %v", objectID, err)

		return false
	}
	defer f.Close()

	if err := modtree.Unpack(gomodcache, m, f); err != nil {
		c.logger.Warnf("restore %s@%s: %v. it will be served as a zip instead.", m.EscapedPath, m.EscapedVersion, err)

		return false
	}

	return true
}

// PackTrees stores the extracted form of every module this run downloaded but
// has no tree for yet.
//
// It runs at flush, because that is the first moment the go command is known to
// have finished extracting.
//
// The zips stay cached alongside the trees. `go mod download` fetches a module's
// zip unconditionally (cmd/go/internal/modcmd/download.go calls DownloadZip
// before Download), so dropping them would send that command upstream for
// everything; what the tree saves it is the extraction, not the transfer.
// `go build` needs neither.
func (c *Cache) PackTrees(ctx context.Context, gomodcache string) {
	if gomodcache == "" {
		return
	}

	missing := c.modulesWithoutTrees()
	if len(missing) == 0 {
		return
	}

	c.logger.Infof("packing %d extracted modules.", len(missing))
	started := time.Now()

	var packed atomic64
	eg := &errgroup.Group{}
	eg.SetLimit(treeConcurrency)
	for _, m := range missing {
		eg.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			if c.packTree(ctx, m, gomodcache) {
				packed.add(1)
			}

			return nil
		})
	}
	_ = eg.Wait()

	c.logger.Infof("packed %d extracted modules in %s.", packed.load(), time.Since(started))
}

func (c *Cache) packTree(ctx context.Context, m modtree.Module, gomodcache string) bool {
	w, err := c.store.NewWriter()
	if err != nil {
		c.logger.Warnf("create tree writer: %v", err)

		return false
	}

	if err := modtree.Pack(gomodcache, m, w); err != nil {
		c.store.Abort(w)
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			c.logger.Warnf("pack %s@%s: %v", m.EscapedPath, m.EscapedVersion, err)
		}

		return false
	}

	// A tar of source files is worth compressing, unlike the zip it replaces.
	if _, err := c.Store(ctx, treeKey(m), w, true); err != nil {
		c.logger.Warnf("store tree for %s@%s: %v", m.EscapedPath, m.EscapedVersion, err)

		return false
	}

	return true
}

// modulesWithoutTrees lists the modules the index has a zip for but no tree.
func (c *Cache) modulesWithoutTrees() []modtree.Module {
	c.locker.RLock()
	defer c.locker.RUnlock()

	haveTree := make(map[string]struct{}, len(c.entries))
	for key := range c.entries {
		if m, ok := moduleFromTreeKey(key); ok {
			haveTree[m.EscapedPath+"@"+m.EscapedVersion] = struct{}{}
		}
	}

	var out []modtree.Module
	for key := range c.entries {
		m, ok := moduleFromZipPath(key)
		if !ok {
			continue
		}
		if _, ok := haveTree[m.EscapedPath+"@"+m.EscapedVersion]; ok {
			continue
		}
		out = append(out, m)
	}

	return out
}

// moduleFromZipPath recognises the index key of a module zip.
func moduleFromZipPath(key string) (modtree.Module, bool) {
	rest, ok := strings.CutSuffix(key, ".zip")
	if !ok {
		return modtree.Module{}, false
	}

	path, version, ok := strings.Cut(rest, "/@v/")
	if !ok || path == "" || version == "" {
		return modtree.Module{}, false
	}

	return modtree.Module{EscapedPath: path, EscapedVersion: version}, true
}

// touchTreeEntries keeps restored trees from ageing out of the index just
// because nothing asked the proxy for them: a warm run never does.
func (c *Cache) touchTreeEntries(now *timestamppb.Timestamp) {
	c.locker.Lock()
	defer c.locker.Unlock()

	for key, entry := range c.entries {
		if _, ok := moduleFromTreeKey(key); ok {
			entry.LastUsedAt = now
		}
	}
}

// atomic64 keeps the counters here readable without importing sync/atomic for a
// single field.
type atomic64 struct {
	locker sync.Mutex
	value  int64
}

func (a *atomic64) add(n int64) {
	a.locker.Lock()
	defer a.locker.Unlock()
	a.value += n
}

func (a *atomic64) load() int64 {
	a.locker.Lock()
	defer a.locker.Unlock()

	return a.value
}
