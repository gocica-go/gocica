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

	lz4 "github.com/DataDog/golz4"
	"github.com/mazrean/gocica/internal/metrics"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

var compressGauge = metrics.NewGauge("blob_compress_latency")

type Uploader struct {
	logger              log.Logger
	client              UploadClient
	outputSizeMapLocker sync.RWMutex
	outputSizeMap       map[string]*v1.ActionsOutput
	waitBaseFunc        waitBaseFunc
}

type UploadClient interface {
	UploadBlock(ctx context.Context, blockID string, r io.ReadSeekCloser) (int64, error)
	UploadBlockFromURL(ctx context.Context, blockID string, url string, offset, size int64) error
	Commit(ctx context.Context, blockIDs []string) error
}

type BaseBlobProvider interface {
	GetOutputs(ctx context.Context) (outputs map[string]*v1.ActionsOutput, err error)
	GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error)
}

type waitBaseFunc func() (baseBlockID string, baseOutputSize int64, baseOutputs map[string]*v1.ActionsOutput, err error)

func NewUploader(ctx context.Context, logger log.Logger, client UploadClient, baseBlobProvider BaseBlobProvider) *Uploader {
	uploader := &Uploader{
		logger:        logger,
		client:        client,
		outputSizeMap: map[string]*v1.ActionsOutput{},
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

func (u *Uploader) setupBase(ctx context.Context, baseBlobProvider BaseBlobProvider) waitBaseFunc {
	if baseBlobProvider == nil {
		return func() (string, int64, map[string]*v1.ActionsOutput, error) {
			return "", 0, map[string]*v1.ActionsOutput{}, nil
		}
	}

	eg, ctx := errgroup.WithContext(ctx)

	var (
		baseBlockID    string
		baseOutputSize int64
	)
	eg.Go(func() error {
		var err error
		baseBlockID, err = u.generateBlockID()
		if err != nil {
			return fmt.Errorf("generate block ID: %w", err)
		}

		url, offset, size, err := baseBlobProvider.GetOutputBlockURL(ctx)
		if err != nil {
			return fmt.Errorf("get output block URL: %w", err)
		}
		baseOutputSize = size

		err = u.client.UploadBlockFromURL(ctx, baseBlockID, url, offset, size)
		if err != nil {
			return fmt.Errorf("upload block from URL: %w", err)
		}

		return nil
	})

	var baseOutputs map[string]*v1.ActionsOutput
	eg.Go(func() error {
		var err error
		baseOutputs, err = baseBlobProvider.GetOutputs(ctx)
		if err != nil {
			return fmt.Errorf("download outputs: %w", err)
		}

		return nil
	})

	return func() (string, int64, map[string]*v1.ActionsOutput, error) {
		if err := eg.Wait(); err != nil {
			return "", 0, nil, err
		}
		u.logger.Debugf("base output size=%d", baseOutputSize)

		return baseBlockID, baseOutputSize, baseOutputs, nil
	}
}

func (u *Uploader) UploadOutput(ctx context.Context, outputID string, size int64, r io.ReadSeekCloser) error {
	var (
		reader      io.ReadSeeker
		compression v1.Compression
	)
	if size > 100*(1<<10) {
		buf := bytes.NewBuffer(nil)
		lr := lz4.NewCompressReader(r)
		defer func() {
			if err := lr.Close(); err != nil {
				u.logger.Warnf("close lz4 reader: %v", err)
			}
		}()

		_, err := io.Copy(buf, lr)
		if err != nil {
			return fmt.Errorf("compress data: %w", err)
		}

		reader = bytes.NewReader(buf.Bytes())
		compression = v1.Compression_COMPRESSION_LZ4
	} else {
		reader = r
		compression = v1.Compression_COMPRESSION_UNSPECIFIED
	}

	size, err := u.client.UploadBlock(ctx, outputID, myio.NopSeekCloser(reader))
	if err != nil {
		return fmt.Errorf("upload block: %w", err)
	}

	u.outputSizeMapLocker.Lock()
	defer u.outputSizeMapLocker.Unlock()
	u.outputSizeMap[outputID] = &v1.ActionsOutput{
		Size:        size,
		Compression: compression,
	}

	return nil
}

func (u *Uploader) constructOutputs(baseOutputSize int64, baseOutputs map[string]*v1.ActionsOutput) ([]string, map[string]*v1.ActionsOutput, int64) {
	var outputSizeMap map[string]*v1.ActionsOutput
	func() {
		u.outputSizeMapLocker.RLock()
		defer u.outputSizeMapLocker.RUnlock()
		outputSizeMap = u.outputSizeMap
	}()

	outputs := baseOutputs
	offset := baseOutputSize
	newOutputIDs := make([]string, 0, len(outputSizeMap))
	for outputID, output := range outputSizeMap {
		if _, ok := baseOutputs[outputID]; ok {
			continue
		}

		output.Offset = offset
		outputs[outputID] = output
		offset += output.Size
		newOutputIDs = append(newOutputIDs, outputID)
	}

	return newOutputIDs, outputs, offset
}

func (u *Uploader) createHeader(entries map[string]*v1.IndexEntry, outputs map[string]*v1.ActionsOutput, outputSize int64) ([]byte, error) {
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
	baseBlockID, baseOutputSize, baseOutputs, err := u.waitBaseFunc()
	if err != nil {
		return 0, fmt.Errorf("wait base: %w", err)
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
	if baseBlockID != "" {
		blockIDs = append(blockIDs, baseBlockID)
	}
	blockIDs = append(blockIDs, newOutputIDs...)
	err = u.client.Commit(ctx, blockIDs)
	if err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return int64(len(headerBuf)) + outputSize, nil
}
