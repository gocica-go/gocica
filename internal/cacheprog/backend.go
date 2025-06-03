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

func (b *ConbinedBackend) start() {
	var err error
	b.metaDataMap, err = b.remote.MetaData(context.Background())
	if err != nil {
		b.logger.Warnf("parse remote metadata: %v. ignore the all remote cache.", err)
	}
	if b.metaDataMap == nil {
		b.metaDataMap = map[string]*v1.IndexEntry{}
	}

	for _, indexEntry := range b.metaDataMap {
		b.objectMap[indexEntry.OutputId] = struct{}{}
	}

	b.newMetaDataMap = make(map[string]*v1.IndexEntry, len(b.metaDataMap))
	metaLimitLastUsedAt := time.Now().Add(-time.Hour * 24 * 7)
	for actionID, metaData := range b.metaDataMap {
		if metaData.LastUsedAt.AsTime().After(metaLimitLastUsedAt) {
			b.newMetaDataMap[actionID] = metaData
		}
	}
}

func (b *ConbinedBackend) Get(ctx context.Context, actionID string) (diskPath string, metaData *MetaData, err error) {
	requestGauge.Set(1, "get")
	defer requestGauge.Set(0, "get")

	durationGauge.Stopwatch(func() {
		indexEntry, ok := b.metaDataMap[actionID]
		if !ok {
			cacheHitGauge.Set(0, "meta_miss")
			return
		}

		diskPath, err = b.local.Get(ctx, indexEntry.OutputId)
		if err != nil {
			err = fmt.Errorf("get local cache: %w", err)
			return
		}

		if diskPath == "" {
			cacheHitGauge.Set(0, "local_miss")
			return
		}

		b.newMetaDataMapLocker.Lock()
		defer b.newMetaDataMapLocker.Unlock()
		indexEntry.LastUsedAt = b.nowTimestamp
		b.newMetaDataMap[actionID] = indexEntry

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

func (b *ConbinedBackend) Put(ctx context.Context, actionID, outputID string, size int64, body myio.ClonableReadSeeker) (diskPath string, err error) {
	requestGauge.Set(1, "put")
	defer requestGauge.Set(0, "put")

	durationGauge.Stopwatch(func() {
		indexEntry := &v1.IndexEntry{
			OutputId:   outputID,
			Size:       size,
			Timenano:   time.Now().UnixNano(),
			LastUsedAt: b.nowTimestamp,
		}

		func() {
			b.newMetaDataMapLocker.Lock()
			defer b.newMetaDataMapLocker.Unlock()
			b.newMetaDataMap[actionID] = indexEntry
		}()

		var ok bool
		func() {
			b.objectMapLocker.Lock()
			defer b.objectMapLocker.Unlock()

			_, ok = b.objectMap[outputID]
			if !ok {
				b.objectMap[outputID] = struct{}{}
			}
		}()
		if ok {
			diskPath, err = b.local.Get(ctx, outputID)
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

		b.eg.Go(func() error {
			if err := b.remote.Put(context.Background(), outputID, size, remoteReader); err != nil {
				return fmt.Errorf("put remote cache: %w", err)
			}

			return nil
		})

		var w io.WriteCloser
		diskPath, w, err = b.local.Put(ctx, outputID, size)
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

func (b *ConbinedBackend) Close(ctx context.Context) (err error) {
	requestGauge.Set(1, "close")
	defer requestGauge.Set(0, "close")

	durationGauge.Stopwatch(func() {
		if waitErr := b.eg.Wait(); waitErr != nil {
			err = fmt.Errorf("wait for all tasks: %w", waitErr)
			return
		}

		if writeErr := b.remote.WriteMetaData(context.Background(), b.newMetaDataMap); writeErr != nil {
			err = fmt.Errorf("write remote metadata: %w", writeErr)
			return
		}

		if closeErr := b.remote.Close(ctx); closeErr != nil {
			err = fmt.Errorf("close remote backend: %w", closeErr)
			return
		}

		if closeErr := b.local.Close(ctx); closeErr != nil {
			err = fmt.Errorf("close backend: %w", closeErr)
			return
		}

		requestGauge.Set(0, "close")
	}, "close")

	return err
}
