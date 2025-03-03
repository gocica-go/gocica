package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/mazrean/gocica/internal/pkg/json"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/proto"
)

var _ RemoteBackend = &GitHubActionsCache{}

type GitHubActionsCache struct {
	logger                       log.Logger
	githubClient                 *http.Client
	baseURL                      *url.URL
	blockIDsLocker               sync.RWMutex
	blockIDs                     []string
	downloadClient, uploadClient *blockblob.Client
	runnerOS, ref, sha           string
}

func NewGitHubActionsCache(
	logger log.Logger,
	token string,
	strBaseURL string,
	runnerOS, ref, sha string,
) (*GitHubActionsCache, error) {
	baseURL, err := url.Parse(strBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	baseURL = baseURL.JoinPath(actionsCacheBasePath)

	githubClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
	}))

	c := &GitHubActionsCache{
		logger:       logger,
		githubClient: githubClient,
		baseURL:      baseURL,
		runnerOS:     runnerOS,
		ref:          ref,
		sha:          sha,
	}

	allObjectBlobKey, restoreKeys := c.allObjectBlobKey()

	downloadURL, err := c.getDownloadURL(context.Background(), allObjectBlobKey, restoreKeys)
	if err != nil {
		if !errors.Is(err, errActionsCacheNotFound) {
			return nil, fmt.Errorf("get download url: %w", err)
		}
		c.logger.Infof("cache not found, creating new cache entry")
	}
	if downloadURL != "" {
		c.downloadClient, err = blockblob.NewClientWithNoCredential(downloadURL, azureClientOptions)
		if err != nil {
			return nil, fmt.Errorf("create download client: %w", err)
		}
	}

	uploadURL, err := c.createCacheEntry(context.Background(), allObjectBlobKey)
	if err != nil {
		if !errors.Is(err, errAlreadyExists) {
			return nil, fmt.Errorf("create cache entry: %w", err)
		}
		c.logger.Infof("cache already exists, skipping upload")
	}
	if uploadURL != "" {
		c.uploadClient, err = blockblob.NewClientWithNoCredential(uploadURL, azureClientOptions)
		if err != nil {
			return nil, fmt.Errorf("create upload client: %w", err)
		}
	}

	logger.Infof("GitHub Actions cache backend initialized.")

	return c, nil
}

const (
	actionsCacheBasePath        = "/twirp/github.actions.results.api.v1.CacheService/"
	actionsCacheMetadataPrefix  = "gocica-r-metadata"
	actionsCacheAllObjectPrefix = "gocica-o-all"
	actionCacheObjectPrefix     = "gocica-o"
	actionsCacheSeparator       = "-"
)

// actionsCacheVersion is sha256 of the context.
// upstream uses paths in actionsCacheVersion, we don't seem to have anything that is unique like this.
// so we use the sha256 of "gocica-cache-1.0" as a actionsCacheVersion.
var actionsCacheVersion = "5eb02eebd0c9b2a428c370e552c7c895ea26154c726235db0a053f746fae0287"

var azureClientOptions = &blockblob.ClientOptions{
	ClientOptions: azcore.ClientOptions{},
}
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.JoinPath(endpoint).String(), buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.githubClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusNotFound:
		return errActionsCacheNotFound
	case http.StatusConflict:
		return errAlreadyExists
	case http.StatusOK:
		// continue to process response for successful request
	default:
		sb := &strings.Builder{}
		_, err := io.Copy(sb, res.Body)
		if err != nil {
			return fmt.Errorf("copy response body: %w", err)
		}

		return fmt.Errorf("unexpected status code: %d, body: %s", res.StatusCode, sb.String())
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

func (c *GitHubActionsCache) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	key, restoreKeys := c.metadataBlobKey()

	signedDownloadURL, err := c.getDownloadURL(ctx, key, restoreKeys)
	if err != nil {
		if errors.Is(err, errActionsCacheNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get download url: %w", err)
	}

	client, err := blockblob.NewClientWithNoCredential(signedDownloadURL, azureClientOptions)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	var buf []byte
	_, err = client.DownloadBuffer(ctx, buf, nil)
	if err != nil {
		return nil, fmt.Errorf("download stream: %w", err)
	}

	c.logger.Debugf("download done: key=%s", key)

	var indexEntryMap v1.IndexEntryMap
	if err := proto.Unmarshal(buf, &indexEntryMap); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	c.logger.Debugf("unmarshal index entry map done: size=%d", len(indexEntryMap.Entries))

	return indexEntryMap.Entries, nil
}

func (c *GitHubActionsCache) WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	indexEntryMap := &v1.IndexEntryMap{
		Entries: metaDataMap,
	}
	buf, err := proto.Marshal(indexEntryMap)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	key, _ := c.metadataBlobKey()

	signedUploadURL, err := c.createCacheEntry(ctx, key)
	if err != nil {
		return fmt.Errorf("create cache entry: %w", err)
	}

	client, err := blockblob.NewClientWithNoCredential(signedUploadURL, azureClientOptions)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	if _, err := client.UploadBuffer(ctx, buf, nil); err != nil {
		return fmt.Errorf("upload stream: %w", err)
	}

	c.logger.Debugf("upload done")

	if err := c.commitCacheEntry(ctx, key, int64(len(buf))); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) Get(ctx context.Context, objectID string, w io.Writer) error {
	key, restoreKeys := c.objectBlobKey(objectID)

	signedDownloadURL, err := c.getDownloadURL(ctx, key, restoreKeys)
	if err != nil {
		return fmt.Errorf("get download url: %w", err)
	}

	client, err := blockblob.NewClientWithNoCredential(signedDownloadURL, azureClientOptions)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	res, err := client.DownloadStream(ctx, nil)
	if err != nil {
		return fmt.Errorf("download stream: %w", err)
	}
	defer res.Body.Close()

	c.logger.Debugf("download done: key=%s", key)

	_, err = io.Copy(w, res.Body)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error {
	key, _ := c.objectBlobKey(objectID)

	signedUploadURL, err := c.createCacheEntry(ctx, key)
	if err != nil {
		if errors.Is(err, errAlreadyExists) {
			return nil
		}
		return fmt.Errorf("create cache entry: %w", err)
	}

	client, err := blockblob.NewClientWithNoCredential(signedUploadURL, azureClientOptions)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	if _, err := client.UploadStream(ctx, r, nil); err != nil {
		return fmt.Errorf("upload stream: %w", err)
	}

	c.logger.Debugf("upload done: key=%s", key)

	if err := c.commitCacheEntry(ctx, key, size); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}

	return err
}

func (c *GitHubActionsCache) metadataBlobKey() (string, []string) {
	return c.blobKey(actionsCacheMetadataPrefix)
}

func (c *GitHubActionsCache) objectBlobKey(objectID string) (string, []string) {
	return c.blobKey(actionCacheObjectPrefix + actionsCacheSeparator + objectID)
}

func (c *GitHubActionsCache) allObjectBlobKey() (string, []string) {
	return c.blobKey(actionsCacheAllObjectPrefix)
}

func (c *GitHubActionsCache) blobKey(baseKey string) (string, []string) {
	baseKey += actionsCacheSeparator + c.runnerOS
	restoreKeys := make([]string, 0, 2)
	for _, k := range []string{c.ref, c.sha} {
		baseKey += actionsCacheSeparator
		restoreKeys = append(restoreKeys, baseKey)
		baseKey += k
	}

	return baseKey, restoreKeys
}

func (c *GitHubActionsCache) Close(ctx context.Context) error {
	if c.uploadClient != nil {
		c.blockIDsLocker.RLock()
		defer c.blockIDsLocker.RUnlock()

		if _, err := c.uploadClient.CommitBlockList(ctx, c.blockIDs, nil); err != nil {
			return fmt.Errorf("commit block list: %w", err)
		}

		c.logger.Debugf("commit block list done")
	}

	return nil
}
