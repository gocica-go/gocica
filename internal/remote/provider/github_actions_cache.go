package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/json"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/internal/remote/storage"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

type GHACacheConfig struct {
	Token    string
	CacheURL string
	RunnerOS string
	Ref      string
	Sha      string
}

var _ remote.Backend = &GitHubActionsCache{}

// GitHubActionsCache implements RemoteBackend using GitHub Actions Cache API.
// It uses GitHubCacheClient for API calls and blob.Uploader/Downloader for data transfer.
type GitHubActionsCache struct {
	logger      log.Logger
	cacheClient *GHACacheClient
	uploader    *remote.Uploader
	downloader  *remote.Downloader
}

// NewGitHubActionsCache creates a new GitHub Actions Cache backend with pre-created dependencies.
// This is a DI-friendly constructor that accepts cacheClient, uploader and downloader as parameters.
// If uploader or downloader is nil, operations requiring them will be no-ops.
func NewGitHubActionsCache(
	ctx context.Context,
	logger log.Logger,
	config *GHACacheConfig,
	localBackend local.Backend,
) (*GitHubActionsCache, error) {
	cacheClient, err := newGitHubCacheClient(
		context.Background(),
		logger,
		config.Token,
		config.CacheURL,
		config.RunnerOS,
		config.Ref,
		config.Sha,
	)
	if err != nil {
		return nil, fmt.Errorf("create github cache client: %w", err)
	}

	eg, ctx := errgroup.WithContext(ctx)

	var downloader *remote.Downloader
	eg.Go(func() error {
		downloadURL, err := cacheClient.getDownloadURL(ctx)
		if err != nil {
			if !errors.Is(err, ErrCacheNotFound) {
				return fmt.Errorf("get download url: %w", err)
			}
			logger.Infof("no existing cache found, proceeding without downloader")
		}

		storageDownloadClient, err := storage.NewAzureDownloadClient(downloadURL)
		if err != nil {
			return fmt.Errorf("create azure download client: %w", err)
		}

		downloader, err = remote.NewDownloader(ctx, logger, storageDownloadClient)
		if err != nil {
			return fmt.Errorf("create downloader: %w", err)
		}

		return nil
	})

	uploadURL, err := cacheClient.createCacheEntry(ctx)
	if err != nil {
		return nil, fmt.Errorf("create cache entry: %w", err)
	}
	storageUploadClient, err := storage.NewAzureUploadClient(uploadURL)
	if err != nil {
		return nil, fmt.Errorf("create azure upload client: %w", err)
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	uploader := remote.NewUploader(ctx, logger, storageUploadClient, downloader)

	c := &GitHubActionsCache{
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

	if err := c.cacheClient.commitCacheEntry(ctx, size); err != nil {
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

const (
	actionsCacheBasePath  = "/twirp/github.actions.results.api.v1.CacheService/"
	actionsCachePrefix    = "gocica-cache"
	actionsCacheSeparator = "-"
)

// actionsCacheVersion is sha256 of the context.
// upstream uses paths in actionsCacheVersion, we don't seem to have anything that is unique like this.
// so we use the sha256 of "gocica-cache-1.0" as a actionsCacheVersion.
var actionsCacheVersion = "5eb02eebd0c9b2a428c370e552c7c895ea26154c726235db0a053f746fae0287"

var (
	ErrCacheNotFound = errors.New("cache not found")
	ErrAlreadyExists = errors.New("cache already exists")
)

var githubAPILatencyGauge = metrics.NewGauge("github_cache_api_latency")

// GHACacheClient handles GitHub Actions Cache API calls.
// This is a standalone client that doesn't depend on GitHubActionsCache.
type GHACacheClient struct {
	logger     log.Logger
	httpClient *http.Client
	baseURL    *url.URL
	runnerOS   string
	ref        string
	sha        string
}

// newGitHubCacheClient creates a new GitHub Cache API client.
func newGitHubCacheClient(
	ctx context.Context,
	logger log.Logger,
	token string,
	strBaseURL string,
	runnerOS string,
	ref, sha string,
) (*GHACacheClient, error) {
	baseURL, err := url.Parse(string(strBaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	baseURL = baseURL.JoinPath(actionsCacheBasePath)

	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: string(token),
	}))

	return &GHACacheClient{
		logger:     logger,
		httpClient: httpClient,
		baseURL:    baseURL,
		runnerOS:   string(runnerOS),
		ref:        string(ref),
		sha:        string(sha),
	}, nil
}

// blobKey returns the cache key and restore keys for this configuration.
func (c *GHACacheClient) blobKey() (string, []string) {
	baseKey := actionsCachePrefix + actionsCacheSeparator + c.runnerOS
	restoreKeys := make([]string, 0, 2)
	for _, k := range []string{c.ref, c.sha} {
		baseKey += actionsCacheSeparator
		restoreKeys = append(restoreKeys, baseKey)
		baseKey += k
	}
	slices.Reverse(restoreKeys)

	return baseKey, restoreKeys
}

func (c *GHACacheClient) doRequest(ctx context.Context, endpoint string, reqBody any, respBody any) error {
	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(reqBody)
	if err != nil {
		return fmt.Errorf("encode request body: %w", err)
	}

	c.logger.Debugf("do request: endpoint=%s, body=%s", endpoint, buf.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.JoinPath(endpoint).String(), buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var res *http.Response
	githubAPILatencyGauge.Stopwatch(func() {
		res, err = c.httpClient.Do(req)
	}, endpoint)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		sb := &strings.Builder{}
		_, err := io.Copy(sb, res.Body)
		if err != nil {
			return fmt.Errorf("copy response body: %w", err)
		}

		switch res.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrCacheNotFound, sb.String())
		case http.StatusConflict:
			return fmt.Errorf("%w: %s", ErrAlreadyExists, sb.String())
		default:
			return fmt.Errorf("unexpected status code: %d, body: %s", res.StatusCode, sb.String())
		}
	}

	if err := json.NewDecoder(res.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

// GetDownloadURL fetches the signed download URL from GitHub Actions Cache API.
func (c *GHACacheClient) getDownloadURL(ctx context.Context) (string, error) {
	key, restoreKeys := c.blobKey()
	c.logger.Debugf("get download url: key=%s, restoreKeys=%v", key, restoreKeys)

	var res struct {
		OK                bool   `json:"ok"`
		SignedDownloadURL string `json:"signed_download_url"`
		MatchedKey        string `json:"matched_key"`
	}
	err := c.doRequest(ctx, "GetCacheEntryDownloadURL", &struct {
		Key         string   `json:"key"`
		RestoreKeys []string `json:"restore_keys"`
		Version     string   `json:"version"`
	}{key, restoreKeys, actionsCacheVersion}, &res)
	if err != nil {
		return "", fmt.Errorf("get cache entry download url: %w", err)
	}

	if !res.OK {
		return "", errors.New("failed to get download url")
	}

	c.logger.Debugf("signed download url: %s", res.SignedDownloadURL)

	return res.SignedDownloadURL, nil
}

// createCacheEntry creates a new cache entry and returns the signed upload URL.
func (c *GHACacheClient) createCacheEntry(ctx context.Context) (string, error) {
	key, _ := c.blobKey()
	c.logger.Debugf("create cache entry: key=%s", key)

	var res struct {
		OK              bool   `json:"ok"`
		SignedUploadURL string `json:"signed_upload_url"`
	}
	err := c.doRequest(ctx, "CreateCacheEntry", &struct {
		Key     string `json:"key"`
		Version string `json:"version"`
	}{key, actionsCacheVersion}, &res)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}

	if !res.OK {
		return "", errors.New("failed to create cache")
	}

	c.logger.Debugf("signed upload url: %s", res.SignedUploadURL)

	return res.SignedUploadURL, nil
}

// CommitCacheEntry finalizes the cache entry upload.
func (c *GHACacheClient) commitCacheEntry(ctx context.Context, size int64) error {
	key, _ := c.blobKey()
	c.logger.Debugf("commit cache entry: key=%s, size=%d", key, size)

	var res struct {
		OK      bool   `json:"ok"`
		EntryID string `json:"entry_id"`
	}
	err := c.doRequest(ctx, "FinalizeCacheEntryUpload", &struct {
		Key       string `json:"key"`
		SizeBytes int64  `json:"size_bytes"`
		Version   string `json:"version"`
	}{key, size, actionsCacheVersion}, &res)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	if !res.OK {
		return errors.New("failed to commit cache")
	}

	c.logger.Debugf("commit done: key=%s", key)

	return nil
}
