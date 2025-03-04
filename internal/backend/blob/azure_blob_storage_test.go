package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/google/go-cmp/cmp"
	"github.com/mazrean/gocica/log"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

const (
	testAccount   = "devstoreaccount1"
	testKey       = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1"
	testContainer = "testcontainer"
	testBlob      = "testblob"
)

var (
	pool     *dockertest.Pool
	resource *dockertest.Resource
	endpoint string
)

func TestMain(m *testing.M) {
	var err error
	pool, err = dockertest.NewPool("")
	if err != nil {
		log.DefaultLogger.Errorf("Failed to create Docker pool: %s", err)
		os.Exit(1)
	}

	opts := &dockertest.RunOptions{
		Repository: "mcr.microsoft.com/azure-storage/azurite",
		Tag:        "latest",
		Env:        []string{fmt.Sprintf(`AZURITE_ACCOUNTS="%s:%s"`, testAccount, testKey)},
		Cmd:        []string{"azurite-blob", "--blobHost", "0.0.0.0", "--loose"},
	}
	resource, err = pool.RunWithOptions(opts, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{
			Name: "no",
		}
	})
	if err != nil {
		log.DefaultLogger.Errorf("Failed to start Azurite container: %s", err)
		os.Exit(1)
	}

	var code int
	defer func() {
		if err := pool.Purge(resource); err != nil {
			log.DefaultLogger.Errorf("Failed to purge Azurite container: %s", err)
		}
		os.Exit(code)
	}()

	endpoint = fmt.Sprintf("http://127.0.0.1:%s", resource.GetPort("10000/tcp"))

	// Wait for the container to be ready
	if err := pool.Retry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		cred, err := azblob.NewSharedKeyCredential(testAccount, testKey)
		if err != nil {
			return fmt.Errorf("create shared key credential: %w", err)
		}

		client, err := blockblob.NewClientWithSharedKeyCredential(fmt.Sprintf("%s/%s", testAccount, testKey), cred, &blockblob.ClientOptions{
			ClientOptions: azcore.ClientOptions{
				InsecureAllowCredentialWithHTTP: true,
				Retry: policy.RetryOptions{
					MaxRetries: 3,
					RetryDelay: 1 * time.Second,
				},
			},
		})
		if err != nil {
			return fmt.Errorf("create client: %w", err)
		}

		_, err = client.DownloadStream(ctx, nil)
		if err != nil && !strings.Contains(err.Error(), "ContainerNotFound") {
			return fmt.Errorf("connect to Azurite: %w", err)
		}

		return nil
	}); err != nil {
		log.DefaultLogger.Errorf("Failed to connect to Azurite: %s", err)
		panic(fmt.Sprintf("Failed to connect to Azurite: %s", err))
	}

	code = m.Run()
}

// readSeekCloserはio.ReadSeekCloserを実装します
type readSeekCloser struct {
	*bytes.Reader
}

func (r *readSeekCloser) Close() error {
	return nil
}

func newReadSeekCloser(data []byte) io.ReadSeekCloser {
	return &readSeekCloser{
		Reader: bytes.NewReader(data),
	}
}

func newAzureUploadClient(t *testing.T) *AzureUploadClient {
	cred, err := azblob.NewSharedKeyCredential(testAccount, testKey)
	if err != nil {
		t.Fatalf("Failed to create shared key credential: %v", err)
	}
	client, err := blockblob.NewClientWithSharedKeyCredential(endpoint+"/"+testAccount, cred, &blockblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Retry: policy.RetryOptions{
				MaxRetries: 3,
				RetryDelay: 1 * time.Second,
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create blob client: %v", err)
	}
	return NewAzureUploadClient(client)
}

func newAzureDownloadClient(t *testing.T) *AzureDownloadClient {
	cred, err := azblob.NewSharedKeyCredential(testAccount, testKey)
	if err != nil {
		t.Fatalf("Failed to create shared key credential: %v", err)
	}

	client, err := blockblob.NewClientWithSharedKeyCredential(endpoint+"/"+testAccount, cred, &blockblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Retry: policy.RetryOptions{
				MaxRetries: 3,
				RetryDelay: 1 * time.Second,
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create blob client: %v", err)
	}
	return NewAzureDownloadClient(client)
}

func TestUploadAndDownload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		blockID       string
		data          []byte
		size          int64
		isUploadErr   bool
		isDownloadErr bool
	}{
		{
			name:    "normal data",
			blockID: "block1",
			data:    []byte("test upload method"),
			size:    17,
		},
		{
			name:    "empty data",
			blockID: "block2",
			data:    []byte{},
			size:    0,
		},
		{
			name:    "large data",
			blockID: "block3",
			data:    bytes.Repeat([]byte("a"), 1024*1024*10),
			size:    1024 * 1024 * 10,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uploadClient := newAzureUploadClient(t)
			downloadClient := newAzureDownloadClient(t)

			// Test UploadBlock
			size, err := uploadClient.UploadBlock(context.Background(), tt.blockID, newReadSeekCloser(tt.data))
			if tt.isUploadErr {
				if err == nil {
					t.Fatal("UploadBlock should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("UploadBlock failed: %v", err)
			}
			if size != tt.size {
				t.Errorf("UploadBlock size = %d, want %d", size, tt.size)
			}

			// Commit the block
			err = uploadClient.Commit(context.Background(), []string{tt.blockID})
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			// Test DownloadBlock
			var buf bytes.Buffer
			err = downloadClient.DownloadBlock(context.Background(), 0, size, &buf)
			if tt.isDownloadErr {
				if err == nil {
					t.Fatal("DownloadBlock should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("DownloadBlock failed: %v", err)
			}
			if diff := cmp.Diff(tt.data, buf.Bytes()); diff != "" {
				t.Errorf("DownloadBlock data mismatch (-want +got):\n%s", diff)
			}

			// Test DownloadBlockBuffer
			bufferData := make([]byte, size)
			err = downloadClient.DownloadBlockBuffer(context.Background(), 0, size, bufferData)
			if err != nil {
				t.Fatalf("DownloadBlockBuffer failed: %v", err)
			}
			if diff := cmp.Diff(tt.data, bufferData); diff != "" {
				t.Errorf("DownloadBlockBuffer data mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUploadBlockFromURL(t *testing.T) {
	t.Parallel()

	uploadClient := newAzureUploadClient(t)
	downloadClient := newAzureDownloadClient(t)

	// Upload initial data
	data := []byte("test upload from url")
	size, err := uploadClient.UploadBlock(context.Background(), "block1", newReadSeekCloser(data))
	if err != nil {
		t.Fatalf("UploadBlock failed: %v", err)
	}

	// Commit the block
	err = uploadClient.Commit(context.Background(), []string{"block1"})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Get URL
	url := downloadClient.GetURL(context.Background())

	// Create new blob with data from URL
	newUploadClient := newAzureUploadClient(t)
	err = newUploadClient.UploadBlockFromURL(context.Background(), "block2", url, 0, size)
	if err != nil {
		t.Fatalf("UploadBlockFromURL failed: %v", err)
	}

	// Commit the block
	err = newUploadClient.Commit(context.Background(), []string{"block2"})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify the data
	var buf bytes.Buffer
	err = downloadClient.DownloadBlock(context.Background(), 0, size, &buf)
	if err != nil {
		t.Fatalf("DownloadBlock failed: %v", err)
	}
	if diff := cmp.Diff(data, buf.Bytes()); diff != "" {
		t.Errorf("DownloadBlock data mismatch (-want +got):\n%s", diff)
	}
}
