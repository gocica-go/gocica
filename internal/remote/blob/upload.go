package blob

//go:generate go tool mockgen -source=$GOFILE -destination=mock/${GOFILE} -package=mock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/DataDog/zstd"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var compressGauge = metrics.NewGauge("blob_compress_latency")

type Uploader struct {
	logger log.Logger

	client UploadClient

	s             singleflight.Group
	outputsLocker sync.RWMutex
	outputs       []*v1.ActionsOutput
	outputMap     map[string]int64

	nowTimestamp *timestamppb.Timestamp
	headerLocker sync.RWMutex
	header       map[string]*v1.IndexEntry

	waitBaseFunc waitBaseFunc
}

type UploadClient interface {
	UploadBlock(ctx context.Context, blockID string, r io.ReadSeekCloser) (int64, error)
	UploadBlockFromURL(ctx context.Context, blockID string, url string, offset, size int64) error
	Commit(ctx context.Context, blockIDs []string) error
}

type BaseBlobProvider interface {
	GetEntries(ctx context.Context) (entries map[string]*v1.IndexEntry, err error)
	GetOutputs(ctx context.Context) (outputs []*v1.ActionsOutput, err error)
	GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error)
}

type waitBaseFunc func() (baseBlockIDs []string, baseOutputSize int64, baseOutputs []*v1.ActionsOutput, err error)

func NewUploader(
	ctx context.Context,
	logger log.Logger,
	client UploadClient,
	baseBlobProvider BaseBlobProvider,
) (*Uploader, error) {
	if baseBlobProvider == nil {
		return &Uploader{
			logger:       logger,
			client:       client,
			nowTimestamp: timestamppb.Now(),
			outputMap:    make(map[string]int64),
			header:       make(map[string]*v1.IndexEntry),
			waitBaseFunc: func() ([]string, int64, []*v1.ActionsOutput, error) {
				return nil, 0, nil, nil
			},
		}, nil
	}

	entries, err := baseBlobProvider.GetEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entries: %w", err)
	}

	newEntries := make(map[string]*v1.IndexEntry, len(entries))
	metaLimitLastUsedAt := time.Now().Add(-time.Hour * 24 * 7)
	for actionID, entry := range entries {
		if entry.LastUsedAt.AsTime().After(metaLimitLastUsedAt) {
			newEntries[actionID] = entry
		}
	}

	uploader := &Uploader{
		logger:       logger,
		client:       client,
		nowTimestamp: timestamppb.Now(),
		header:       newEntries,
		outputMap:    make(map[string]int64),
	}

	uploader.waitBaseFunc = uploader.setupBase(ctx, baseBlobProvider)

	return uploader, nil
}

func (u *Uploader) generateBlockID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf[:]), nil
}

const maxUploadChunkSize = 4 * (1 << 20)

func (u *Uploader) setupBase(ctx context.Context, baseBlobProvider BaseBlobProvider) waitBaseFunc {
	eg, ctx := errgroup.WithContext(ctx)

	var (
		baseBlockIDs   []string
		baseOutputSize int64
	)
	eg.Go(func() error {
		url, offset, size, err := baseBlobProvider.GetOutputBlockURL(ctx)
		if err != nil {
			return fmt.Errorf("get output block URL: %w", err)
		}
		baseOutputSize = size

		var uploadSize int64
		for i := int64(0); i < size; i += uploadSize {
			baseBlockID, err := u.generateBlockID()
			if err != nil {
				return fmt.Errorf("generate block ID: %w", err)
			}
			baseBlockIDs = append(baseBlockIDs, baseBlockID)

			chunkUploadSize := min(maxUploadChunkSize, size-i)
			uploadSize = chunkUploadSize
			eg.Go(func() error {
				err = u.client.UploadBlockFromURL(ctx, baseBlockID, url, offset+i, chunkUploadSize)
				if err != nil {
					return fmt.Errorf("upload block from URL: %w", err)
				}

				return nil
			})
		}

		return nil
	})

	var baseOutputs []*v1.ActionsOutput
	eg.Go(func() error {
		var err error
		baseOutputs, err = baseBlobProvider.GetOutputs(ctx)
		if err != nil {
			return fmt.Errorf("download outputs: %w", err)
		}

		return nil
	})

	return func() ([]string, int64, []*v1.ActionsOutput, error) {
		if err := eg.Wait(); err != nil {
			return nil, 0, nil, err
		}
		u.logger.Debugf("base output size=%d", baseOutputSize)

		return baseBlockIDs, baseOutputSize, baseOutputs, nil
	}
}

func (u *Uploader) UploadOutput(ctx context.Context, actionID, outputID string, size int64, r io.ReadSeekCloser) error {
	iUploadSize, err, _ := u.s.Do(outputID, func() (any, error) {
		var (
			uploadSize int64
			ok         bool
		)
		func() {
			u.outputsLocker.RLock()
			defer u.outputsLocker.RUnlock()
			uploadSize, ok = u.outputMap[outputID]
		}()
		if ok {
			return uploadSize, nil
		}

		var (
			reader      io.ReadSeeker
			compression v1.Compression
		)
		if size > 100*(2^10) {
			buf := bytes.NewBuffer(nil)
			zw := zstd.NewWriterLevel(buf, 1)

			var err error
			compressGauge.Stopwatch(func() {
				_, err = io.Copy(zw, r)
			}, "compress_data")
			if err != nil {
				return uint64(0), fmt.Errorf("compress data: %w", err)
			}

			if err := zw.Close(); err != nil {
				return uint64(0), fmt.Errorf("close compressor: %w", err)
			}

			reader = bytes.NewReader(buf.Bytes())
			compression = v1.Compression_COMPRESSION_ZSTD
		} else {
			reader = r
			compression = v1.Compression_COMPRESSION_UNSPECIFIED
		}

		if size == 0 {
			uploadSize = 0
		} else {
			var err error
			uploadSize, err = u.client.UploadBlock(ctx, outputID, myio.NopSeekCloser(reader))
			if err != nil {
				return uint64(0), fmt.Errorf("upload block: %w", err)
			}
		}

		func() {
			u.logger.Debugf("append output lock waiting: actionID=%s, outputID=%s", actionID, outputID)
			u.outputsLocker.Lock()
			defer u.outputsLocker.Unlock()
			u.logger.Debugf("append output lock acquired: actionID=%s, outputID=%s", actionID, outputID)

			u.outputs = append(u.outputs, &v1.ActionsOutput{
				Id:          outputID,
				Size:        uploadSize,
				Compression: compression,
			})
			u.outputMap[outputID] = uploadSize
		}()

		return uploadSize, nil
	})
	if err != nil {
		return fmt.Errorf("upload output: %w", err)
	}

	uploadSize, ok := iUploadSize.(int64)
	if !ok {
		return fmt.Errorf("upload size is not int64: %v", iUploadSize)
	}

	func() {
		u.logger.Debugf("update entry lock waiting: actionID=%s, outputID=%s", actionID, outputID)
		u.headerLocker.Lock()
		defer u.headerLocker.Unlock()
		u.logger.Debugf("update entry lock acquired: actionID=%s, outputID=%s", actionID, outputID)

		u.header[actionID] = &v1.IndexEntry{
			OutputId:   outputID,
			Size:       uploadSize,
			Timenano:   time.Now().UnixNano(),
			LastUsedAt: u.nowTimestamp,
		}
	}()

	return nil
}

func (u *Uploader) UpdateEntry(_ context.Context, actionID string, entry *v1.IndexEntry) error {
	entry.LastUsedAt = u.nowTimestamp

	u.logger.Debugf("update entry lock waiting: actionID=%s, outputID=%s", actionID, entry.OutputId)
	u.headerLocker.Lock()
	defer u.headerLocker.Unlock()
	u.logger.Debugf("update entry lock acquired: actionID=%s, outputID=%s", actionID, entry.OutputId)

	u.header[actionID] = entry

	return nil
}

func (u *Uploader) constructOutputs(baseOutputSize int64, baseOutputs []*v1.ActionsOutput) ([]string, []*v1.ActionsOutput, int64) {
	var newOutputs []*v1.ActionsOutput
	func() {
		u.outputsLocker.RLock()
		defer u.outputsLocker.RUnlock()
		newOutputs = u.outputs
	}()

	outputMap := make(map[string]struct{}, len(newOutputs)+len(baseOutputs))
	for _, output := range baseOutputs {
		outputMap[output.Id] = struct{}{}
	}
	outputs := baseOutputs
	offset := baseOutputSize
	newOutputIDs := make([]string, 0, len(newOutputs))
	for _, output := range newOutputs {
		if _, ok := outputMap[output.Id]; ok {
			continue
		}

		outputMap[output.Id] = struct{}{}
		output.Offset = offset
		offset += output.Size
		outputs = append(outputs, output)
		if output.Size != 0 {
			newOutputIDs = append(newOutputIDs, output.Id)
		}
	}

	return newOutputIDs, outputs, offset
}

func (u *Uploader) createHeader(entries map[string]*v1.IndexEntry, outputs []*v1.ActionsOutput, outputSize int64) ([]byte, error) {
	actionsCache := &v1.ActionsCache{
		Entries:         entries,
		Outputs:         outputs,
		OutputTotalSize: outputSize,
	}

	protobufBuf, err := proto.Marshal(actionsCache)
	if err != nil {
		return nil, fmt.Errorf("marshal actions cache: %w", err)
	}

	buf := make([]byte, 8, 8+len(protobufBuf))
	binary.BigEndian.PutUint64(buf, uint64(len(protobufBuf)))
	buf = append(buf, protobufBuf...)

	return buf, nil
}

func (u *Uploader) Commit(ctx context.Context) (int64, error) {
	baseBlockIDs, baseOutputSize, baseOutputs, err := u.waitBaseFunc()
	if err != nil {
		u.logger.Warnf("failed to upload base: %v", err)
		baseBlockIDs = nil
		baseOutputSize = 0
		baseOutputs = []*v1.ActionsOutput{}
	}

	newOutputIDs, outputs, outputSize := u.constructOutputs(baseOutputSize, baseOutputs)

	var headerBuf []byte
	func() {
		u.logger.Debugf("create header lock waiting")
		u.headerLocker.RLock()
		defer u.headerLocker.RUnlock() //nolint:errcheck
		u.logger.Debugf("create header lock acquired")

		headerBuf, err = u.createHeader(u.header, outputs, outputSize)
	}()
	if err != nil {
		return 0, fmt.Errorf("create header: %w", err)
	}

	headerBlockID, err := u.generateBlockID()
	if err != nil {
		return 0, fmt.Errorf("generate header block ID: %w", err)
	}

	_, err = u.client.UploadBlock(ctx, headerBlockID, myio.NopSeekCloser(bytes.NewReader(headerBuf)))
	if err != nil {
		return 0, fmt.Errorf("upload header: %w", err)
	}

	blockIDs := make([]string, 0, len(newOutputIDs)+2)
	blockIDs = append(blockIDs, headerBlockID)
	blockIDs = append(blockIDs, baseBlockIDs...)
	blockIDs = append(blockIDs, newOutputIDs...)
	err = u.client.Commit(ctx, blockIDs)
	if err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return int64(len(headerBuf)) + outputSize, nil
}
