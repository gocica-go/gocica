package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/DataDog/zstd"
	"github.com/mazrean/gocica/internal/metrics"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

var compressGauge = metrics.NewGauge("blob_compress_latency")

type Uploader struct {
	logger        log.Logger
	client        UploadClient
	outputsLocker sync.RWMutex
	outputs       []*v1.ActionsOutput
	waitBaseFunc  waitBaseFunc
}

type UploadClient interface {
	UploadBlock(ctx context.Context, blockID string, r io.ReadSeekCloser) (int64, error)
	UploadBlockFromURL(ctx context.Context, blockID string, url string, offset, size int64) error
	Commit(ctx context.Context, blockIDs []string) error
}

type BaseBlobProvider interface {
	GetOutputs(ctx context.Context) (outputs []*v1.ActionsOutput, err error)
	GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error)
}

type waitBaseFunc func() (baseBlockIDs []string, baseOutputSize int64, baseOutputs []*v1.ActionsOutput, err error)

func NewUploader(ctx context.Context, logger log.Logger, client UploadClient, baseBlobProvider BaseBlobProvider) *Uploader {
	uploader := &Uploader{
		logger: logger,
		client: client,
	}

	uploader.waitBaseFunc = uploader.setupBase(ctx, baseBlobProvider)

	return uploader
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
	if baseBlobProvider == nil {
		return func() ([]string, int64, []*v1.ActionsOutput, error) {
			return nil, 0, nil, nil
		}
	}

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
			eg.Go(func() error {
				baseBlockID, err := u.generateBlockID()
				if err != nil {
					return fmt.Errorf("generate block ID: %w", err)
				}

				uploadSize = min(maxUploadChunkSize, size-i)
				err = u.client.UploadBlockFromURL(ctx, baseBlockID, url, offset+i, uploadSize)
				if err != nil {
					return fmt.Errorf("upload block from URL: %w", err)
				}

				baseBlockIDs = append(baseBlockIDs, baseBlockID)

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

func (u *Uploader) UploadOutput(ctx context.Context, outputID string, size int64, r io.ReadSeekCloser) error {
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
			return fmt.Errorf("compress data: %w", err)
		}

		if err := zw.Close(); err != nil {
			return fmt.Errorf("close compressor: %w", err)
		}

		reader = bytes.NewReader(buf.Bytes())
		compression = v1.Compression_COMPRESSION_ZSTD
	} else {
		reader = r
		compression = v1.Compression_COMPRESSION_UNSPECIFIED
	}

	var uploadSize int64
	if size == 0 {
		uploadSize = 0
	} else {
		var err error
		uploadSize, err = u.client.UploadBlock(ctx, outputID, myio.NopSeekCloser(reader))
		if err != nil {
			return fmt.Errorf("upload block: %w", err)
		}
	}

	u.outputsLocker.Lock()
	defer u.outputsLocker.Unlock()
	u.outputs = append(u.outputs, &v1.ActionsOutput{
		Id:          outputID,
		Size:        uploadSize,
		Compression: compression,
	})

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

func (u *Uploader) Commit(ctx context.Context, entries map[string]*v1.IndexEntry) (int64, error) {
	baseBlockIDs, baseOutputSize, baseOutputs, err := u.waitBaseFunc()
	if err != nil {
		u.logger.Warnf("failed to upload base: %v", err)
		baseBlockIDs = nil
		baseOutputSize = 0
		baseOutputs = []*v1.ActionsOutput{}
	}

	newOutputIDs, outputs, outputSize := u.constructOutputs(baseOutputSize, baseOutputs)

	headerBuf, err := u.createHeader(entries, outputs, outputSize)
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
