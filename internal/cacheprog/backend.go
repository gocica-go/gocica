package cacheprog

import (
	"context"
	"fmt"
	"io"

	"github.com/mazrean/gocica/internal/closer"
	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
)

type Backend interface {
	Get(ctx context.Context, actionID string) (diskPath string, metaData *MetaData, err error)
	Put(ctx context.Context, actionID, outputID string, size int64, body myio.ClonableReadSeeker) (diskPath string, err error)
	Close(ctx context.Context) error
}

type MetaData struct {
	// OutputID is the unique identifier for the object.
	OutputID string
	// Size is the size of the object in bytes.
	Size int64
	// Timenano is the time the object was created in Unix nanoseconds.
	Timenano int64
}

var _ Backend = &CombinedBackend{}

var (
	requestGauge  = metrics.NewGauge("backend_request")
	durationGauge = metrics.NewGauge("backend_duration")
	cacheHitGauge = metrics.NewGauge("backend_cache_hit")
)

type CombinedBackend struct {
	logger log.Logger

	local  local.Backend
	remote remote.Backend

	eg *errgroup.Group
}

func NewCombinedBackend(logger log.Logger, local local.Backend, remote remote.Backend) (*CombinedBackend, error) {
	combined := &CombinedBackend{
		logger: logger,
		eg:     &errgroup.Group{},
		local:  local,
		remote: remote,
	}

	return combined, nil
}

func (b *CombinedBackend) Get(ctx context.Context, actionID string) (diskPath string, metaData *MetaData, err error) {
	requestGauge.Set(1, "get")
	defer requestGauge.Set(0, "get")

	durationGauge.Stopwatch(func() {
		var indexEntry *remote.MetaData
		indexEntry, err = b.remote.MetaData(ctx, actionID)
		if err != nil {
			err = fmt.Errorf("get remote metadata: %w", err)
			return
		}

		if indexEntry == nil {
			cacheHitGauge.Set(0, "meta_miss")
			return
		}

		diskPath, err = b.local.Get(ctx, indexEntry.OutputID)
		if err != nil {
			err = fmt.Errorf("get local cache: %w", err)
			return
		}

		if diskPath == "" {
			cacheHitGauge.Set(0, "local_miss")
			return
		}

		cacheHitGauge.Set(1, "hit")

		metaData = &MetaData{
			OutputID: indexEntry.OutputID,
			Size:     indexEntry.Size,
			Timenano: indexEntry.Timenano,
		}
		err = nil
	}, "get")

	return diskPath, metaData, err
}

func (b *CombinedBackend) Put(ctx context.Context, actionID, outputID string, size int64, body myio.ClonableReadSeeker) (diskPath string, err error) {
	requestGauge.Set(1, "put")
	defer requestGauge.Set(0, "put")

	durationGauge.Stopwatch(func() {
		var (
			remoteReader io.ReadSeeker
			localReader  io.Reader
		)
		if size == 0 {
			remoteReader = myio.EmptyReader
			localReader = myio.EmptyReader
		} else {
			remoteReader = body
			localReader = body.Clone()
		}

		b.eg.Go(func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					b.logger.Warnf("panic in remote put: %v", r)
					err = fmt.Errorf("panic in remote put: %v", r)
					return
				}
			}()

			err = b.remote.Put(context.Background(), actionID, outputID, size, remoteReader)
			if err != nil {
				err = fmt.Errorf("put remote cache: %w", err)
				return
			}

			return nil
		})

		var opener local.OpenerWithUnlock
		diskPath, opener, err = b.local.Put(ctx, outputID)
		if err != nil {
			err = fmt.Errorf("put: %w", err)
			return
		}
		if opener == nil {
			return
		}

		f, openErr := opener.Open()
		if openErr != nil {
			err = fmt.Errorf("open local cache: %w", openErr)
			return
		}
		defer f.Close()

		if _, cpErr := io.Copy(f, localReader); cpErr != nil {
			err = fmt.Errorf("copy: %w", cpErr)
			return
		}
	}, "put")

	return diskPath, err
}

func (b *CombinedBackend) Close(ctx context.Context) (err error) {
	requestGauge.Set(1, "close")
	defer requestGauge.Set(0, "close")

	durationGauge.Stopwatch(func() {
		if waitErr := b.eg.Wait(); waitErr != nil {
			err = fmt.Errorf("wait for all tasks: %w", waitErr)
			return
		}

		if closeErr := closer.Close(ctx); closeErr != nil {
			err = fmt.Errorf("close closer: %w", closeErr)
			return
		}
	}, "close")

	b.logger.Debugf("closed")

	return err
}
