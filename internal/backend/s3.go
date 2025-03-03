package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
)

const (
	s3MetadataObjectName = "r-metadata"
)

// Ensure S3 implements RemoteBackend
var _ RemoteBackend = &S3{}

// S3 implements the RemoteBackend interface using MinIO Go Client SDK.
type S3 struct {
	logger log.Logger
	client *minio.Client
	bucket string

	sf              singleflight.Group
	objectMapLocker sync.RWMutex
	objectMap       map[string]struct{}
}

// NewS3 initializes a new S3 backend.
// endpoint: S3 endpoint URL
// accessKey: access key for S3
// secretKey: secret key for S3
// bucket: the bucket name to use
// profile: AWS credential profile name (ignored if accessKey and secretKey are provided)
// useSSL: whether to use SSL
// usePathStyle: whether to force path style
func NewS3(
	logger log.Logger,
	endpoint, region, accessKey, secretKey, bucket string,
	useSSL, usePathStyle bool,
) (*S3, error) {
	var creds *credentials.Credentials
	if accessKey != "" && secretKey != "" {
		creds = credentials.NewStaticV4(accessKey, secretKey, "")
	} else {
		creds = credentials.NewFileAWSCredentials("", "")
	}

	bucketLookupType := minio.BucketLookupDNS
	if usePathStyle {
		bucketLookupType = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, &minio.Options{
		Region:       region,
		Creds:        creds,
		Secure:       useSSL,
		BucketLookup: bucketLookupType,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize S3 client: %w", err)
	}

	logger.Infof("S3 backend initialized with bucket %q", bucket)

	return &S3{
		logger:    logger,
		client:    client,
		bucket:    bucket,
		objectMap: make(map[string]struct{}),
	}, nil
}

func (s *S3) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s3MetadataObjectName, minio.GetObjectOptions{})
	if err != nil {
		minioErr := minio.ToErrorResponse(err)
		if minioErr.Code == "NoSuchKey" {
			return nil, nil
		}
		return nil, fmt.Errorf("get metadata object: %w", err)
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		minioErr := minio.ToErrorResponse(err)
		if minioErr.Code == "NoSuchKey" {
			return nil, nil
		}
		return nil, fmt.Errorf("read metadata object: %w", err)
	}

	indexEntryMap := v1.IndexEntryMap{}
	if err := proto.Unmarshal(data, &indexEntryMap); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}

	return indexEntryMap.Entries, nil
}

func (s *S3) WriteMetaData(ctx context.Context, metaDataMapBuf []byte) error {
	opts := minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	}
	_, err := s.client.PutObject(ctx, s.bucket, s3MetadataObjectName, bytes.NewReader(metaDataMapBuf), int64(len(metaDataMapBuf)), opts)
	if err != nil {
		return fmt.Errorf("put metadata object: %w", err)
	}

	return nil
}

func (s *S3) Get(ctx context.Context, outputID string, w io.Writer) error {
	opts := minio.GetObjectOptions{}
	obj, err := s.client.GetObject(ctx, s.bucket, s.objectName(outputID), opts)
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	defer obj.Close()

	func() {
		s.objectMapLocker.Lock()
		defer s.objectMapLocker.Unlock()
		s.objectMap[outputID] = struct{}{}
	}()

	_, err = io.Copy(w, obj)
	if err != nil {
		return fmt.Errorf("copy object: %w", err)
	}
	return nil
}

func (s *S3) Put(ctx context.Context, outputID string, size int64, r io.Reader) error {
	defer func() {
		_, err := io.Copy(io.Discard, r)
		if err != nil {
			s.logger.Warnf("discard body: %v", err)
		}
	}()

	_, err, _ := s.sf.Do(outputID, func() (any, error) {
		var ok bool
		func() {
			s.objectMapLocker.RLock()
			defer s.objectMapLocker.RUnlock()
			_, ok = s.objectMap[outputID]
		}()
		if ok {
			return nil, nil
		}

		opts := minio.PutObjectOptions{
			ContentType: "application/octet-stream",
		}
		_, err := s.client.PutObject(ctx, s.bucket, s.objectName(outputID), r, size, opts)
		if err != nil {
			return nil, fmt.Errorf("upload object: %w", err)
		}

		func() {
			s.objectMapLocker.Lock()
			defer s.objectMapLocker.Unlock()
			s.objectMap[outputID] = struct{}{}
		}()

		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("do singleflight: %w", err)
	}

	return nil
}

func (s *S3) objectName(outputID string) string {
	return fmt.Sprintf("o-%s", encodeID(outputID))
}

func (s *S3) Close() error {
	return nil
}
