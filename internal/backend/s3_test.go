package backend

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

const (
	testUser        = "minioadmin"
	testPassword    = "minioadmin"
	testBucket      = "testbucket"
	testRegion      = "us-east-1"
	exampleActionID = "v3BOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s="
	exampleOutputID = "t/+D8XWCl4fwI29I1bd78wcUkCQeI2DJrT5jggWZZMk="
)

// Global variables to hold dockertest pool, resource, and endpoint.
var (
	pool     *dockertest.Pool
	resource *dockertest.Resource
	endpoint string
)

// TestMain launches the MinIO container and cleans up after tests.
func TestMain(m *testing.M) {
	var err error
	pool, err = dockertest.NewPool("")
	if err != nil {
		log.Fatalf("Failed to connect to docker: %s", err)
	}

	opts := &dockertest.RunOptions{
		Repository: "minio/minio",
		Tag:        "latest",
		Env: []string{
			fmt.Sprintf("MINIO_ROOT_USER=%s", testUser),
			fmt.Sprintf("MINIO_ROOT_PASSWORD=%s", testPassword),
			fmt.Sprintf("MINIO_REGION=%s", testRegion),
		},
		Cmd:          []string{"server", "/data", "--console-address", ":9001"},
		ExposedPorts: []string{"9000/tcp", "9001/tcp"},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"9000/tcp": {{HostIP: "", HostPort: "9000"}},
			"9001/tcp": {{HostIP: "", HostPort: "9001"}},
		},
	}
	resource, err = pool.RunWithOptions(opts, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{
			Name: "no",
		}
	})
	if err != nil {
		log.Fatalf("Failed to start MinIO container: %s", err)
	}
	/*defer func() {
		if err := pool.Purge(resource); err != nil {
			log.Printf("Failed to purge MinIO container: %s", err)
		}
	}()*/

	endpoint = fmt.Sprintf("localhost:%s", resource.GetPort("9000/tcp"))

	// Wait for the container to be ready and ensure the "testbucket" exists.
	if err := pool.Retry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		client, err := minio.New(endpoint, &minio.Options{
			Region: testRegion,
			Creds:  credentials.NewStaticV4(testUser, testPassword, ""),
			Secure: false,
		})
		if err != nil {
			return fmt.Errorf("initialize MinIO client: %w", err)
		}

		if err := client.MakeBucket(ctx, testBucket, minio.MakeBucketOptions{}); err != nil {
			return err
		}

		return nil
	}); err != nil {
		log.Fatalf("Failed to connect to MinIO container: %s", err)
	}

	code := m.Run()

	os.Exit(code)
}

// newS3Instance creates an S3 instance for testing.
func newS3Instance(t *testing.T) *S3 {
	s3Inst, err := NewS3(endpoint, testRegion, testUser, testPassword, testBucket, false)
	if err != nil {
		t.Fatalf("Failed to create S3 instance: %v", err)
	}
	return s3Inst
}

// TestPut validates the behavior of the Put method.
func TestPutAndGet(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		actionID string
		data     []byte
		meta     *MetaData
	}{
		{
			name: "normal data",
			data: []byte("test put method"),
			// base64エンコードされた値
			actionID: "v3BOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s=",
			meta: &MetaData{
				OutputID: exampleOutputID,
				Size:     15,
				Timenano: time.Now().UnixNano(),
			},
		},
		{
			name:     "empty data",
			data:     []byte{},
			actionID: "eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=",
			meta: &MetaData{
				OutputID: exampleOutputID,
				Size:     0,
				Timenano: time.Now().UnixNano(),
			},
		},
		{
			name:     "large data",
			data:     bytes.Repeat([]byte("a"), 1024*1024*10),
			actionID: "sjZslZ6Zj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=",
			meta: &MetaData{
				OutputID: exampleOutputID,
				Size:     1024 * 1024 * 10,
				Timenano: time.Now().UnixNano(),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s3Inst := newS3Instance(t)
			err := s3Inst.Put(t.Context(), tc.actionID, tc.meta, bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Put failed: %v", err)
			}

			// Verify the operation by using Get.
			var buf bytes.Buffer
			actualMeta, err := s3Inst.Get(t.Context(), tc.actionID, &buf)
			if err != nil {
				t.Fatalf("Get failed: %v", err)
			}

			t.Logf("actualMeta: %+v, meta: %+v", actualMeta, tc.meta)
			if diff := cmp.Diff(tc.meta.Size, actualMeta.Size); diff != "" {
				t.Errorf("Size mismatch: %s", diff)
			}
			if diff := cmp.Diff(tc.meta.OutputID, actualMeta.OutputID); diff != "" {
				t.Errorf("OutputID mismatch: %s", diff)
			}

			if diff := cmp.Diff(tc.meta.Timenano, actualMeta.Timenano); diff != "" {
				t.Errorf("Timenano mismatch: %s", diff)
			}

			if diff := cmp.Diff(tc.data, buf.Bytes()); diff != "" {
				t.Errorf("Data mismatch: %s", diff)
			}
		})
	}
}
