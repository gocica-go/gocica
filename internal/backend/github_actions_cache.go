package backend

import (
	"context"
	"fmt"
	"io"

	"github.com/mazrean/gocica/internal/backend/blob"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
)

var _ RemoteBackend = &GitHubActionsCache{}

// GitHubActionsCache implements RemoteBackend using GitHub Actions Cache API.
// It uses GitHubCacheClient for API calls and blob.Uploader/Downloader for data transfer.
type GitHubActionsCache struct {
	logger      log.Logger
	cacheClient *blob.GitHubCacheClient
	uploader    *blob.Uploader
	downloader  *blob.Downloader
}

// NewGitHubActionsCache creates a new GitHub Actions Cache backend with pre-created dependencies.
// This is a DI-friendly constructor that accepts cacheClient, uploader and downloader as parameters.
// If uploader or downloader is nil, operations requiring them will be no-ops.
func NewGitHubActionsCache(
	_ context.Context,
	logger log.Logger,
	cacheClient *blob.GitHubCacheClient,
	localBackend LocalBackend,
	uploader *blob.Uploader,
	downloader *blob.Downloader,
) (*GitHubActionsCache, error) {
	c := &GitHubActionsCache{
		logger:      logger,
		cacheClient: cacheClient,
		uploader:    uploader,
		downloader:  downloader,
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

func (c *GitHubActionsCache) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	if c.downloader == nil {
		return map[string]*v1.IndexEntry{}, nil
	}

	entries, err := c.downloader.GetEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entries: %w", err)
	}

	return entries, nil
}

func (c *GitHubActionsCache) WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	if c.uploader == nil {
		return nil
	}

	size, err := c.uploader.Commit(ctx, metaDataMap)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if err := c.cacheClient.CommitCacheEntry(ctx, size); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error {
	if c.uploader == nil {
		return nil
	}

	if err := c.uploader.UploadOutput(ctx, objectID, size, myio.NopSeekCloser(r)); err != nil {
		return fmt.Errorf("upload output: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) Close(context.Context) error {
	return nil
}
