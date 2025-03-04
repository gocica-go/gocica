package blob

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"google.golang.org/protobuf/proto"
)

type Downloader struct {
	client     DownloadClient
	headerSize int64
	header     *v1.ActionsCache
}

type DownloadClient interface {
	GetURL() string
	DownloadBlock(offset int64, size int64, w io.Writer) error
	DownloadBlockBuffer(offset int64, size int64, buf []byte) error
}

func NewDownloader(client DownloadClient) (*Downloader, error) {
	downloader := &Downloader{
		client: client,
	}

	var err error
	downloader.header, downloader.headerSize, err = downloader.readHeader()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	return downloader, nil
}

func (d *Downloader) readHeader() (header *v1.ActionsCache, headerSize int64, err error) {
	sizeBuf := make([]byte, 8)
	err = d.client.DownloadBlockBuffer(0, 8, sizeBuf)
	if err != nil {
		return nil, 0, fmt.Errorf("download size buffer: %w", err)
	}
	//nolint:gosec
	protobufSize := int64(binary.BigEndian.Uint64(sizeBuf))

	protoBuf := make([]byte, protobufSize)
	err = d.client.DownloadBlockBuffer(8, protobufSize, protoBuf)
	if err != nil {
		return nil, 0, fmt.Errorf("download header buffer: %w", err)
	}

	header = &v1.ActionsCache{}
	if err = proto.Unmarshal(protoBuf, header); err != nil {
		return nil, 0, fmt.Errorf("unmarshal header: %w", err)
	}

	return header, 8 + int64(len(protoBuf)), nil
}

func (d *Downloader) GetEntries() (metadata map[string]*v1.IndexEntry, err error) {
	return d.header.Entries, nil
}

func (d *Downloader) GetOutputs() (outputs map[string]*v1.ActionsOutput, err error) {
	return d.header.Outputs, nil
}

func (d *Downloader) GetOutputBlockURL() (url string, offset, size int64, err error) {
	url = d.client.GetURL()
	offset = d.headerSize
	size = d.header.OutputTotalSize

	return url, offset, size, nil
}

var ErrOutputNotFound = errors.New("output not found")

func (d *Downloader) DownloadOutputBlock(blobID string, w io.Writer) error {
	output, ok := d.header.Outputs[blobID]
	if !ok {
		return ErrOutputNotFound
	}

	if output.Size <= 0 {
		return fmt.Errorf("invalid output size: %d", output.Size)
	}

	offset := d.headerSize + output.Offset
	if err := d.client.DownloadBlock(offset, output.Size, w); err != nil {
		return fmt.Errorf("download block: %w", err)
	}

	return nil
}
