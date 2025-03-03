package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

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
	logger             log.Logger
	githubClient       *http.Client
	runnerOS, ref, sha string
}

func NewGitHubActionsCache(
	logger log.Logger,
	token string,
	runnerOS, ref, sha string,
) *GitHubActionsCache {
	githubClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
	}))
	return &GitHubActionsCache{
		logger:       logger,
		githubClient: githubClient,
		runnerOS:     runnerOS,
		ref:          ref,
		sha:          sha,
	}
}

const (
	actionsCacheBaseURL        = "https://api.github.com/twirp/github.actions.results.api.v1.CacheService/"
	actionsCacheMetadataPrefix = "gocica-r-metadata"
	actionCacheObjectPrefix    = "gocica-o"
	actionsCacheSeparator      = "-"
)

var azureClientOptions = &blockblob.ClientOptions{
	ClientOptions: azcore.ClientOptions{},
}
var errActionsCacheNotFound = errors.New("cache not found")

func (c *GitHubActionsCache) doRequest(ctx context.Context, endpoint string, reqBody any, respBody any) error {
	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(reqBody)
	if err != nil {
		return fmt.Errorf("encode request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, actionsCacheBaseURL+endpoint, buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.githubClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return errActionsCacheNotFound
	} else if res.StatusCode != http.StatusOK {
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

func (c *GitHubActionsCache) loadCache(ctx context.Context, key string, restoreKeys []string) (io.ReadCloser, error) {
	c.logger.Debugf("load cache: key=%s, restoreKeys=%v", key, restoreKeys)

	var loadResp struct {
		OK                bool   `json:"ok"`
		SignedDownloadURL string `json:"signed_download_url"`
		MatchedKey        string `json:"matched_key"`
	}
	err := c.doRequest(ctx, "GetCacheEntryDownloadURL", &struct {
		Key         string   `json:"key"`
		RestoreKeys []string `json:"restore_keys"`
		Version     string   `json:"version"`
	}{key, restoreKeys, "1"}, &loadResp)
	if err != nil {
		return nil, fmt.Errorf("get cache entry download url: %w", err)
	}

	if !loadResp.OK {
		return nil, errors.New("cache not found")
	}

	c.logger.Debugf("signed download url: %s", loadResp.SignedDownloadURL)

	client, err := blockblob.NewClientWithNoCredential(loadResp.SignedDownloadURL, azureClientOptions)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	res, err := client.DownloadStream(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("download stream: %w", err)
	}

	c.logger.Debugf("download done")

	return res.Body, nil
}

func (c *GitHubActionsCache) storeCache(ctx context.Context, key string, size int64, r io.Reader) error {
	c.logger.Debugf("store cache: key=%s, size=%d", key, size)
	var reserveRes struct {
		OK              bool   `json:"ok"`
		SignedUploadURL string `json:"signed_upload_url"`
	}
	err := c.doRequest(ctx, "CreateCacheEntry", &struct {
		Key     string `json:"key"`
		Version string `json:"version"`
	}{key, "1"}, &reserveRes)
	if err != nil {
		return err
	}

	if !reserveRes.OK {
		return errors.New("failed to reserve cache")
	}
	c.logger.Debugf("signed upload url: %s", reserveRes.SignedUploadURL)

	client, err := blockblob.NewClientWithNoCredential(reserveRes.SignedUploadURL, azureClientOptions)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	if _, err := client.UploadStream(ctx, r, nil); err != nil {
		return fmt.Errorf("upload stream: %w", err)
	}

	c.logger.Debugf("upload done")

	var commitRes struct {
		OK      bool   `json:"ok"`
		EntryID string `json:"entry_id"`
	}
	if err := c.doRequest(ctx, "FinalizeCacheEntryUpload", &struct {
		Key       string `json:"key"`
		SizeBytes int64  `json:"size_bytes"`
		Version   string `json:"version"`
	}{key, size, "1"}, &commitRes); err != nil {
		return err
	}

	if !commitRes.OK {
		return errors.New("failed to commit cache")
	}

	c.logger.Debugf("commit done")

	return nil
}

func (c *GitHubActionsCache) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	key, restoreKeys := c.metadataBlobKey()
	res, err := c.loadCache(ctx, key, restoreKeys)
	if err != nil {
		if errors.Is(err, errActionsCacheNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load cache: %w", err)
	}
	defer res.Close()

	metaDataBuf, err := io.ReadAll(res)
	if err != nil {
		return nil, fmt.Errorf("read all: %w", err)
	}

	var indexEntryMap v1.IndexEntryMap
	if err := proto.Unmarshal(metaDataBuf, &indexEntryMap); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	return indexEntryMap.Entries, nil
}

func (c *GitHubActionsCache) WriteMetaData(ctx context.Context, metaDataMapBuf []byte) error {
	key, _ := c.metadataBlobKey()
	return c.storeCache(ctx, key, int64(len(metaDataMapBuf)), bytes.NewReader(metaDataMapBuf))
}

func (c *GitHubActionsCache) Get(ctx context.Context, objectID string, w io.Writer) error {
	key, restoreKeys := c.objectBlobKey(objectID)
	res, err := c.loadCache(ctx, key, restoreKeys)
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}
	defer res.Close()

	_, err = io.Copy(w, res)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) Put(ctx context.Context, objectID string, size int64, r io.Reader) error {
	key, _ := c.objectBlobKey(objectID)
	return c.storeCache(ctx, key, size, r)
}

func (c *GitHubActionsCache) metadataBlobKey() (string, []string) {
	return c.blobKey(actionsCacheMetadataPrefix)
}

func (c *GitHubActionsCache) objectBlobKey(objectID string) (string, []string) {
	return c.blobKey(actionCacheObjectPrefix + actionsCacheSeparator + objectID)
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

func (c *GitHubActionsCache) Close() error {
	return nil
}
