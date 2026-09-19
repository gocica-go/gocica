package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/mazrean/gocica/internal/pkg/json"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
)

type GHACacheConfig struct {
	Token    string
	CacheURL string
	RunnerOS string
	Ref      string
	Sha      string
	// Prefix and KeyVersion namespace the cache entry. Empty values fall back to
	// the build cache namespace, so existing keys stay byte-for-byte identical.
	Prefix     string
	KeyVersion string
}

func GHACacheProvider(
	ctx context.Context,
	logger log.Logger,
	config *GHACacheConfig,
) (DownloadClientProvider, UploadClientProvider, error) {
	cacheClient, err := newGitHubCacheClient(ctx, logger, config)
	if err != nil {
		return nil, nil, fmt.Errorf("create github cache client: %w", err)
	}

	uploadClientProvider := func(context.Context) (core.UploadClient, error) {
		return &lazyUploadClient{logger: logger, cacheClient: cacheClient}, nil
	}

	downloadClientProvider := func(ctx context.Context) (core.DownloadClient, error) {
		downloadURL, err := cacheClient.getDownloadURL(ctx)
		if err != nil {
			logger.Debugf("get download url: %v", err)
			logger.Infof("cache not found. building without cache.")

			return nil, nil
		}

		downloadClient, err := newRefreshingDownloadClient(logger, cacheClient, downloadURL)
		if err != nil {
			return nil, fmt.Errorf("create refreshing download client: %w", err)
		}

		return downloadClient, nil
	}

	return downloadClientProvider, uploadClientProvider, nil
}

const (
	actionsCacheBasePath  = "/twirp/github.actions.results.api.v1.CacheService/"
	actionsCachePrefix    = "gocica-cache"
	actionsCacheSeparator = "-"

	// ModuleCachePrefix namespaces the Go module proxy cache entry.
	ModuleCachePrefix = "gocica-mod"
)

// actionsCacheVersion is sha256 of the context.
// upstream uses paths in actionsCacheVersion, we don't seem to have anything that is unique like this.
// so we use the sha256 of "gocica-cache-1.0" as a actionsCacheVersion.
var actionsCacheVersion = "5eb02eebd0c9b2a428c370e552c7c895ea26154c726235db0a053f746fae0287"

// ModuleCacheVersion is sha256 of "gocica-mod-1.0". GitHub treats version as part
// of the entry identity, so this keeps the module namespace isolated from the
// build cache even if the two ever shared a key prefix.
var ModuleCacheVersion = func() string {
	sum := sha256.Sum256([]byte("gocica-mod-1.0"))
	return hex.EncodeToString(sum[:])
}()

var (
	ErrCacheNotFound = errors.New("cache not found")
	ErrAlreadyExists = errors.New("cache already exists")
)

var githubAPILatencyGauge = metrics.NewGauge("github_cache_api_latency")

// ghaCacheClient handles GitHub Actions Cache API calls.
// This is a standalone client that doesn't depend on GitHubActionsCache.
type ghaCacheClient struct {
	logger     log.Logger
	httpClient *http.Client
	baseURL    *url.URL
	runnerOS   string
	ref        string
	sha        string
	prefix     string
	version    string
}

// newGitHubCacheClient creates a new GitHub Cache API client.
func newGitHubCacheClient(ctx context.Context, logger log.Logger, config *GHACacheConfig) (*ghaCacheClient, error) {
	baseURL, err := url.Parse(config.CacheURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	baseURL = baseURL.JoinPath(actionsCacheBasePath)

	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: config.Token,
	}))

	prefix := config.Prefix
	if prefix == "" {
		prefix = actionsCachePrefix
	}
	version := config.KeyVersion
	if version == "" {
		version = actionsCacheVersion
	}

	return &ghaCacheClient{
		logger:     logger,
		httpClient: httpClient,
		baseURL:    baseURL,
		runnerOS:   config.RunnerOS,
		ref:        config.Ref,
		sha:        config.Sha,
		prefix:     prefix,
		version:    version,
	}, nil
}

// blobKey returns the cache key and restore keys for this configuration.
func (c *ghaCacheClient) blobKey() (string, []string) {
	baseKey := c.prefix + actionsCacheSeparator + c.runnerOS
	restoreKeys := make([]string, 0, 2)
	for _, k := range []string{c.ref, c.sha} {
		baseKey += actionsCacheSeparator
		restoreKeys = append(restoreKeys, baseKey)
		baseKey += k
	}
	slices.Reverse(restoreKeys)

	return baseKey, restoreKeys
}

func (c *ghaCacheClient) doRequest(ctx context.Context, endpoint string, reqBody any, respBody any) error {
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
func (c *ghaCacheClient) getDownloadURL(ctx context.Context) (string, error) {
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
	}{key, restoreKeys, c.version}, &res)
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
func (c *ghaCacheClient) createCacheEntry(ctx context.Context) (string, error) {
	key, _ := c.blobKey()
	c.logger.Debugf("create cache entry: key=%s", key)

	var res struct {
		OK              bool   `json:"ok"`
		SignedUploadURL string `json:"signed_upload_url"`
	}
	err := c.doRequest(ctx, "CreateCacheEntry", &struct {
		Key     string `json:"key"`
		Version string `json:"version"`
	}{key, c.version}, &res)
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
func (c *ghaCacheClient) commitCacheEntry(ctx context.Context, size int64) error {
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
	}{key, size, c.version}, &res)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	if !res.OK {
		return errors.New("failed to commit cache")
	}

	c.logger.Debugf("commit done: key=%s", key)

	return nil
}
