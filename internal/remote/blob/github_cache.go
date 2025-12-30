package blob

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

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/mazrean/gocica/internal/pkg/json"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
)

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

// GitHubCacheClient handles GitHub Actions Cache API calls.
// This is a standalone client that doesn't depend on GitHubActionsCache.
type GitHubCacheClient struct {
	logger     log.Logger
	httpClient *http.Client
	baseURL    *url.URL
	runnerOS   string
	ref        string
	sha        string
}

// NewGitHubCacheClient creates a new GitHub Cache API client.
func NewGitHubCacheClient(
	ctx context.Context,
	logger log.Logger,
	token string,
	strBaseURL string,
	runnerOS, ref, sha string,
) (*GitHubCacheClient, error) {
	baseURL, err := url.Parse(strBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	baseURL = baseURL.JoinPath(actionsCacheBasePath)

	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
	}))

	return &GitHubCacheClient{
		logger:     logger,
		httpClient: httpClient,
		baseURL:    baseURL,
		runnerOS:   runnerOS,
		ref:        ref,
		sha:        sha,
	}, nil
}

// blobKey returns the cache key and restore keys for this configuration.
func (c *GitHubCacheClient) blobKey() (string, []string) {
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

func (c *GitHubCacheClient) doRequest(ctx context.Context, endpoint string, reqBody any, respBody any) error {
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
func (c *GitHubCacheClient) getDownloadURL(ctx context.Context) (string, error) {
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
func (c *GitHubCacheClient) createCacheEntry(ctx context.Context) (string, error) {
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
func (c *GitHubCacheClient) CommitCacheEntry(ctx context.Context, size int64) error {
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

// NewDownloadClient creates an Azure blob download client from the download URL.
// Returns nil if download URL is empty (cache not found).
func NewDownloadClient(ctx context.Context, cacheClient *GitHubCacheClient) (DownloadClient, error) {
	url, err := cacheClient.getDownloadURL(ctx)
	if err != nil {
		if errors.Is(err, ErrCacheNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get download url: %w", err)
	}

	client, err := blockblob.NewClientWithNoCredential(url, azureConfig)
	if err != nil {
		return nil, fmt.Errorf("create download client: %w", err)
	}
	return NewAzureDownloadClient(client), nil
}

// NewUploadClient creates an Azure blob upload client from the upload URL.
// Returns nil if upload URL is empty (cache already exists).
func NewUploadClient(ctx context.Context, cacheClient *GitHubCacheClient) (UploadClient, error) {
	url, err := cacheClient.createCacheEntry(ctx)
	if err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil, nil
		}
		return nil, fmt.Errorf("create cache entry: %w", err)
	}

	client, err := blockblob.NewClientWithNoCredential(url, azureConfig)
	if err != nil {
		return nil, fmt.Errorf("create upload client: %w", err)
	}
	return NewAzureUploadClient(client), nil
}

// NewUploaderOrNil creates an Uploader from an UploadClient.
// Returns nil if client is nil (cache already exists).
// Uses downloader as BaseBlobProvider if available.
func NewUploaderOrNil(
	ctx context.Context,
	logger log.Logger,
	client UploadClient,
	downloader *Downloader,
) *Uploader {
	if client == nil {
		return nil
	}
	return NewUploader(ctx, logger, client, downloader)
}
