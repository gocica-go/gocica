package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/locker"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Backend interface {
	Get(ctx context.Context, actionID string) (diskPath string, metaData *MetaData, err error)
	Put(ctx context.Context, actionID, outputID string, size int64, body myio.ClonableReadSeeker) (diskPath string, err error)
	Close(ctx context.Context) error
}

type LocalBackend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Put(ctx context.Context, outputID string, size int64, body io.Reader) (diskPath string, err error)
	Close(ctx context.Context) error
}

type RemoteBackend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
	Get(ctx context.Context, objectID string, size int64, w io.Writer) error
	Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error
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

type ConbinedBackend struct {
	logger log.Logger

	local  LocalBackend
	remote RemoteBackend

	objectMapLocker sync.Mutex
	objectMap       map[string]*locker.WaitSuccess

	eg                   *errgroup.Group
	nowTimestamp         *timestamppb.Timestamp
	metaDataMap          map[string]*v1.IndexEntry
	newMetaDataMapLocker sync.Mutex
	newMetaDataMap       map[string]*v1.IndexEntry
}

func NewConbinedBackend(logger log.Logger, local LocalBackend, remote RemoteBackend) (*ConbinedBackend, error) {
	conbined := &ConbinedBackend{
		logger:       logger,
		eg:           &errgroup.Group{},
		objectMap:    map[string]*locker.WaitSuccess{},
		local:        local,
		remote:       remote,
		nowTimestamp: timestamppb.Now(),
	}

	conbined.start()

	return conbined, nil
}

func (b *ConbinedBackend) start() {
	metaDataMap, err := b.local.MetaData(context.Background())
	if err != nil {
		b.logger.Warnf("parse local metadata: %v. ignore the all local cache.", err)
	}

	remoteMetaDataMap, err := b.remote.MetaData(context.Background())
	if err != nil {
		b.logger.Warnf("parse remote metadata: %v. ignore the all remote cache.", err)
	}

	b.metaDataMap = metaDataMap
	if b.metaDataMap == nil {
		b.metaDataMap = make(map[string]*v1.IndexEntry, len(remoteMetaDataMap))
	}
	for actionID, remoteMetaData := range remoteMetaDataMap {
		localMetaData, ok := b.metaDataMap[actionID]
		if ok && localMetaData.LastUsedAt.AsTime().After(remoteMetaData.LastUsedAt.AsTime()) {
			continue
		}

		b.metaDataMap[actionID] = remoteMetaData
	}

	b.newMetaDataMap = make(map[string]*v1.IndexEntry, len(metaDataMap))
	metaLimitLastUsedAt := time.Now().Add(-time.Hour * 24 * 7)
	for actionID, metaData := range b.metaDataMap {
		if metaData.LastUsedAt.AsTime().After(metaLimitLastUsedAt) {
			b.newMetaDataMap[actionID] = metaData
		}
	}
}

func (b *ConbinedBackend) Get(ctx context.Context, actionID string) (diskPath string, metaData *MetaData, err error) {
	indexEntry, ok := b.metaDataMap[actionID]
	if !ok {
		return "", nil, nil
	}

	var waitSuccess *locker.WaitSuccess
	func() {
		b.objectMapLocker.Lock()
		defer b.objectMapLocker.Unlock()
		waitSuccess, ok = b.objectMap[indexEntry.OutputId]
		if !ok {
			waitSuccess = locker.NewWaitSuccess()
			b.objectMap[indexEntry.OutputId] = waitSuccess
		}
	}()

	waitSuccess.Run(func() bool {
		diskPath, err = b.local.Get(ctx, indexEntry.OutputId)
		if err != nil {
			err = fmt.Errorf("get local cache: %w", err)
			return false
		}

		if diskPath != "" {
			func() {
				b.newMetaDataMapLocker.Lock()
				defer b.newMetaDataMapLocker.Unlock()
				indexEntry.LastUsedAt = b.nowTimestamp
				b.newMetaDataMap[actionID] = indexEntry
			}()

			return true
		}

		eg, ctx := errgroup.WithContext(ctx)
		var r io.Reader
		if indexEntry.Size == 0 {
			r = myio.EmptyReader
		} else {
			pr, pw := io.Pipe()

			r = pr

			eg.Go(func() error {
				defer pw.Close()

				err := b.remote.Get(ctx, indexEntry.OutputId, indexEntry.Size, pw)
				if err != nil {
					return fmt.Errorf("get remote cache: %w", err)
				}

				return nil
			})
		}

		diskPath, err = b.local.Put(ctx, indexEntry.OutputId, indexEntry.Size, r)
		if err != nil {
			if errors.Is(err, ErrSizeMismatch) {
				err = nil
				return false
			}
			err = fmt.Errorf("put: %w", err)
			return false
		}

		if diskPath == "" {
			err = nil
			return false
		}

		if err = eg.Wait(); err != nil {
			err = fmt.Errorf("wait for get remote cache: %w", err)
			return false
		}

		func() {
			b.newMetaDataMapLocker.Lock()
			defer b.newMetaDataMapLocker.Unlock()
			indexEntry.LastUsedAt = b.nowTimestamp
			b.newMetaDataMap[actionID] = indexEntry
		}()

		return true
	}, func() {
		diskPath, err = b.local.Get(ctx, indexEntry.OutputId)
		if err != nil {
			err = fmt.Errorf("get local cache: %w", err)
			return
		}
	})
	if err != nil {
		return "", nil, err
	}

	return diskPath, &MetaData{
		OutputID: indexEntry.OutputId,
		Size:     indexEntry.Size,
		Timenano: indexEntry.Timenano,
	}, nil
}

func (b *ConbinedBackend) Put(ctx context.Context, actionID, outputID string, size int64, body myio.ClonableReadSeeker) (diskPath string, err error) {
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

	var waitSuccess *locker.WaitSuccess
	func() {
		var ok bool

		b.objectMapLocker.Lock()
		defer b.objectMapLocker.Unlock()

		waitSuccess, ok = b.objectMap[outputID]
		if !ok {
			waitSuccess = locker.NewWaitSuccess()
			b.objectMap[outputID] = waitSuccess
		}
	}()

	waitSuccess.Run(func() bool {
		var (
			localReader io.Reader
		)
		if size == 0 {
			localReader = myio.EmptyReader
		} else {
			localReader = body

			b.eg.Go(func() error {
				if err := b.remote.Put(context.Background(), outputID, size, body.Clone()); err != nil {
					return fmt.Errorf("put remote cache: %w", err)
				}

				return nil
			})
		}

		diskPath, err = b.local.Put(ctx, outputID, size, localReader)
		if err != nil {
			err = fmt.Errorf("put: %w", err)
			return false
		}

		return true
	}, func() {
		diskPath, err = b.local.Get(ctx, outputID)
		if err != nil {
			err = fmt.Errorf("get local cache: %w", err)
			return
		}
	})
	if err != nil {
		return "", err
	}

	return diskPath, nil
}

func (b *ConbinedBackend) Close(ctx context.Context) error {
	if err := b.eg.Wait(); err != nil {
		return fmt.Errorf("wait for all tasks: %w", err)
	}

	b.eg.Go(func() error {
		if err := b.remote.WriteMetaData(context.Background(), b.newMetaDataMap); err != nil {
			return fmt.Errorf("write remote metadata: %w", err)
		}

		if err := b.remote.Close(ctx); err != nil {
			return fmt.Errorf("close remote backend: %w", err)
		}

		return nil
	})

	if err := b.local.WriteMetaData(context.Background(), b.newMetaDataMap); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	if err := b.local.Close(ctx); err != nil {
		return fmt.Errorf("close backend: %w", err)
	}

	if err := b.eg.Wait(); err != nil {
		return fmt.Errorf("wait for all tasks: %w", err)
	}

	return nil
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}

func decodeID(id string) string {
	return strings.ReplaceAll(id, "-", "/")
}
