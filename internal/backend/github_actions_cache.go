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
	myhttp "github.com/mazrean/gocica/internal/pkg/http"
	"github.com/mazrean/gocica/internal/pkg/json"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/proto"
)

var _ RemoteBackend = &GitHubActionsCache{}

type GitHubActionsCache struct {
	logger       log.Logger
	githubClient *http.Client
}

func NewGitHubActionsCache(
	logger log.Logger,
	token string,
) *GitHubActionsCache {
	githubClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
	}))
	myhttp.ClientSetting(githubClient)

	return &GitHubActionsCache{
		logger:       logger,
		githubClient: githubClient,
	}
}

const (
	githubActionsCacheBaseURL    = "https://api.github.com/twirp/github.actions.results.api.v1.CacheService/"
	githubActionsCacheMetadatKey = "gocica/r-metadata"
)

var azureClientOptions = &blockblob.ClientOptions{
	ClientOptions: azcore.ClientOptions{},
}

func (c *GitHubActionsCache) doRequest(ctx context.Context, endpoint string, reqBody interface{}, respBody interface{}) error {
	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(reqBody)
	if err != nil {
		return fmt.Errorf("encode request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubActionsCacheBaseURL+endpoint, buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.githubClient.Do(req)
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

		return fmt.Errorf("unexpected status code: %d, body: %s", res.StatusCode, sb.String())
	}

	if err := json.NewDecoder(res.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) loadCache(ctx context.Context, key string) (io.ReadCloser, error) {
	var loadResp struct {
		OK                bool   `json:"ok"`
		SignedDownloadURL string `json:"signed_download_url"`
		MatchedKey        string `json:"matched_key"`
	}
	if err := c.doRequest(ctx, "GetCacheEntryDownloadURL", &struct {
		Key         string   `json:"key"`
		RestoreKeys []string `json:"restore_keys"`
		Version     string   `json:"version"`
	}{key, nil, "1"}, &loadResp); err != nil {
		return nil, err
	}

	if !loadResp.OK {
		return nil, errors.New("cache not found")
	}

	client, err := blockblob.NewClientWithNoCredential(loadResp.SignedDownloadURL, azureClientOptions)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	res, err := client.DownloadStream(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("download stream: %w", err)
	}

	return res.Body, nil
}

func (c *GitHubActionsCache) storeCache(ctx context.Context, key string, size int64, r io.Reader) error {
	var reserveResp struct {
		OK              bool   `json:"ok"`
		SignedUploadURL string `json:"signed_upload_url"`
	}
	if err := c.doRequest(ctx, "CreateCacheEntry", &struct {
		Key     string `json:"key"`
		Version string `json:"version"`
	}{key, "1"}, &reserveResp); err != nil {
		return err
	}

	if !reserveResp.OK {
		return errors.New("failed to reserve cache")
	}

	client, err := blockblob.NewClientWithNoCredential(reserveResp.SignedUploadURL, azureClientOptions)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	if _, err := client.UploadStream(ctx, r, nil); err != nil {
		return fmt.Errorf("upload stream: %w", err)
	}

	var commitResp struct {
		OK      bool   `json:"ok"`
		EntryID string `json:"entry_id"`
	}
	if err := c.doRequest(ctx, "FinalizeCacheEntryUpload", &struct {
		Key       string `json:"key"`
		SizeBytes int64  `json:"size_bytes"`
		Version   string `json:"version"`
	}{key, size, "1"}, &commitResp); err != nil {
		return err
	}

	if !commitResp.OK {
		return errors.New("failed to commit cache")
	}

	return nil
}

func (c *GitHubActionsCache) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	res, err := c.loadCache(ctx, githubActionsCacheMetadatKey)
	if err != nil {
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
	return c.storeCache(ctx, githubActionsCacheMetadatKey, int64(len(metaDataMapBuf)), bytes.NewReader(metaDataMapBuf))
}

func (c *GitHubActionsCache) Get(ctx context.Context, objectID string, w io.Writer) error {
	res, err := c.loadCache(ctx, c.objectBlobKey(objectID))
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
	return c.storeCache(ctx, c.objectBlobKey(objectID), size, r)
}

func (c *GitHubActionsCache) objectBlobKey(objectID string) string {
	return fmt.Sprintf("gocica/o-%s", encodeID(objectID))
}

func (c *GitHubActionsCache) Close() error {
	return nil
}
