package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/DataDog/zstd"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"
)

type Downloader struct {
	logger log.Logger
	// warning: client can be nil, which means no download is needed.
	client     DownloadClient
	headerSize int64
	header     *v1.ActionsCache

	outputIndexOnce sync.Once
	outputIndex     map[string]*v1.ActionsOutput
}

// DefaultSpeculativeHeaderSize is how much of the blob's head is fetched in one
// request when reading the header. The header is an 8-byte length followed by
// the index, whose size is only known after the first request; reading them
// separately costs a round trip on every handshake, and the module proxy makes
// two of those before any bytes of the cache move. Measured against
// tailscale/tailscale the index is well under this, so the second request is
// gone; a larger index still works, it just pays the round trip as before.
const DefaultSpeculativeHeaderSize = 4 << 20

// DownloaderOptions tunes a Downloader.
type DownloaderOptions struct {
	// SpeculativeHeaderSize overrides DefaultSpeculativeHeaderSize. Anything
	// below the 8-byte length prefix reads only that, as before.
	SpeculativeHeaderSize int64
}

// DownloadClient defines the interface for downloading blocks from remote storage.
type DownloadClient interface {
	GetURL(ctx context.Context) (string, error)
	DownloadBlock(ctx context.Context, offset int64, size int64, w io.Writer) error
	DownloadBlockBuffer(ctx context.Context, offset int64, size int64, buf []byte) error
}

// NewDownloader creates a new Downloader with the given client.
// It reads the header from the remote storage immediately.
func NewDownloader(
	ctx context.Context,
	logger log.Logger,
	client DownloadClient,
) (*Downloader, error) {
	return NewDownloaderWithOptions(ctx, logger, client, DownloaderOptions{})
}

// NewDownloaderWithOptions is NewDownloader with tuning.
func NewDownloaderWithOptions(
	ctx context.Context,
	logger log.Logger,
	client DownloadClient,
	options DownloaderOptions,
) (*Downloader, error) {
	downloader := &Downloader{
		logger: logger,
		client: client,
	}

	speculate := options.SpeculativeHeaderSize
	if speculate == 0 {
		speculate = DefaultSpeculativeHeaderSize
	}

	var err error
	downloader.header, downloader.headerSize, err = downloader.readHeader(ctx, speculate)
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	return downloader, nil
}

// headerPrefixSize is the length prefix in front of the serialized index.
const headerPrefixSize = 8

// readHeader fetches the length prefix and the index behind it. The first
// request asks for speculate bytes: when the index fits, that is the only
// request; otherwise the rest is fetched with a second one, as it always was.
func (d *Downloader) readHeader(ctx context.Context, speculate int64) (header *v1.ActionsCache, headerSize int64, err error) {
	if d.client == nil {
		return &v1.ActionsCache{
			Entries:         map[string]*v1.IndexEntry{},
			Outputs:         nil,
			OutputTotalSize: 0,
		}, 0, nil
	}

	speculate = max(speculate, headerPrefixSize)

	// A range past the end of the blob is answered with what exists, so this is
	// safe on a blob smaller than the speculation.
	head := &bytes.Buffer{}
	head.Grow(int(speculate))
	if err := d.client.DownloadBlock(ctx, 0, speculate, head); err != nil {
		return nil, 0, fmt.Errorf("download header: %w", err)
	}
	if head.Len() < headerPrefixSize {
		return nil, 0, fmt.Errorf("blob is %d bytes, shorter than the header length prefix", head.Len())
	}
	// An empty index marshals to nothing, so zero is a valid size.
	//nolint:gosec
	protobufSize := int64(binary.BigEndian.Uint64(head.Bytes()[:headerPrefixSize]))

	got := head.Bytes()[headerPrefixSize:]
	if int64(len(got)) > protobufSize {
		got = got[:protobufSize]
	}
	protoBuf := make([]byte, protobufSize)
	copy(protoBuf, got)
	if rest := protobufSize - int64(len(got)); rest > 0 {
		offset := headerPrefixSize + int64(len(got))
		if err := d.client.DownloadBlockBuffer(ctx, offset, rest, protoBuf[len(got):]); err != nil {
			return nil, 0, fmt.Errorf("download header buffer: %w", err)
		}
	}
	d.logger.Debugf("blob header is %d bytes; speculated %d, second request needed: %t", protobufSize, speculate, int64(len(got)) < protobufSize)

	header = &v1.ActionsCache{}
	if err = proto.Unmarshal(protoBuf, header); err != nil {
		return nil, 0, fmt.Errorf("unmarshal header: %w", err)
	}

	return header, headerPrefixSize + protobufSize, nil
}

func (d *Downloader) GetEntries(context.Context) (metadata map[string]*v1.IndexEntry, err error) {
	return d.header.Entries, nil
}

func (d *Downloader) GetOutputs(context.Context) (outputs []*v1.ActionsOutput, err error) {
	return d.header.Outputs, nil
}

func (d *Downloader) IsEmpty() bool {
	return d.header.OutputTotalSize == 0
}

func (d *Downloader) GetOutputBlockURL(ctx context.Context) (url string, offset, size int64, err error) {
	if d.client == nil {
		return "", 0, 0, errors.New("no download client")
	}

	url, err = d.client.GetURL(ctx)
	if err != nil {
		return "", 0, 0, fmt.Errorf("get url: %w", err)
	}
	offset = d.headerSize
	size = d.header.OutputTotalSize

	return url, offset, size, nil
}

const maxChunkSize = 4 * (1 << 20)

// openFileLimit is the maximum number of files that can be opened at the same time.
// ref: https://github.com/golang/go/issues/46279
const openFileLimit = 100000

// DownloadAllOutputBlocks writes every output of the blob through
// objectWriterFunc, coalescing neighbouring outputs into chunked reads.
//
// skip, when non-nil, reports outputs that are already available locally. Their
// bytes are stepped over rather than transferred, which is what keeps the proxy
// daemon's prewarm from being undone by the next process downloading the same
// blob again. A skipped output ends the chunk it falls in, since a chunk is one
// contiguous read.
func (d *Downloader) DownloadAllOutputBlocks(
	ctx context.Context,
	objectWriterFunc func(ctx context.Context, objectID string) (io.WriteCloser, error),
	skip func(objectID string) bool,
) error {
	if d.client == nil {
		return nil
	}

	if skip == nil {
		skip = func(string) bool { return false }
	}

	outputs := d.header.Outputs
	slices.SortFunc(outputs, func(x, y *v1.ActionsOutput) int {
		return int(x.Offset - y.Offset)
	})

	eg := errgroup.Group{}

	s := semaphore.NewWeighted(openFileLimit)
	offset := d.headerSize
	for i := 0; i < len(outputs); {
		for i < len(outputs) && skip(outputs[i].Id) {
			offset += outputs[i].Size
			i++
		}
		if i >= len(outputs) {
			break
		}

		d.logger.Debugf("creating chunk: %d", i)
		chunkOffset := offset
		chunkSize := int64(0)
		chunkWriters := []myio.WriterWithSize{}
		chunkCloseFuncs := []func() error{}
		for ; i < len(outputs) && chunkSize < maxChunkSize && !skip(outputs[i].Id); i++ {
			output := outputs[i]
			offset += output.Size
			chunkSize += output.Size

			d.logger.Debugf("acquiring semaphore(%d): outputID=%s", i, output.Id)

			err := s.Acquire(ctx, 1)
			if err != nil {
				return fmt.Errorf("acquire semaphore: %w", err)
			}

			d.logger.Debugf("creating object writer(%d): outputID=%s", i, output.Id)

			w, err := objectWriterFunc(ctx, outputs[i].Id)
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
			defer s.Release(int64(len(chunkWriters)))
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

// Output returns the blob entry for an object ID.
//
// The lookup index is built on first use: the bulk download path never needs it,
// and the module proxy asks once per module, which is often enough that a linear
// scan over every output in the blob would show up.
func (d *Downloader) Output(id string) (*v1.ActionsOutput, bool) {
	d.outputIndexOnce.Do(func() {
		d.outputIndex = make(map[string]*v1.ActionsOutput, len(d.header.Outputs))
		for _, output := range d.header.Outputs {
			d.outputIndex[output.Id] = output
		}
	})

	output, ok := d.outputIndex[id]

	return output, ok
}

// DownloadOutput fetches a single output by range and writes its decompressed
// bytes to w.
//
// This is the counterpart of DownloadAllOutputBlocks for callers that only need a
// few objects out of a large blob. The build cache wants everything, so it uses
// the bulk path; the module proxy only needs the modules this build actually
// imports, which is a fraction of the module graph.
func (d *Downloader) DownloadOutput(ctx context.Context, output *v1.ActionsOutput, w io.Writer) error {
	if d.client == nil {
		return errors.New("no download client")
	}

	if output.Size == 0 {
		return nil
	}

	var closeFunc func() error
	if output.Compression == v1.Compression_COMPRESSION_ZSTD {
		dw := zstd.NewDecompressWriter(w)
		w, closeFunc = dw, dw.Close
	}

	if err := d.client.DownloadBlock(ctx, d.headerSize+output.Offset, output.Size, w); err != nil {
		if closeFunc != nil {
			_ = closeFunc()
		}

		return fmt.Errorf("download block: %w", err)
	}

	if closeFunc != nil {
		if err := closeFunc(); err != nil {
			return fmt.Errorf("close decompress writer: %w", err)
		}
	}

	return nil
}
