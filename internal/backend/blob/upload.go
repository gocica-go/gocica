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

	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type Uploader struct {
	client              UploadClient
	outputSizeMapLocker sync.RWMutex
	outputSizeMap       map[string]int64
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

func NewUploader(ctx context.Context, client UploadClient, baseBlobProvider BaseBlobProvider) *Uploader {
	uploader := &Uploader{
		client:        client,
		outputSizeMap: map[string]int64{},
	}

	uploader.waitBaseFunc = uploader.setupBase(ctx, baseBlobProvider)

	return uploader
}

func (u *Uploader) getenarteBlockID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf[:]), nil
}

func (u *Uploader) setupBase(ctx context.Context, baseBlobProvider BaseBlobProvider) waitBaseFunc {
	eg, ctx := errgroup.WithContext(ctx)

	var (
		baseBlockID    string
		baseOutputSize int64
	)
	eg.Go(func() error {
		var err error
		baseBlockID, err = u.getenarteBlockID()
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

		return baseBlockID, baseOutputSize, baseOutputs, nil
	}
}

func (u *Uploader) UploadOutput(ctx context.Context, outputID string, size int64, r io.ReadSeekCloser) error {
	n, err := u.client.UploadBlock(ctx, outputID, r)
	if err != nil {
		return fmt.Errorf("upload block: %w", err)
	}

	if n != size {
		return fmt.Errorf("size mismatch: expected=%d, actual=%d", size, n)
	}

	u.outputSizeMapLocker.Lock()
	defer u.outputSizeMapLocker.Unlock()
	u.outputSizeMap[outputID] = size

	return nil
}

func (u *Uploader) constructOutputs(baseOutputSize int64, baseOutputs map[string]*v1.ActionsOutput) ([]string, map[string]*v1.ActionsOutput) {
	var outputSizeMap map[string]int64
	func() {
		u.outputSizeMapLocker.RLock()
		defer u.outputSizeMapLocker.RUnlock()
		outputSizeMap = u.outputSizeMap
	}()

	outputs := baseOutputs
	offset := baseOutputSize
	newOutputIDs := make([]string, 0, len(outputSizeMap))
	for outputID, size := range outputSizeMap {
		if _, ok := baseOutputs[outputID]; ok {
			continue
		}

		outputs[outputID] = &v1.ActionsOutput{
			Offset: offset,
			Size:   size,
		}
		offset += size
		newOutputIDs = append(newOutputIDs, outputID)
	}

	return newOutputIDs, outputs
}

func (u *Uploader) createHeader(entries map[string]*v1.IndexEntry, outputs map[string]*v1.ActionsOutput) ([]byte, error) {
	actionsCache := &v1.ActionsCache{
		Entries: entries,
		Outputs: outputs,
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
	baseBlockID, baseOutputSize, baseOutputs, err := u.waitBaseFunc()
	if err != nil {
		return fmt.Errorf("wait base: %w", err)
	}

	newOutputIDs, outputs := u.constructOutputs(baseOutputSize, baseOutputs)

	headerBuf, err := u.createHeader(entries, outputs)
	if err != nil {
		return fmt.Errorf("create header: %w", err)
	}

	headerBlockID, err := u.getenarteBlockID()
	if err != nil {
		return fmt.Errorf("generate header block ID: %w", err)
	}

	_, err = u.client.UploadBlock(ctx, headerBlockID, myio.NopSeekCloser(bytes.NewReader(headerBuf)))
	if err != nil {
		return fmt.Errorf("upload header: %w", err)
	}

	blockIDs := append([]string{headerBlockID, baseBlockID}, newOutputIDs...)
	err = u.client.Commit(ctx, blockIDs)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}
