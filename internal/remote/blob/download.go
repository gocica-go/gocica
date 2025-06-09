package blob

//go:generate go tool mockgen -source=$GOFILE -destination=mock/${GOFILE} -package=mock

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"slices"

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

	outputIDs := make([]string, 0, len(downloader.header.Outputs))
	outputIDMap := make(map[string]struct{}, len(downloader.header.Outputs))
	for _, output := range downloader.header.Outputs {
		if _, ok := outputIDMap[output.Id]; ok {
			continue
		}
		outputIDMap[output.Id] = struct{}{}

		outputIDs = append(outputIDs, output.Id)
	}

	if err := localBackend.Lock(ctx, outputIDs...); err != nil {
		return nil, fmt.Errorf("lock local cache: %w", err)
	}

	// Download all output blocks in the background.
	go func() {
		if err := downloader.DownloadAllOutputBlocks(context.Background(), localBackend); err != nil {
			logger.Errorf("download all output blocks: %v", err)
		}
	}()

	return downloader, nil
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

func (d *Downloader) GetEntry(_ context.Context, actionID string) (metadata *v1.IndexEntry, err error) {
	return d.header.Entries[actionID], nil
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

func (d *Downloader) DownloadAllOutputBlocks(ctx context.Context, localBackend local.Backend) error {
	outputs := d.header.Outputs
	slices.SortFunc(outputs, func(x, y *v1.ActionsOutput) int {
		return int(x.Offset - y.Offset)
	})

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

			_, w, err := localBackend.Put(ctx, outputs[i].Id, outputs[i].Size)
			if err != nil {
				return fmt.Errorf("get object writer: %w", err)
			}
			chunkCloseFuncs = append(chunkCloseFuncs, w.Close)

			switch output.Compression {
			case v1.Compression_COMPRESSION_ZSTD:
				d.logger.Debugf("creating decompress writer(%d): outputID=%s", i, output.Id)
				w = zstd.NewDecompressWriter(w)
				chunkCloseFuncs = append(chunkCloseFuncs, w.Close)
			case v1.Compression_COMPRESSION_UNSPECIFIED:
				fallthrough
			default:
				d.logger.Debugf("creating raw writer(%d): outputID=%s", i, output.Id)
			}

			chunkWriters = append(chunkWriters, myio.WriterWithSize{
				Writer: w,
				Size:   outputs[i].Size,
			})
		}

		slices.Reverse(chunkCloseFuncs)
		j := i
		eg.Go(func() error {
			defer func() {
				// io.WriteCloser is expected to be already Closed in JoindWriter.
				// However, in order to avoid deadlock in the event that an error occurs during the process and Close is not performed, Close is performed by defer without fail.
				for _, closeFunc := range chunkCloseFuncs {
					if err := closeFunc(); err != nil {
						d.logger.Debugf("close object writer: %v", err)
					}
				}
			}()

			jw := myio.NewJoinedWriter(chunkWriters...)

			d.logger.Debugf("downloading chunk: %d/%d", j, len(outputs))
			if err := d.client.DownloadBlock(ctx, chunkOffset, chunkSize, jw); err != nil {
				return fmt.Errorf("download block: %w", err)
			}

			d.logger.Debugf("downloaded chunk: %d/%d", j, len(outputs))

			return nil
		})
	}

	d.logger.Debugf("waiting for all chunks")

	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}
