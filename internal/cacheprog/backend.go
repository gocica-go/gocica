package cacheprog

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"
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

var _ Backend = &ConbinedBackend{}

var (
	requestGauge  = metrics.NewGauge("backend_request")
	durationGauge = metrics.NewGauge("backend_duration")
	cacheHitGauge = metrics.NewGauge("backend_cache_hit")
)

type ConbinedBackend struct {
	logger log.Logger

	local  local.Backend
	remote remote.Backend

	objectMapLocker sync.Mutex
	objectMap       map[string]struct{}

	eg                   *errgroup.Group
	nowTimestamp         *timestamppb.Timestamp
	metaDataMap          map[string]*v1.IndexEntry
	newMetaDataMapLocker sync.Mutex
	newMetaDataMap       map[string]*v1.IndexEntry
}

func NewConbinedBackend(logger log.Logger, local local.Backend, remote remote.Backend) (*ConbinedBackend, error) {
	conbined := &ConbinedBackend{
		logger:       logger,
		eg:           &errgroup.Group{},
		objectMap:    map[string]struct{}{},
		local:        local,
		remote:       remote,
		nowTimestamp: timestamppb.Now(),
	}

	conbined.start()

	return conbined, nil
}

func (cb *ConbinedBackend) start() {
	var err error
	cb.metaDataMap, err = cb.remote.MetaData(context.Background())
	if err != nil {
		cb.logger.Warnf("parse remote metadata: %v. ignore the all remote cache.", err)
	}
	if cb.metaDataMap == nil {
		cb.metaDataMap = map[string]*v1.IndexEntry{}
	}

	for _, indexEntry := range cb.metaDataMap {
		cb.objectMap[indexEntry.OutputId] = struct{}{}
	}

	cb.newMetaDataMap = make(map[string]*v1.IndexEntry, len(cb.metaDataMap))
	metaLimitLastUsedAt := time.Now().Add(-time.Hour * 24 * 7)
	for actionID, metaData := range cb.metaDataMap {
		if metaData.LastUsedAt.AsTime().After(metaLimitLastUsedAt) {
			cb.newMetaDataMap[actionID] = metaData
		}
	}
}

func (cb *ConbinedBackend) Get(ctx context.Context, actionID string) (diskPath string, metaData *MetaData, err error) {
	requestGauge.Set(1, "get")
	defer requestGauge.Set(0, "get")

	durationGauge.Stopwatch(func() {
		indexEntry, ok := cb.metaDataMap[actionID]
		if !ok {
			cacheHitGauge.Set(0, "meta_miss")
			return
		}

		diskPath, err = cb.local.Get(ctx, indexEntry.OutputId)
		if err != nil {
			err = fmt.Errorf("get local cache: %w", err)
			return
		}

		if diskPath == "" {
			cacheHitGauge.Set(0, "local_miss")
			return
		}

		cb.newMetaDataMapLocker.Lock()
		defer cb.newMetaDataMapLocker.Unlock()
		indexEntry.LastUsedAt = cb.nowTimestamp
		cb.newMetaDataMap[actionID] = indexEntry

		cacheHitGauge.Set(1, "hit")

		metaData = &MetaData{
			OutputID: indexEntry.OutputId,
			Size:     indexEntry.Size,
			Timenano: indexEntry.Timenano,
		}
		err = nil
	}, "get")

	return diskPath, metaData, err
}

func (cb *ConbinedBackend) Put(ctx context.Context, actionID, outputID string, size int64, body myio.ClonableReadSeeker) (diskPath string, err error) {
	requestGauge.Set(1, "put")
	defer requestGauge.Set(0, "put")

	durationGauge.Stopwatch(func() {
		indexEntry := &v1.IndexEntry{
			OutputId:   outputID,
			Size:       size,
			Timenano:   time.Now().UnixNano(),
			LastUsedAt: cb.nowTimestamp,
		}

		func() {
			cb.newMetaDataMapLocker.Lock()
			defer cb.newMetaDataMapLocker.Unlock()
			cb.newMetaDataMap[actionID] = indexEntry
		}()

		var ok bool
		func() {
			cb.objectMapLocker.Lock()
			defer cb.objectMapLocker.Unlock()

			_, ok = cb.objectMap[outputID]
			if !ok {
				cb.objectMap[outputID] = struct{}{}
			}
		}()
		if ok {
			diskPath, err = cb.local.Get(ctx, outputID)
			if err != nil {
				err = fmt.Errorf("get local cache: %w", err)
				return
			}

			if diskPath != "" {
				return
			}
		}

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

		cb.eg.Go(func() error {
			if err := cb.remote.Put(context.Background(), outputID, size, remoteReader); err != nil {
				return fmt.Errorf("put remote cache: %w", err)
			}

			return nil
		})

		var w io.WriteCloser
		diskPath, w, err = cb.local.Put(ctx, outputID, size)
		if err != nil {
			err = fmt.Errorf("put: %w", err)
			return
		}
		defer w.Close()

		if _, cpErr := io.Copy(w, localReader); cpErr != nil {
			err = fmt.Errorf("copy: %w", cpErr)
			return
		}
	}, "put")

	return diskPath, err
}

func (cb *ConbinedBackend) Close(ctx context.Context) (err error) {
	requestGauge.Set(1, "close")
	defer requestGauge.Set(0, "close")

	durationGauge.Stopwatch(func() {
		if waitErr := cb.eg.Wait(); waitErr != nil {
			err = fmt.Errorf("wait for all tasks: %w", waitErr)
			return
		}

		if writeErr := cb.remote.WriteMetaData(context.Background(), cb.newMetaDataMap); writeErr != nil {
			err = fmt.Errorf("write remote metadata: %w", writeErr)
			return
		}

		if closeErr := cb.remote.Close(ctx); closeErr != nil {
			err = fmt.Errorf("close remote backend: %w", closeErr)
			return
		}

		if closeErr := cb.local.Close(ctx); closeErr != nil {
			err = fmt.Errorf("close backend: %w", closeErr)
			return
		}

		requestGauge.Set(0, "close")
	}, "close")

	return err
}
