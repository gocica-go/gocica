package blob

//go:generate go tool mockgen -source=$GOFILE -destination=mock/${GOFILE} -package=mock

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/DataDog/zstd"
	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type Downloader struct {
	logger log.Logger

	client DownloadClient

	headerSize int64
	header     *v1.ActionsCache
}

type DownloadClient interface {
	GetURL(ctx context.Context) string
	DownloadBlock(ctx context.Context, offset int64, size int64, w io.Writer) error
	DownloadBlockBuffer(ctx context.Context, offset int64, size int64, buf []byte) error
}

func NewDownloader(
	ctx context.Context,
	logger log.Logger,
	client DownloadClient,
	localBackend local.Backend,
) (*Downloader, error) {
	downloader := &Downloader{
		logger: logger,
		client: client,
	}

	var err error
	downloader.header, downloader.headerSize, err = downloader.readHeader(ctx)
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	outputsWithOpenerMap, err := downloader.createOutputWithOpenerMap(ctx, downloader.header.Outputs, localBackend)
	if err != nil {
		return nil, fmt.Errorf("create output with openers: %w", err)
	}

	// Download all output blocks in the background.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("panic in download all output blocks: %v", r)
			}
		}()

		if err := downloader.DownloadAllOutputBlocks(context.Background(), outputsWithOpenerMap); err != nil {
			logger.Errorf("download all output blocks: %v", err)
		}
	}()

	return downloader, nil
}

func (d *Downloader) createOutputWithOpenerMap(ctx context.Context, outputs []*v1.ActionsOutput, localBackend local.Backend) (map[string]local.OpenerWithUnlock, error) {
	outputsWithOpeners := make(map[string]local.OpenerWithUnlock, len(outputs))
	for _, output := range outputs {
		if _, ok := outputsWithOpeners[output.Id]; ok {
			continue
		}

		_, opener, err := localBackend.Put(ctx, output.Id)
		if err != nil {
			return nil, fmt.Errorf("put local cache: %w", err)
		}

		if opener != nil {
			outputsWithOpeners[output.Id] = opener
		}
	}

	return outputsWithOpeners, nil
}

func (d *Downloader) readHeader(ctx context.Context) (header *v1.ActionsCache, headerSize int64, err error) {
	sizeBuf := make([]byte, 8)
	err = d.client.DownloadBlockBuffer(ctx, 0, 8, sizeBuf)
	if err != nil {
		return nil, 0, fmt.Errorf("download size buffer: %w", err)
	}
	//nolint:gosec
	protobufSize := int64(binary.BigEndian.Uint64(sizeBuf))

	protoBuf := make([]byte, protobufSize)
	err = d.client.DownloadBlockBuffer(ctx, 8, protobufSize, protoBuf)
	if err != nil {
		return nil, 0, fmt.Errorf("download header buffer: %w", err)
	}

	header = &v1.ActionsCache{}
	if err = proto.Unmarshal(protoBuf, header); err != nil {
		return nil, 0, fmt.Errorf("unmarshal header: %w", err)
	}

	return header, 8 + int64(len(protoBuf)), nil
}

func (d *Downloader) GetEntries(context.Context) (entries map[string]*v1.IndexEntry, err error) {
	return d.header.Entries, nil
}

func (d *Downloader) GetEntry(_ context.Context, actionID string) (metadata *v1.IndexEntry, ok bool, err error) {
	metadata, ok = d.header.Entries[actionID]
	return metadata, ok, nil
}

func (d *Downloader) GetOutputs(context.Context) (outputs []*v1.ActionsOutput, err error) {
	return d.header.Outputs, nil
}

func (d *Downloader) GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error) {
	url = d.client.GetURL(ctx)
	offset = d.headerSize
	size = d.header.OutputTotalSize

	return url, offset, size, nil
}

const maxChunkSize = 4 * (1 << 20)

func (d *Downloader) DownloadAllOutputBlocks(ctx context.Context, outputsWithOpenerMap map[string]local.OpenerWithUnlock) error {
	d.logger.Debugf("downloading all output blocks")
	defer d.logger.Debugf("downloaded all output blocks")

	outputs := d.header.Outputs
	slices.SortFunc(outputs, func(x, y *v1.ActionsOutput) int {
		return int(x.Offset - y.Offset)
	})

	d.logger.Debugf("outputs: %d", len(outputs))

	eg := errgroup.Group{}

	offset := d.headerSize
	for i := 0; i < len(outputs); {
		d.logger.Debugf("creating chunk: %d", i)
		chunkOffset := offset
		chunkSize := int64(0)
		chunkWriters := []myio.WriterWithSize{}
		chunkCloseFuncs := []func() error{}
		for ; i < len(outputs) && chunkSize < maxChunkSize; i++ {
			output := outputs[i]
			offset += output.Size
			chunkSize += output.Size

			d.logger.Debugf("creating object writer(%d): outputID=%s", i, output.Id)

			opener, ok := outputsWithOpenerMap[output.Id]
			if !ok {
				return fmt.Errorf("object writer not found: outputID=%s", output.Id)
			}

			d.logger.Debugf("opening object writer(%d): outputID=%s", i, output.Id)
			fw, err := opener.Open()
			if err != nil {
				return fmt.Errorf("open object writer: %w", err)
			}
			d.logger.Debugf("opened object writer(%d): outputID=%s", i, output.Id)
			var w io.Writer = fw
			closeFunc := sync.OnceValue(fw.Close)

			switch output.Compression {
			case v1.Compression_COMPRESSION_ZSTD:
				d.logger.Debugf("creating decompress writer(%d): outputID=%s", i, output.Id)
				zw := zstd.NewDecompressWriter(fw)
				w = zw
				closeFunc = sync.OnceValue(func() error {
					if err := zw.Close(); err != nil { // decompress writer
						d.logger.Debugf("close decompress writer: %v", err)
					}

					if err := fw.Close(); err != nil {
						d.logger.Debugf("close file writer: %v", err)
					}

					return nil
				})
			case v1.Compression_COMPRESSION_UNSPECIFIED:
				fallthrough
			default:
				d.logger.Debugf("creating raw writer(%d): outputID=%s", i, output.Id)
			}

			chunkWriters = append(chunkWriters, myio.WriterWithSize{
				Writer: w,
				Size:   output.Size,
				Close:  closeFunc,
			})
			chunkCloseFuncs = append(chunkCloseFuncs, closeFunc)
		}

		j := i
		eg.Go(func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					d.logger.Warnf("panic in download all output blocks: %v", r)
					err = errors.Join(err, fmt.Errorf("panic in download all output blocks: %v", r))
					return
				}
			}()

			// io.WriteCloser is expected to be already Closed in JoindWriter.
			// However, in order to avoid deadlock in the event that an error occurs during the process and Close is not performed, Close is performed by defer without fail.
			for _, closeFunc := range chunkCloseFuncs {
				defer func() {
					if err := closeFunc(); err != nil {
						d.logger.Debugf("close object writer: %v", err)
					}
				}()
			}

			jw := myio.NewJoinedWriter(chunkWriters...)

			d.logger.Debugf("downloading chunk: %d/%d", j, len(outputs))
			err = d.client.DownloadBlock(ctx, chunkOffset, chunkSize, jw)
			if err != nil {
				err = fmt.Errorf("download block: %w", err)
				return err
			}

			d.logger.Debugf("downloaded chunk: %d/%d", j, len(outputs))

			return nil
		})
	}

	d.logger.Debugf("waiting for all chunks")

	if err := eg.Wait(); err != nil {
		return err
	}

	d.logger.Debugf("all chunks downloaded")

	return nil
}
