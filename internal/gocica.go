package internal

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/mazrean/gocica/internal/backend"
	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
)

type Gocica struct {
	logger    log.Logger
	backend   backend.Backend
	hitCount  uint64
	missCount uint64
	putCount  uint64
}

func NewGocica(logger log.Logger, backend backend.Backend) *Gocica {
	return &Gocica{logger: logger, backend: backend}
}

func (g *Gocica) Get(ctx context.Context, req *protocol.Request, res *protocol.Response) error {
	diskPath, meta, err := g.backend.Get(ctx, req.ActionID)
	if err != nil {
		return fmt.Errorf("get action: %w", err)
	}

	if diskPath == "" || meta == nil {
		atomic.AddUint64(&g.missCount, 1)
		g.logger.Debugf("action %s not found(diskPath: %s, meta: %v)", req.ActionID, diskPath, meta)
		res.Miss = true
		return nil
	}

	atomic.AddUint64(&g.hitCount, 1)
	g.logger.Debugf("action %s found", req.ActionID)
	res.DiskPath = diskPath
	res.OutputID = meta.OutputID
	res.Size = meta.Size
	res.TimeNanos = meta.Timenano

	return nil
}

func (g *Gocica) Put(ctx context.Context, req *protocol.Request, res *protocol.Response) error {
	atomic.AddUint64(&g.putCount, 1)
	diskPath, err := g.backend.Put(ctx, req.ActionID, req.OutputID, req.BodySize, req.Body)
	if err != nil {
		return fmt.Errorf("put action: %w", err)
	}

	res.DiskPath = diskPath

	return nil
}

func (g *Gocica) Close() error {
	g.logger.Infof("cache hit count: %d", atomic.LoadUint64(&g.hitCount))
	g.logger.Infof("cache miss count: %d", atomic.LoadUint64(&g.missCount))
	g.logger.Infof("cache put count: %d", atomic.LoadUint64(&g.putCount))
	return g.backend.Close()
}
