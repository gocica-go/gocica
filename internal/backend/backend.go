package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
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
	Get(ctx context.Context, actionID string, w io.Writer) (meta *MetaData, err error)
	Put(ctx context.Context, actionID string, meta *MetaData, r io.Reader) error
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

	backend Backend
	//remote  RemoteBackend

	nowTimestamp         *timestamppb.Timestamp
	metaDataMap          map[string]*v1.IndexEntry
	newMetaDataMapLocker sync.Mutex
	newMetaDataMap       map[string]*v1.IndexEntry
}

func NewConbinedBackend(logger log.Logger, backend Backend) (*ConbinedBackend, error) {
	conbined := &ConbinedBackend{
		logger:       logger,
		backend:      backend,
		nowTimestamp: timestamppb.Now(),
	}

	conbined.start()

	return conbined, nil
}

func (b *ConbinedBackend) start() {
	metaDataMap, err := b.backend.MetaData(context.Background())
	if err != nil {
		b.logger.Errorf("parse local metadata: %v. ignore the all local cache.", err)
	}

	b.metaDataMap = metaDataMap

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

	diskPath, err = b.backend.Get(ctx, indexEntry.OutputId)
	if err != nil {
		return "", nil, fmt.Errorf("get local cache: %w", err)
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

	diskPath, err = b.backend.Put(ctx, outputID, size, body)
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}

	return diskPath, nil
}

func (b *ConbinedBackend) Close() error {
	if err := b.backend.WriteMetaData(context.Background(), b.newMetaDataMap); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	if err := b.backend.Close(); err != nil {
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
