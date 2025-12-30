package remote

import (
	"context"
	"fmt"
	"io"

	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
)

type Backend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
	Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error
	Close(ctx context.Context) error
}

var _ Backend = &BackendImpl{}

// BackendImpl implements RemoteBackend.
// It uses Uploader/Downloader for data transfer.
type BackendImpl struct {
	logger     log.Logger
	uploader   *Uploader
	downloader *Downloader
}

// NewBackend creates a new RemoteBackend with the given uploader and downloader.
func NewBackend(
	logger log.Logger,
	localBackend local.Backend,
	uploader *Uploader,
	downloader *Downloader,
) (*BackendImpl, error) {
	c := &BackendImpl{
		logger:     logger,
		uploader:   uploader,
		downloader: downloader,
	}

	if c.downloader != nil {
		// Download all output blocks in the background.
		// Use context.Background() because this should continue even if the parent context is cancelled.
		go func() {
			if err := c.downloader.DownloadAllOutputBlocks(context.Background(), func(ctx context.Context, objectID string) (io.WriteCloser, error) {
				_, w, err := localBackend.Put(ctx, objectID, 0)
				return w, err
			}); err != nil {
				logger.Errorf("download all output blocks: %v", err)
			}
		}()
	}

	logger.Infof("GitHub Actions cache backend initialized.")

	return c, nil
}

func (c *BackendImpl) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	if c.downloader == nil {
		return map[string]*v1.IndexEntry{}, nil
	}

	entries, err := c.downloader.GetEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entries: %w", err)
	}

	return entries, nil
}

func (c *BackendImpl) WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	if c.uploader == nil {
		return nil
	}

	if err := c.uploader.Commit(ctx, metaDataMap); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func (c *BackendImpl) Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error {
	if c.uploader == nil {
		return nil
	}

	if err := c.uploader.UploadOutput(ctx, objectID, size, myio.NopSeekCloser(r)); err != nil {
		return fmt.Errorf("upload output: %w", err)
	}

	return nil
}

func (c *BackendImpl) Close(context.Context) error {
	return nil
}
