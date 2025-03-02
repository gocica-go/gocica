package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testUser     = "minioadmin"
	testPassword = "minioadmin"
	testBucket   = "testbucket"
	testRegion   = "us-east-1"
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
		log.DefaultLogger.Errorf("Failed to create Docker pool: %s", err)
		os.Exit(1)
	}

	opts := &dockertest.RunOptions{
		Repository: "minio/minio",
		Tag:        "latest",
		Env: []string{
			fmt.Sprintf("MINIO_ROOT_USER=%s", testUser),
			fmt.Sprintf("MINIO_ROOT_PASSWORD=%s", testPassword),
			fmt.Sprintf("MINIO_REGION=%s", testRegion),
		},
		Cmd: []string{"server", "/data"},
	}
	resource, err = pool.RunWithOptions(opts, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{
			Name: "no",
		}
	})
	if err != nil {
		log.DefaultLogger.Errorf("Failed to start MinIO container: %s", err)
		os.Exit(1)
	}
	defer func() {
		if err := pool.Purge(resource); err != nil {
			log.DefaultLogger.Errorf("Failed to purge MinIO container: %s", err)
		}
	}()

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
		log.DefaultLogger.Errorf("Failed to connect to MinIO: %s", err)
	}

	code := m.Run()

	os.Exit(code)
}

// newS3Instance creates an S3 instance for testing.
func newS3Instance(t *testing.T) *S3 {
	s3Inst, err := NewS3(log.DefaultLogger, endpoint, testRegion, testUser, testPassword, testBucket, false, true)
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
		outputID string
		data     []byte
		size     int64
		isPutErr bool
		isGetErr bool
	}{
		{
			name: "normal data",
			data: []byte("test put method"),
			// base64エンコードされた値
			outputID: "v3BOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s=",
			size:     15,
		},
		{
			name:     "empty data",
			data:     []byte{},
			outputID: "eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=",
			size:     0,
		},
		{
			name:     "large data",
			data:     bytes.Repeat([]byte("a"), 1024*1024*10),
			outputID: "sjZslZ6Zj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=",
			size:     1024 * 1024 * 10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s3Inst := newS3Instance(t)
			err := s3Inst.Put(t.Context(), tc.outputID, tc.size, bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Put failed: %v", err)
			}

			if tc.isPutErr {
				if err == nil {
					t.Fatal("Put should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("Put failed: %v", err)
			}

			// Verify the operation by using Get with new signature.
			var buf bytes.Buffer
			err = s3Inst.Get(t.Context(), tc.outputID, &buf)

			if tc.isGetErr {
				if err == nil {
					t.Fatal("Get should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("Get failed: %v", err)
			}

			if diff := cmp.Diff(tc.data, buf.Bytes()); diff != "" {
				t.Errorf("Data mismatch: %s", diff)
			}
		})
	}
}

// Integration tests for MetaData and WriteMetaData

func TestMetaData(t *testing.T) {
	t.Parallel()

	s3Inst := newS3Instance(t)
	tests := []struct {
		name        string
		setup       func(t *testing.T)
		wantEntries map[string]*v1.IndexEntry
		isErr       bool
	}{
		{
			name: "No metadata object exists",
			setup: func(t *testing.T) {
				// Remove metadata object if it exists.
				_ = s3Inst.client.RemoveObject(context.Background(), testBucket, "r-metadata", minio.RemoveObjectOptions{})
			},
			wantEntries: nil,
		},
		{
			name: "Valid metadata object exists",
			setup: func(t *testing.T) {
				metaMap := map[string]*v1.IndexEntry{
					"eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=": {
						OutputId:   "v3BOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s=",
						Size:       15,
						Timenano:   time.Now().UnixNano(),
						LastUsedAt: timestamppb.New(time.Now().Add(-time.Hour)),
					},
				}
				data, err := proto.Marshal(&v1.IndexEntryMap{Entries: metaMap})
				if err != nil {
					t.Fatalf("proto.Marshal failed: %v", err)
				}

				_, err = s3Inst.client.PutObject(context.Background(), testBucket, s3MetadataObjectName, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
					ContentType: "application/octet-stream",
				})
				if err != nil {
					t.Fatalf("Failed to put metadata object: %v", err)
				}
			},
			wantEntries: map[string]*v1.IndexEntry{
				"eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=": {
					OutputId:   "v3BOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s=",
					Size:       15,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.New(time.Now().Add(-time.Hour)),
				},
			},
		},
		{
			name: "Invalid metadata object",
			setup: func(t *testing.T) {
				_, err := s3Inst.client.PutObject(context.Background(), testBucket, s3MetadataObjectName, bytes.NewReader([]byte("invalid data")), 12, minio.PutObjectOptions{
					ContentType: "application/octet-stream",
				})
				if err != nil {
					t.Fatalf("Failed to put metadata object: %v", err)
				}
			},
			isErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)

			entries, err := s3Inst.MetaData(context.Background())

			if tc.isErr {
				if err == nil {
					t.Error("MetaData should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("MetaData failed: %v", err)
			}

			if diff := cmp.Diff(tc.wantEntries, entries, cmpopts.IgnoreFields(v1.IndexEntry{}, "LastUsedAt", "Timenano"), cmpopts.IgnoreUnexported(v1.IndexEntry{})); diff != "" {
				t.Errorf("MetaData entries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWriteMetaData(t *testing.T) {
	t.Parallel()

	s3Inst := newS3Instance(t)
	tests := []struct {
		name           string
		metaMap        map[string]*v1.IndexEntry
		isAlreadyExist bool
		isErr          bool
	}{
		{
			name: "Write valid metadata",
			metaMap: map[string]*v1.IndexEntry{
				"eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=": {
					OutputId:   "v3BOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s=",
					Size:       15,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.New(time.Now().Add(-time.Hour)),
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.isAlreadyExist {
				metaMap := map[string]*v1.IndexEntry{
					"j8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc=": {
						OutputId:   "sBOdpBuNr7ZbZjRFilEKBebzLA0Tpmt7zl2E/Vk34s=",
						Size:       1000,
						Timenano:   time.Now().UnixNano(),
						LastUsedAt: timestamppb.New(time.Now().Add(-time.Hour)),
					},
				}
				data, err := proto.Marshal(&v1.IndexEntryMap{Entries: metaMap})
				if err != nil {
					t.Fatalf("proto.Marshal failed: %v", err)
				}

				_, err = s3Inst.client.PutObject(context.Background(), testBucket, s3MetadataObjectName, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
					ContentType: "application/octet-stream",
				})
				if err != nil {
					t.Fatalf("Failed to put metadata object: %v", err)
				}
			} else {
				// Remove metadata object if it exists.
				_ = s3Inst.client.RemoveObject(context.Background(), testBucket, s3MetadataObjectName, minio.RemoveObjectOptions{})
			}

			err := s3Inst.WriteMetaData(context.Background(), tc.metaMap)
			if err != nil {
				t.Errorf("WriteMetaData returned error: %v", err)
			}

			if tc.isErr {
				if err == nil {
					t.Error("WriteMetaData should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteMetaData failed: %v", err)
			}

			// Verify the operation by using MetaData.
			obj, err := s3Inst.client.GetObject(context.Background(), testBucket, s3MetadataObjectName, minio.GetObjectOptions{})
			if err != nil {
				t.Fatalf("Failed to get metadata object: %v", err)
			}
			defer obj.Close()

			data, err := io.ReadAll(obj)
			if err != nil {
				t.Fatalf("Failed to read metadata object: %v", err)
			}

			indexEntryMap := v1.IndexEntryMap{}
			if err := proto.Unmarshal(data, &indexEntryMap); err != nil {
				t.Fatalf("Failed to unmarshal metadata: %v", err)
			}

			if diff := cmp.Diff(tc.metaMap, indexEntryMap.Entries, cmpopts.IgnoreUnexported(v1.IndexEntry{}, timestamppb.Timestamp{})); diff != "" {
				t.Errorf("MetaData entries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
