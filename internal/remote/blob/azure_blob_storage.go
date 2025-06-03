package blob

import (
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/mazrean/gocica/internal/metrics"
)

var _ UploadClient = (*AzureUploadClient)(nil)
var latencyGauge = metrics.NewGauge("azure_blob_storage_latency")

type AzureUploadClient struct {
	client *blockblob.Client
}

func NewAzureUploadClient(client *blockblob.Client) *AzureUploadClient {
	return &AzureUploadClient{client: client}
}

func (a *AzureUploadClient) UploadBlock(ctx context.Context, blockID string, r io.ReadSeekCloser) (int64, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("get size: %w", err)
	}
	_, err = r.Seek(0, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("seek start: %w", err)
	}

	latencyGauge.Stopwatch(func() {
		_, err = a.client.StageBlock(ctx, blockID, r, nil)
	}, "stage_block")
	if err != nil {
		return 0, fmt.Errorf("stage block: %w", err)
	}

	return size, nil
}

func (a *AzureUploadClient) UploadBlockFromURL(ctx context.Context, blockID string, url string, offset, size int64) error {
	var err error
	latencyGauge.Stopwatch(func() {
		_, err = a.client.StageBlockFromURL(ctx, blockID, url, &blockblob.StageBlockFromURLOptions{
			Range: blob.HTTPRange{Offset: offset, Count: size},
		})
	}, "stage_block_from_url")
	if err != nil {
		return fmt.Errorf("stage block from url: %w", err)
	}

	return nil
}

func (a *AzureUploadClient) Commit(ctx context.Context, blockIDs []string) error {
	var err error
	latencyGauge.Stopwatch(func() {
		_, err = a.client.CommitBlockList(ctx, blockIDs, nil)
	}, "commit_block_list")
	if err != nil {
		return fmt.Errorf("commit block list: %w", err)
	}

	return nil
}

var _ DownloadClient = (*AzureDownloadClient)(nil)

type AzureDownloadClient struct {
	client *blockblob.Client
}

func NewAzureDownloadClient(client *blockblob.Client) *AzureDownloadClient {
	return &AzureDownloadClient{client: client}
}

func (a *AzureDownloadClient) GetURL(context.Context) string {
	return a.client.URL()
}

func (a *AzureDownloadClient) DownloadBlock(ctx context.Context, offset int64, size int64, w io.Writer) error {
	var (
		res blob.DownloadStreamResponse
		err error
	)
	latencyGauge.Stopwatch(func() {
		res, err = a.client.DownloadStream(ctx, &blob.DownloadStreamOptions{
			Range: blob.HTTPRange{Offset: offset, Count: size},
		})
	}, "download_stream")
	if err != nil {
		return fmt.Errorf("download stream: %w", err)
	}
	defer res.Body.Close()

	if _, err := io.Copy(w, res.Body); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func (a *AzureDownloadClient) DownloadBlockBuffer(ctx context.Context, offset int64, size int64, buf []byte) error {
	var err error
	latencyGauge.Stopwatch(func() {
		_, err = a.client.DownloadBuffer(ctx, buf, &blob.DownloadBufferOptions{
			Range: blob.HTTPRange{Offset: offset, Count: size},
		})
	}, "download_buffer")
	if err != nil {
		return fmt.Errorf("download buffer: %w", err)
	}

	return nil
}
