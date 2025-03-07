package blob

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	lz4 "github.com/DataDog/golz4"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type Downloader struct {
	client     DownloadClient
	headerSize int64
	header     *v1.ActionsCache
}

type DownloadClient interface {
	GetURL(ctx context.Context) string
	DownloadBlock(ctx context.Context, offset int64, size int64, w io.Writer) error
	DownloadBlockBuffer(ctx context.Context, offset int64, size int64, buf []byte) error
}

func NewDownloader(ctx context.Context, client DownloadClient) (*Downloader, error) {
	downloader := &Downloader{
		client: client,
	}

	var err error
	downloader.header, downloader.headerSize, err = downloader.readHeader(ctx)
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

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

func (d *Downloader) GetEntries(context.Context) (metadata map[string]*v1.IndexEntry, err error) {
	return d.header.Entries, nil
}

func (d *Downloader) GetOutputs(context.Context) (outputs map[string]*v1.ActionsOutput, err error) {
	return d.header.Outputs, nil
}

func (d *Downloader) GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error) {
	url = d.client.GetURL(ctx)
	offset = d.headerSize
	size = d.header.OutputTotalSize

	return url, offset, size, nil
}

var ErrOutputNotFound = errors.New("output not found")

func (d *Downloader) DownloadOutputBlock(ctx context.Context, blobID string, w io.Writer) error {
	output, ok := d.header.Outputs[blobID]
	if !ok {
		return ErrOutputNotFound
	}

	if output.Size <= 0 {
		return fmt.Errorf("invalid output size: %d", output.Size)
	}

	offset := d.headerSize + output.Offset
	switch output.Compression {
	case v1.Compression_COMPRESSION_LZ4:
		pr, pw := io.Pipe()
		defer pr.Close()

		eg := errgroup.Group{}
		eg.Go(func() error {
			defer pw.Close()
			if err := d.client.DownloadBlock(ctx, offset, output.Size, pw); err != nil {
				return fmt.Errorf("download block: %w", err)
			}
			return nil
		})

		dr := lz4.NewDecompressReader(pr)
		defer dr.Close()

		if _, err := io.Copy(w, dr); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("copy decompressed data: %w", err)
		}

		if err := eg.Wait(); err != nil {
			return err
		}
	case v1.Compression_COMPRESSION_UNSPECIFIED:
		if err := d.client.DownloadBlock(ctx, offset, output.Size, w); err != nil {
			return fmt.Errorf("download block: %w", err)
		}
	default:
		return fmt.Errorf("unsupported compression: %v", output.Compression)
	}

	return nil
}
