package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Backend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Put(ctx context.Context, outputID string, size int64, body io.Reader) (diskPath string, err error)
	Close() error
}

type RemoteBackend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
	Get(ctx context.Context, objectID string, w io.Writer) error
	Put(ctx context.Context, objectID string, size int64, r io.Reader) error
	Close() error
}

type MetaData struct {
	// OutputID is the unique identifier for the object.
	OutputID string
	// Size is the size of the object in bytes.
	Size int64
	// Timenano is the time the object was created in Unix nanoseconds.
	Timenano int64
}

type ConbinedBackend struct {
	logger log.Logger
	eg     *errgroup.Group

	local  Backend
	remote RemoteBackend

	nowTimestamp         *timestamppb.Timestamp
	metaDataMap          map[string]*v1.IndexEntry
	newMetaDataMapLocker sync.Mutex
	newMetaDataMap       map[string]*v1.IndexEntry
}

func NewConbinedBackend(logger log.Logger, local Backend, remote RemoteBackend) (*ConbinedBackend, error) {
	conbined := &ConbinedBackend{
		logger:       logger,
		eg:           &errgroup.Group{},
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
		b.logger.Errorf("parse local metadata: %v. ignore the all local cache.", err)
	}

	remoteMetaDataMap, err := b.remote.MetaData(context.Background())
	if err != nil {
		b.logger.Errorf("parse remote metadata: %v. ignore the all remote cache.", err)
	}

	b.metaDataMap = metaDataMap
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

	diskPath, err = b.local.Get(ctx, indexEntry.OutputId)
	if err != nil {
		return "", nil, fmt.Errorf("get local cache: %w", err)
	}

	if diskPath != "" {
		func() {
			b.newMetaDataMapLocker.Lock()
			defer b.newMetaDataMapLocker.Unlock()
			indexEntry.LastUsedAt = b.nowTimestamp
			b.newMetaDataMap[actionID] = indexEntry
		}()

		return diskPath, &MetaData{
			OutputID: indexEntry.OutputId,
			Size:     indexEntry.Size,
			Timenano: indexEntry.Timenano,
		}, nil
	}

	eg, ctx := errgroup.WithContext(ctx)
	pr, pw := io.Pipe()
	eg.Go(func() error {
		defer pw.Close()

		err := b.remote.Get(ctx, indexEntry.OutputId, pw)
		if err != nil {
			return fmt.Errorf("get remote cache: %w", err)
		}

		return nil
	})

	diskPath, err = b.local.Put(ctx, indexEntry.OutputId, indexEntry.Size, pr)
	if err != nil {
		if errors.Is(err, ErrSizeMismatch) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("put remote cache: %w", err)
	}

	if diskPath == "" {
		return "", nil, nil
	}

	if err := eg.Wait(); err != nil {
		return "", nil, fmt.Errorf("wait for get remote cache: %w", err)
	}

	func() {
		b.newMetaDataMapLocker.Lock()
		defer b.newMetaDataMapLocker.Unlock()
		indexEntry.LastUsedAt = b.nowTimestamp
		b.newMetaDataMap[actionID] = indexEntry
	}()

	return diskPath, &MetaData{
		OutputID: indexEntry.OutputId,
		Size:     indexEntry.Size,
		Timenano: indexEntry.Timenano,
	}, nil
}

func (b *ConbinedBackend) Put(ctx context.Context, actionID, outputID string, size int64, body io.Reader) (diskPath string, err error) {
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

	diskPath, err = b.local.Put(ctx, outputID, size, body)
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}

	b.eg.Go(func() error {
		if err := b.remote.Put(ctx, outputID, size, body); err != nil {
			return fmt.Errorf("put remote cache: %w", err)
		}

		return nil
	})

	return diskPath, nil
}

func (b *ConbinedBackend) Close() error {
	if err := b.eg.Wait(); err != nil {
		return fmt.Errorf("wait for all tasks: %w", err)
	}

	if err := b.local.WriteMetaData(context.Background(), b.newMetaDataMap); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	if err := b.local.Close(); err != nil {
		return fmt.Errorf("close backend: %w", err)
	}

	return nil
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}

func decodeID(id string) string {
	return strings.ReplaceAll(id, "-", "/")
}
