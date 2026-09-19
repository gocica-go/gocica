package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/DataDog/zstd"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

var compressGauge = metrics.NewGauge("blob_compress_latency")

// compressThresholdBytes is the size above which an output is zstd-compressed by
// DefaultCompressionPolicy.
const compressThresholdBytes = 100 << 10

// CompressionPolicy reports whether an output should be zstd-compressed.
// It is consulted once per output, before the bytes are read.
type CompressionPolicy func(objectID string, size int64) bool

// DefaultCompressionPolicy compresses every output larger than compressThresholdBytes.
func DefaultCompressionPolicy(_ string, size int64) bool {
	return size > compressThresholdBytes
}

type Uploader struct {
	logger log.Logger
	// warning: client can be nil, which means no upload is needed.
	client        UploadClient
	outputsLocker sync.RWMutex
	outputs       []*v1.ActionsOutput
	compression   CompressionPolicy

	// The base blob copy is started lazily, on the first output upload.
	// Starting it in the constructor would pin a signed URL that can expire
	// before the flush of a long-lived process, silently dropping the whole
	// previous blob. See setupBase.
	baseProvider BaseBlobProvider
	baseOnce     sync.Once
	waitBaseFunc waitBaseFunc

	uploadDisabled atomic.Bool
}

// ErrUploadDisabled is returned by an UploadClient when the remote will not accept
// this run's blob at all, for instance because another job already published the
// same cache key. It is not a failure: the Uploader stops uploading and the build
// carries on with whatever it restored.
var ErrUploadDisabled = errors.New("upload disabled")

// UploadClient defines the interface for uploading blocks to remote storage.
type UploadClient interface {
	UploadBlock(ctx context.Context, blockID string, r io.ReadSeekCloser) (int64, error)
	UploadBlockFromURL(ctx context.Context, blockID string, url string, offset, size int64) error
	Commit(ctx context.Context, blockIDs []string, size int64) error
}

type BaseBlobProvider interface {
	IsEmpty() bool
	GetOutputs(ctx context.Context) (outputs []*v1.ActionsOutput, err error)
	GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error)
}

type waitBaseFunc func() (baseBlockIDs []string, baseOutputSize int64, baseOutputs []*v1.ActionsOutput, err error)

// NewUploader creates a new Uploader with the given client and base blob provider.
// A nil compression policy falls back to DefaultCompressionPolicy.
func NewUploader(ctx context.Context, logger log.Logger, client UploadClient, baseBlobProvider BaseBlobProvider, compression CompressionPolicy) *Uploader {
	if compression == nil {
		compression = DefaultCompressionPolicy
	}

	return &Uploader{
		logger:       logger,
		client:       client,
		compression:  compression,
		baseProvider: baseBlobProvider,
	}
}

// ensureBase starts the base blob copy exactly once. It is called from the first
// UploadOutput so the server-side copy overlaps with the outputs being staged,
// and from Commit for the case where the caller committed without uploading.
func (u *Uploader) ensureBase() waitBaseFunc {
	u.baseOnce.Do(func() {
		u.waitBaseFunc = u.setupBase(u.baseProvider)
	})

	return u.waitBaseFunc
}

func (u *Uploader) generateBlockID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf[:]), nil
}

const maxUploadChunkSize = 4 * (1 << 20)

func (u *Uploader) setupBase(baseBlobProvider BaseBlobProvider) waitBaseFunc {
	if baseBlobProvider.IsEmpty() || u.client == nil {
		return func() ([]string, int64, []*v1.ActionsOutput, error) {
			return nil, 0, nil, nil
		}
	}

	eg, ctx := errgroup.WithContext(context.Background())

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

func (u *Uploader) UploadOutput(ctx context.Context, outputID string, size int64, r io.ReadSeekCloser) error {
	if u.client == nil || u.uploadDisabled.Load() {
		return nil
	}

	var (
		reader      io.ReadSeeker
		compression v1.Compression
	)
	if u.compression(outputID, size) {
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
		if errors.Is(err, ErrUploadDisabled) {
			u.uploadDisabled.Store(true)
			u.logger.Infof("remote refused this run's cache entry. continuing without upload.")

			return nil
		}
		if err != nil {
			return fmt.Errorf("upload block: %w", err)
		}
	}

	// Only now do we know the remote accepts writes, so the server-side copy of the
	// previous blob is worth starting. It then overlaps with the remaining outputs.
	u.ensureBase()

	u.outputsLocker.Lock()
	defer u.outputsLocker.Unlock()
	u.outputs = append(u.outputs, &v1.ActionsOutput{
		Id:          outputID,
		Size:        uploadSize,
		Compression: compression,
	})

	return nil
}

func (u *Uploader) newOutputCount() int {
	u.outputsLocker.RLock()
	defer u.outputsLocker.RUnlock()

	return len(u.outputs)
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

func (u *Uploader) Commit(ctx context.Context, entries map[string]*v1.IndexEntry) error {
	if u.client == nil || u.uploadDisabled.Load() {
		return nil
	}

	// Nothing was staged, so the blob would be a byte-for-byte copy of the base.
	// Skip the whole round trip: the restore key chain still finds the base entry.
	if u.newOutputCount() == 0 {
		u.logger.Infof("no new output in this run. skipping cache entry upload.")
		return nil
	}

	baseBlockIDs, baseOutputSize, baseOutputs, err := u.ensureBase()()
	if err != nil {
		u.logger.Warnf("failed to upload base: %v", err)
		baseBlockIDs = nil
		baseOutputSize = 0
		baseOutputs = []*v1.ActionsOutput{}
	}

	newOutputIDs, outputs, outputSize := u.constructOutputs(baseOutputSize, baseOutputs)

	headerBuf, err := u.createHeader(entries, outputs, outputSize)
	if err != nil {
		return fmt.Errorf("create header: %w", err)
	}

	headerBlockID, err := u.generateBlockID()
	if err != nil {
		return fmt.Errorf("generate header block ID: %w", err)
	}

	_, err = u.client.UploadBlock(ctx, headerBlockID, myio.NopSeekCloser(bytes.NewReader(headerBuf)))
	if errors.Is(err, ErrUploadDisabled) {
		u.logger.Infof("remote refused this run's cache entry. continuing without upload.")

		return nil
	}
	if err != nil {
		return fmt.Errorf("upload header: %w", err)
	}

	blockIDs := make([]string, 0, len(newOutputIDs)+2)
	blockIDs = append(blockIDs, headerBlockID)
	blockIDs = append(blockIDs, baseBlockIDs...)
	blockIDs = append(blockIDs, newOutputIDs...)
	err = u.client.Commit(ctx, blockIDs, int64(len(headerBuf))+outputSize)
	if err != nil {
		return fmt.Errorf("commit: %w", errors.Join(err, context.Cause(ctx)))
	}

	return nil
}
