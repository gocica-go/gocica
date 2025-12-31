package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/log"
)

var _ remote.Backend = &Backend{}

// Backend implements remote.Backend.
// It uses Uploader/Downloader for data transfer.
type Backend struct {
	logger     log.Logger
	uploader   *Uploader
	downloader *Downloader
	cancel     context.CancelCauseFunc
}

// NewBackend creates a new RemoteBackend with the given uploader and downloader.
func NewBackend(
	logger log.Logger,
	localBackend local.Backend,
	uploader *Uploader,
	downloader *Downloader,
) (*Backend, error) {
	c := &Backend{
		logger:     logger,
		uploader:   uploader,
		downloader: downloader,
	}

	if !c.downloader.IsEmpty() {
		ctx := context.Background()
		ctx, c.cancel = context.WithCancelCause(ctx)

		// Download all output blocks in the background.
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Errorf("panic in downloading output blocks: %v", r)
				}
			}()

			if err := c.downloader.DownloadAllOutputBlocks(ctx, func(ctx context.Context, objectID string) (io.WriteCloser, error) {
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

func (c *Backend) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	entries, err := c.downloader.GetEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entries: %w", err)
	}

	return entries, nil
}

func (c *Backend) WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	if err := c.uploader.Commit(ctx, metaDataMap); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func (c *Backend) Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error {
	if err := c.uploader.UploadOutput(ctx, objectID, size, myio.NopSeekCloser(r)); err != nil {
		return fmt.Errorf("upload output: %w", err)
	}

	return nil
}

func (c *Backend) Close(context.Context) error {
	c.cancel(errors.New("backend closed"))

	return nil
}
