package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/google/uuid"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/json"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

var _ RemoteBackend = &GitHubActionsCache{}

type GitHubActionsCache struct {
	logger log.Logger

	baseURL      *url.URL
	githubClient *http.Client

	baseOffset                   int64
	downloadClient, uploadClient *blockblob.Client

	metadataMap     map[string]*v1.IndexEntry
	outputMap       map[string]*v1.ActionsOutput
	oldBlockID      string
	oldBlockCopyEg  *errgroup.Group
	blockMapLocker  sync.RWMutex
	blockMap        map[string]int64
	outputTotalSize int64

	runnerOS, ref, sha string
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
		logger:         logger,
		githubClient:   githubClient,
		baseURL:        baseURL,
		oldBlockCopyEg: &errgroup.Group{},
		blockMap:       map[string]int64{},
		runnerOS:       runnerOS,
		ref:            ref,
		sha:            sha,
	}

	eg := &errgroup.Group{}
	var (
		downloadURL               string
		headerOffset, oldBlobSize int64
	)
	eg.Go(func() error {
		var err error
		downloadURL, headerOffset, oldBlobSize, err = c.downloadSetup(context.Background())
		return err
	})

	if err := c.uploadSetup(context.Background()); err != nil {
		return nil, fmt.Errorf("upload setup: %w", err)
	}

	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("download setup: %w", err)
	}

	if downloadURL != "" {
		c.oldBlockCopyEg.Go(func() error {
			return c.oldBlockCopy(context.Background(), downloadURL, headerOffset, oldBlobSize)
		})
	}

	logger.Infof("GitHub Actions cache backend initialized.")

	return c, nil
}

const (
	actionsCacheBasePath  = "/twirp/github.actions.results.api.v1.CacheService/"
	actionsCachePrefix    = "gocica-cache"
	actionsCacheSeparator = "-"
)

func (c *GitHubActionsCache) downloadSetup(ctx context.Context) (string, int64, int64, error) {
	blobKey, restoreKeys := c.blobKey()

	downloadURL, err := c.getDownloadURL(context.Background(), blobKey, restoreKeys)
	if err != nil {
		if errors.Is(err, errActionsCacheNotFound) {
			c.logger.Infof("cache not found, creating new cache entry")
			return "", 0, 0, nil
		}
		return "", 0, 0, fmt.Errorf("get download url: %w", err)
	}

	c.downloadClient, err = blockblob.NewClientWithNoCredential(downloadURL, azureClientOptions)
	if err != nil {
		return "", 0, 0, fmt.Errorf("create download client: %w", err)
	}

	metadataMap, outputMap, outputTotalSize, headerOffset, err := c.parseHeader(ctx)
	if err != nil {
		return "", 0, 0, fmt.Errorf("parse header: %w", err)
	}

	c.metadataMap = metadataMap
	c.outputMap = outputMap
	c.outputTotalSize = outputTotalSize
	c.baseOffset = headerOffset + outputTotalSize

	return downloadURL, headerOffset, outputTotalSize, nil
}

func (c *GitHubActionsCache) uploadSetup(ctx context.Context) error {
	blobKey, _ := c.blobKey()

	uploadURL, err := c.createCacheEntry(ctx, blobKey)
	if err != nil {
		if errors.Is(err, errAlreadyExists) {
			c.logger.Infof("cache already exists, skipping upload")
			return nil
		}
		return fmt.Errorf("create cache entry: %w", err)
	}

	c.uploadClient, err = blockblob.NewClientWithNoCredential(uploadURL, azureClientOptions)
	if err != nil {
		return fmt.Errorf("create upload client: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) oldBlockCopy(ctx context.Context, downloadURL string, offset, size int64) error {
	oldBlobUUID := [16]byte(uuid.New())
	oldBlobID := base64.StdEncoding.EncodeToString(oldBlobUUID[:])
	_, err := c.uploadClient.StageBlockFromURL(ctx, oldBlobID, downloadURL, &blockblob.StageBlockFromURLOptions{
		Range: blob.HTTPRange{Offset: offset, Count: size},
	})
	if err != nil {
		return fmt.Errorf("stage block from url: %w", err)
	}

	return nil
}

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

func (c *GitHubActionsCache) createHeader(metaDataMap map[string]*v1.IndexEntry, blockMap map[string]int64, outputTotalSize int64) ([]byte, error) {
	offset := int64(0)
	outputMap := make(map[string]*v1.ActionsOutput, len(blockMap))
	for k, v := range blockMap {
		outputMap[k] = &v1.ActionsOutput{
			Offset: offset,
		}
		offset += v
	}

	protoBuf, err := proto.Marshal(&v1.ActionsCache{
		Entries:         metaDataMap,
		Outputs:         outputMap,
		OutputTotalSize: outputTotalSize,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	buf := make([]byte, 0, 8+len(protoBuf))
	binary.BigEndian.PutUint64(buf, uint64(len(protoBuf)))
	buf = append(buf, protoBuf...)

	return buf, nil
}

func (c *GitHubActionsCache) parseHeader(ctx context.Context) (map[string]*v1.IndexEntry, map[string]*v1.ActionsOutput, int64, int64, error) {
	buf := make([]byte, 0, 8)
	if err := c.loadBuffer(ctx, buf, 0, 8); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("load header size buffer: %w", err)
	}
	//nolint:gosec
	protobufSize := int64(binary.BigEndian.Uint64(buf))

	buf = make([]byte, 0, protobufSize)
	if err := c.loadBuffer(ctx, buf, 8, protobufSize); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("load header buffer: %w", err)
	}

	var actionsCache v1.ActionsCache
	if err := proto.Unmarshal(buf, &actionsCache); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("unmarshal: %w", err)
	}

	return actionsCache.Entries, actionsCache.Outputs, actionsCache.OutputTotalSize, 8 + protobufSize, nil
}

func (c *GitHubActionsCache) loadBuffer(ctx context.Context, buf []byte, offset, size int64) error {
	if _, err := c.downloadClient.DownloadBuffer(ctx, buf, &blob.DownloadBufferOptions{
		Range: blob.HTTPRange{Offset: offset, Count: size},
	}); err != nil {
		return fmt.Errorf("download stream: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) loadStream(ctx context.Context, w io.Writer, offset, size int64) error {
	res, err := c.downloadClient.DownloadStream(ctx, &blob.DownloadStreamOptions{
		Range: blob.HTTPRange{Offset: offset, Count: size},
	})
	if err != nil {
		return fmt.Errorf("download stream: %w", err)
	}
	defer res.Body.Close()

	if _, err := io.Copy(w, res.Body); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) MetaData(context.Context) (map[string]*v1.IndexEntry, error) {
	return c.metadataMap, nil
}

func (c *GitHubActionsCache) WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	if c.uploadClient == nil {
		return nil
	}

	key, _ := c.blobKey()

	var (
		blockMap        map[string]int64
		outputTotalSize int64
	)
	func() {
		c.blockMapLocker.RLock()
		defer c.blockMapLocker.RUnlock()
		blockMap = c.blockMap
		outputTotalSize = c.outputTotalSize
	}()

	header, err := c.createHeader(metaDataMap, blockMap, outputTotalSize)
	if err != nil {
		return fmt.Errorf("create header: %w", err)
	}

	headerBlobID := [16]byte(uuid.New())
	strHeaderBlobID := base64.StdEncoding.EncodeToString(headerBlobID[:])
	_, err = c.uploadClient.StageBlock(
		ctx,
		strHeaderBlobID,
		myio.NopSeekCloser(bytes.NewReader(header)),
		nil,
	)
	if err != nil {
		return fmt.Errorf("stage header block: %w", err)
	}

	if err := c.oldBlockCopyEg.Wait(); err != nil {
		return fmt.Errorf("old block copy: %w", err)
	}

	blockIDs := make([]string, 0, len(blockMap)+1)
	blockIDs = append(blockIDs, strHeaderBlobID)
	if c.oldBlockID != "" {
		blockIDs = append(blockIDs, c.oldBlockID)
	}
	blockIDs = append(blockIDs, slices.Collect(maps.Keys(blockMap))...)

	if _, err := c.uploadClient.CommitBlockList(ctx, blockIDs, nil); err != nil {
		return fmt.Errorf("commit block list: %w", err)
	}

	c.logger.Debugf("commit block list done")

	indexEntryMap := &v1.IndexEntryMap{
		Entries: metaDataMap,
	}
	protoBuf, err := proto.Marshal(indexEntryMap)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	c.logger.Debugf("metadata upload done")

	if err := c.commitCacheEntry(ctx, key, int64(len(protoBuf))); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}

	return nil
}

func (c *GitHubActionsCache) Get(ctx context.Context, objectID string, size int64, w io.Writer) error {
	if c.downloadClient == nil {
		return nil
	}

	object, ok := c.outputMap[objectID]
	if !ok {
		return nil
	}

	offset := c.baseOffset + object.Offset

	c.logger.Debugf("download: objectID=%s, offset=%d, size=%d", objectID, offset, size)

	if err := c.loadStream(ctx, w, offset, size); err != nil {
		return fmt.Errorf("load stream: %w", err)
	}

	c.logger.Debugf("download done: objectID=%s", objectID)

	return nil
}

func (c *GitHubActionsCache) Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error {
	if c.uploadClient == nil {
		return nil
	}

	c.logger.Debugf("upload: objectID=%s, size=%d", objectID, size)

	_, err := c.uploadClient.StageBlock(
		ctx,
		objectID,
		myio.NopSeekCloser(r),
		nil,
	)
	if err != nil {
		return fmt.Errorf("stage header block: %w", err)
	}

	c.logger.Debugf("stage block done: objectID=%s", objectID)

	c.blockMapLocker.Lock()
	defer c.blockMapLocker.Unlock()
	c.blockMap[objectID] = size
	c.outputTotalSize += size

	return err
}

func (c *GitHubActionsCache) blobKey() (string, []string) {
	baseKey := actionsCachePrefix + actionsCacheSeparator + c.runnerOS
	restoreKeys := make([]string, 0, 2)
	for _, k := range []string{c.ref, c.sha} {
		baseKey += actionsCacheSeparator
		restoreKeys = append(restoreKeys, baseKey)
		baseKey += k
	}

	return baseKey, restoreKeys
}

func (c *GitHubActionsCache) Close(context.Context) error {
	return nil
}
