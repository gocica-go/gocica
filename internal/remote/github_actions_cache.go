package remote

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

	"github.com/mazrean/gocica/internal/closer"
	"github.com/mazrean/gocica/internal/config"
	"github.com/mazrean/gocica/internal/local"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/json"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	"github.com/mazrean/gocica/internal/remote/blob"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
)

var _ Backend = &GitHubActionsCache{}

var latencyGauge = metrics.NewGauge("github_actions_cache_latency")

type GitHubActionsCache struct {
	logger log.Logger

	baseURL      *url.URL
	githubClient *http.Client

	uploader   *blob.Uploader
	downloader *blob.Downloader

	runnerOS, ref, sha string
}

func NewGitHubActionsCache(
	logger log.Logger,
	config *config.Config,
	localBackend local.Backend,
) (*GitHubActionsCache, error) {
	ctx := context.Background()

	if config.Github.Token == "" {
		return nil, fmt.Errorf("GitHub token is not specified")
	}

	baseURL, err := url.Parse(config.Github.CacheURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	baseURL = baseURL.JoinPath(actionsCacheBasePath)

	githubClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: config.Github.Token,
	}))

	c := &GitHubActionsCache{
		logger:       logger,
		githubClient: githubClient,
		baseURL:      baseURL,
		runnerOS:     config.Github.RunnerOS,
		ref:          config.Github.Ref,
		sha:          config.Github.Sha,
	}
	closer.Add(c.Close)

	c.downloader, err = c.setupDownloader(ctx, localBackend)
	if err != nil {
		return nil, fmt.Errorf("setup downloader: %w", err)
	}

	c.uploader, err = c.setupUploader(ctx, c.downloader)
	if err != nil {
		return nil, fmt.Errorf("setup uploader: %w", err)
	}

	logger.Infof("GitHub Actions cache backend initialized.")

	return c, nil
}

const (
	actionsCacheBasePath  = "/twirp/github.actions.results.api.v1.CacheService/"
	actionsCachePrefix    = "gocica-cache"
	actionsCacheSeparator = "-"
)

func (c *GitHubActionsCache) setupDownloader(ctx context.Context, localBackend local.Backend) (*blob.Downloader, error) {
	blobKey, restoreKeys := c.blobKey()

	downloadURL, err := c.getDownloadURL(ctx, blobKey, restoreKeys)
	if err != nil {
		c.logger.Debugf("get download url: %v", err)
		c.logger.Infof("cache not found, creating new cache entry")
		return nil, nil
	}

	downloadClient, err := blob.NewAzureDownloadClient(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("create download client: %w", err)
	}

	downloader, err := blob.NewDownloader(ctx, c.logger, downloadClient, localBackend)
	if err != nil {
		return nil, fmt.Errorf("create downloader: %w", err)
	}

	return downloader, nil
}

func (c *GitHubActionsCache) setupUploader(ctx context.Context, downloader *blob.Downloader) (*blob.Uploader, error) {
	blobKey, _ := c.blobKey()

	uploadURL, err := c.createCacheEntry(ctx, blobKey)
	if err != nil {
		if errors.Is(err, errAlreadyExists) {
			c.logger.Infof("cache already exists, skipping upload")
			return nil, nil
		}
		return nil, fmt.Errorf("create cache entry: %w", err)
	}

	uploadClient, err := blob.NewAzureUploadClient(uploadURL)
	if err != nil {
		return nil, fmt.Errorf("create upload client: %w", err)
	}

	uploader, err := blob.NewUploader(ctx, c.logger, uploadClient, downloader)
	if err != nil {
		return nil, fmt.Errorf("create uploader: %w", err)
	}

	return uploader, nil
}

// actionsCacheVersion is sha256 of the context.
// upstream uses paths in actionsCacheVersion, we don't seem to have anything that is unique like this.
// so we use the sha256 of "gocica-cache-1.0" as a actionsCacheVersion.
const actionsCacheVersion = "5eb02eebd0c9b2a428c370e552c7c895ea26154c726235db0a053f746fae0287"

var (
	errActionsCacheNotFound = errors.New("cache not found")
	errAlreadyExists        = errors.New("cache already exists")
)

func (c *GitHubActionsCache) doRequest(ctx context.Context, endpoint string, reqBody any, respBody any) error {
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
	latencyGauge.Stopwatch(func() {
		res, err = c.githubClient.Do(req)
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
			return fmt.Errorf("%w: %s", errActionsCacheNotFound, sb.String())
		case http.StatusConflict:
			return fmt.Errorf("%w: %s", errAlreadyExists, sb.String())
		default:
			return fmt.Errorf("unexpected status code: %d, body: %s", res.StatusCode, sb.String())
		}
	}

	if err := json.NewDecoder(res.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) getDownloadURL(ctx context.Context, key string, restoreKeys []string) (string, error) {
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

func (c *GitHubActionsCache) createCacheEntry(ctx context.Context, key string) (string, error) {
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

func (c *GitHubActionsCache) commitCacheEntry(ctx context.Context, key string, size int64) error {
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

func (c *GitHubActionsCache) MetaData(ctx context.Context, actionID string) (*MetaData, error) {
	entry, err := c.downloader.GetEntry(ctx, actionID)
	if err != nil {
		return nil, fmt.Errorf("get entry: %w", err)
	}

	if err := c.uploader.UpdateEntry(ctx, actionID, entry); err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}

	return &MetaData{
		OutputID: entry.OutputId,
		Size:     entry.Size,
		Timenano: entry.Timenano,
	}, nil
}

func (c *GitHubActionsCache) Put(ctx context.Context, actionID, objectID string, size int64, r io.ReadSeeker) error {
	if c.uploader == nil {
		return nil
	}

	if err := c.uploader.UploadOutput(ctx, objectID, size, myio.NopSeekCloser(r)); err != nil {
		return fmt.Errorf("upload output: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) blobKey() (string, []string) {
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

func (c *GitHubActionsCache) Close(ctx context.Context) error {
	if c.uploader == nil {
		return nil
	}

	size, err := c.uploader.Commit(ctx)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	key, _ := c.blobKey()
	if err := c.commitCacheEntry(ctx, key, size); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}

	return nil
}
