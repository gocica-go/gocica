package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	myio "github.com/mazrean/gocica/internal/pkg/io"
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
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Put(ctx context.Context, outputID string, size int64) (diskPath string, w io.WriteCloser, err error)
	Close(ctx context.Context) error
}

type RemoteBackend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
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
	objectMap       map[string]struct{}

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

	b.newMetaDataMap = make(map[string]*v1.IndexEntry, len(b.metaDataMap))
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

	if diskPath == "" {
		return "", nil, nil
	}

	b.newMetaDataMapLocker.Lock()
	defer b.newMetaDataMapLocker.Unlock()
	indexEntry.LastUsedAt = b.nowTimestamp
	b.newMetaDataMap[actionID] = indexEntry

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
			return "", fmt.Errorf("get local cache: %w", err)
		}

		if diskPath != "" {
			return diskPath, nil
		}

		return diskPath, nil
	}

	var (
		remoteReader io.ReadSeeker
		localReader  io.Reader
		printReader  io.Reader
	)
	if size == 0 {
		remoteReader = myio.EmptyReader
		localReader = myio.EmptyReader
		printReader = myio.EmptyReader
	} else {
		remoteReader = body
		localReader = body.Clone()
		printReader = body.Clone()
	}

	sb := strings.Builder{}
	_, err = io.Copy(&sb, printReader)
	if err != nil {
		return "", fmt.Errorf("copy: %w", err)
	}
	b.logger.Debugf("printReader: %s %s", outputID, sb.String())

	b.eg.Go(func() error {
		if err := b.remote.Put(context.Background(), outputID, size, remoteReader); err != nil {
			return fmt.Errorf("put remote cache: %w", err)
		}

		return nil
	})

	var w io.WriteCloser
	diskPath, w, err = b.local.Put(ctx, outputID, size)
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}
	defer w.Close()

	if _, err := io.Copy(w, localReader); err != nil {
		return "", fmt.Errorf("copy: %w", err)
	}

	return diskPath, nil
}

func (b *ConbinedBackend) Close(ctx context.Context) error {
	if err := b.eg.Wait(); err != nil {
		return fmt.Errorf("wait for all tasks: %w", err)
	}

	if err := b.remote.WriteMetaData(context.Background(), b.newMetaDataMap); err != nil {
		return fmt.Errorf("write remote metadata: %w", err)
	}

	if err := b.remote.Close(ctx); err != nil {
		return fmt.Errorf("close remote backend: %w", err)
	}

	if err := b.local.Close(ctx); err != nil {
		return fmt.Errorf("close backend: %w", err)
	}

	return nil
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}
