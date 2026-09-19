package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/internal/remote/storage"
	"github.com/mazrean/gocica/log"
)

// sasRefreshInterval is how long a signed storage URL is reused before a fresh one
// is requested. The signed URLs GitHub hands out do expire, and a long-lived
// process (the module proxy daemon runs for a whole job) can outlive them. An
// expired URL on the base-copy path silently drops the entire previous blob, so
// this is deliberately far shorter than any plausible expiry.
const sasRefreshInterval = 20 * time.Minute

var _ core.UploadClient = (*lazyUploadClient)(nil)

// lazyUploadClient defers CreateCacheEntry until something is actually uploaded.
// Reserving the entry up front means a run that uploads nothing still burns the
// key, and a run that dies before finalizing locks that key out of every retry.
type lazyUploadClient struct {
	logger      log.Logger
	cacheClient *ghaCacheClient

	once     sync.Once
	client   core.UploadClient
	disabled bool
}

func (c *lazyUploadClient) resolve(ctx context.Context) (core.UploadClient, error) {
	c.once.Do(func() {
		uploadURL, err := c.cacheClient.createCacheEntry(ctx)
		switch {
		case errors.Is(err, ErrAlreadyExists):
			key, _ := c.cacheClient.blobKey()
			c.logger.Infof("cache entry %q already exists. skipping upload.", key)
			c.disabled = true

			return
		case err != nil:
			// Degraded mode: a remote that will not take our writes must not fail
			// the build.
			c.logger.Warnf("create cache entry: %v. continuing without upload.", err)
			c.disabled = true

			return
		}

		uploadClient, err := storage.NewAzureUploadClient(uploadURL)
		if err != nil {
			c.logger.Warnf("create azure upload client: %v. continuing without upload.", err)
			c.disabled = true

			return
		}

		c.client = uploadClient
	})

	if c.disabled {
		return nil, core.ErrUploadDisabled
	}

	return c.client, nil
}

func (c *lazyUploadClient) UploadBlock(ctx context.Context, blockID string, r io.ReadSeekCloser) (int64, error) {
	client, err := c.resolve(ctx)
	if err != nil {
		return 0, err
	}

	return client.UploadBlock(ctx, blockID, r)
}

func (c *lazyUploadClient) UploadBlockFromURL(ctx context.Context, blockID string, url string, offset, size int64) error {
	client, err := c.resolve(ctx)
	if err != nil {
		return err
	}

	return client.UploadBlockFromURL(ctx, blockID, url, offset, size)
}

func (c *lazyUploadClient) Commit(ctx context.Context, blockIDs []string, size int64) error {
	client, err := c.resolve(ctx)
	if err != nil {
		return err
	}

	if err := client.Commit(ctx, blockIDs, size); err != nil {
		return fmt.Errorf("commit upload client: %w", err)
	}

	if err := c.cacheClient.commitCacheEntry(ctx, size); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}

	return nil
}

var _ core.DownloadClient = (*refreshingDownloadClient)(nil)

// refreshingDownloadClient re-requests the signed download URL once it is older
// than sasRefreshInterval. See the comment on that constant.
type refreshingDownloadClient struct {
	logger      log.Logger
	cacheClient *ghaCacheClient

	locker    sync.Mutex
	client    core.DownloadClient
	fetchedAt time.Time
}

func newRefreshingDownloadClient(logger log.Logger, cacheClient *ghaCacheClient, downloadURL string) (*refreshingDownloadClient, error) {
	client, err := storage.NewAzureDownloadClient(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("create azure download client: %w", err)
	}

	return &refreshingDownloadClient{
		logger:      logger,
		cacheClient: cacheClient,
		client:      client,
		fetchedAt:   time.Now(),
	}, nil
}

func (c *refreshingDownloadClient) current(ctx context.Context) core.DownloadClient {
	c.locker.Lock()
	defer c.locker.Unlock()

	if time.Since(c.fetchedAt) < sasRefreshInterval {
		return c.client
	}

	downloadURL, err := c.cacheClient.getDownloadURL(ctx)
	if err != nil {
		// Keep using the old URL: it may still be valid, and there is nothing
		// better to fall back to.
		c.logger.Warnf("refresh download url: %v. reusing the previous one.", err)
		c.fetchedAt = time.Now()

		return c.client
	}

	client, err := storage.NewAzureDownloadClient(downloadURL)
	if err != nil {
		c.logger.Warnf("recreate azure download client: %v. reusing the previous one.", err)
		c.fetchedAt = time.Now()

		return c.client
	}

	c.client = client
	c.fetchedAt = time.Now()

	return c.client
}

func (c *refreshingDownloadClient) GetURL(ctx context.Context) (string, error) {
	return c.current(ctx).GetURL(ctx)
}

func (c *refreshingDownloadClient) DownloadBlock(ctx context.Context, offset int64, size int64, w io.Writer) error {
	return c.current(ctx).DownloadBlock(ctx, offset, size, w)
}

func (c *refreshingDownloadClient) DownloadBlockBuffer(ctx context.Context, offset int64, size int64, buf []byte) error {
	return c.current(ctx).DownloadBlockBuffer(ctx, offset, size, buf)
}
