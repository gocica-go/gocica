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
	hitCount  atomic.Uint64
	missCount atomic.Uint64
	putCount  atomic.Uint64
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
		cp.missCount.Add(1)
		cp.logger.Debugf("action %s not found(diskPath: %s, meta: %v)", req.ActionID, diskPath, meta)
		res.Miss = true
		return nil
	}

	cp.hitCount.Add(1)
	cp.logger.Debugf("action %s found", req.ActionID)
	res.DiskPath = diskPath
	res.OutputID = meta.OutputID
	res.Size = meta.Size
	res.TimeNanos = meta.Timenano

	return nil
}

func (cp *CacheProg) Put(ctx context.Context, req *protocol.Request, res *protocol.Response) error {
	cp.putCount.Add(1)
	diskPath, err := cp.backend.Put(ctx, req.ActionID, req.OutputID, req.BodySize, req.Body)
	if err != nil {
		return fmt.Errorf("put action: %w", err)
	}

	res.DiskPath = diskPath

	return nil
}

func (cp *CacheProg) Close(ctx context.Context) error {
	cp.logger.Infof("cache hit count: %d", cp.hitCount.Load())
	cp.logger.Infof("cache miss count: %d", cp.missCount.Load())
	cp.logger.Infof("cache put count: %d", cp.putCount.Load())

	if err := cp.backend.Close(ctx); err != nil {
		return fmt.Errorf("close backend: %w", err)
	}

	return nil
}
