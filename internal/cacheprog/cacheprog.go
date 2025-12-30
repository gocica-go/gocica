package cacheprog

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
)

type CacheProg struct {
	logger    log.Logger
	backend   Backend
	hitCount  uint64
	missCount uint64
	putCount  uint64
}

func NewCacheProg(logger log.Logger, backend Backend) *CacheProg {
	return &CacheProg{logger: logger, backend: backend}
}

func (cp *CacheProg) Get(ctx context.Context, req *protocol.Request, res *protocol.Response) error {
	diskPath, meta, err := cp.backend.Get(ctx, req.ActionID)
	if err != nil {
		return fmt.Errorf("get action: %w", err)
	}

	if diskPath == "" || meta == nil {
		atomic.AddUint64(&cp.missCount, 1)
		cp.logger.Debugf("action %s not found(diskPath: %s, meta: %v)", req.ActionID, diskPath, meta)
		res.Miss = true
		return nil
	}

	atomic.AddUint64(&cp.hitCount, 1)
	cp.logger.Debugf("action %s found", req.ActionID)
	res.DiskPath = diskPath
	res.OutputID = meta.OutputID
	res.Size = meta.Size
	res.TimeNanos = meta.Timenano

	return nil
}

func (cp *CacheProg) Put(ctx context.Context, req *protocol.Request, res *protocol.Response) error {
	atomic.AddUint64(&cp.putCount, 1)
	diskPath, err := cp.backend.Put(ctx, req.ActionID, req.OutputID, req.BodySize, req.Body)
	if err != nil {
		return fmt.Errorf("put action: %w", err)
	}

	res.DiskPath = diskPath

	return nil
}

func (cp *CacheProg) Close(ctx context.Context) error {
	cp.logger.Infof("cache hit count: %d", atomic.LoadUint64(&cp.hitCount))
	cp.logger.Infof("cache miss count: %d", atomic.LoadUint64(&cp.missCount))
	cp.logger.Infof("cache put count: %d", atomic.LoadUint64(&cp.putCount))

	if err := cp.backend.Close(ctx); err != nil {
		return fmt.Errorf("close backend: %w", err)
	}

	return nil
}
