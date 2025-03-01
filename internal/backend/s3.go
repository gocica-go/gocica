package backend

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Ensure S3 implements RemoteBackend
var _ RemoteBackend = &S3{}

// S3 implements the RemoteBackend interface using MinIO Go Client SDK.
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 initializes a new S3 backend.
// endpoint: S3 endpoint URL
// accessKey: access key for S3
// secretKey: secret key for S3
// bucket: the bucket name to use
// profile: AWS credential profile name (ignored if accessKey and secretKey are provided)
// useSSL: whether to use SSL
func NewS3(endpoint, region, accessKey, secretKey, bucket string, useSSL bool) (*S3, error) {
	var creds *credentials.Credentials
	if accessKey != "" && secretKey != "" {
		creds = credentials.NewStaticV4(accessKey, secretKey, "")
	} else {
		creds = credentials.NewFileAWSCredentials("", "")
	}

	client, err := minio.New(endpoint, &minio.Options{
		Region: region,
		Creds:  creds,
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize S3 client: %w", err)
	}

	// Ensure bucket exists
	ctx := context.Background()
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket %q does not exist", bucket)
	}

	return &S3{
		client: client,
		bucket: bucket,
	}, nil
}

// Get retrieves the object associated with actionID from S3 and writes it to w.
// It also returns the MetaData extracted from object metadata.
func (s *S3) Get(ctx context.Context, actionID string, w io.Writer) (meta *MetaData, err error) {
	opts := minio.GetObjectOptions{}
	obj, err := s.client.GetObject(ctx, s.bucket, s.objectName(actionID), opts)
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer obj.Close()

	// Copy object data to w
	_, err = io.Copy(w, obj)
	if err != nil {
		return nil, fmt.Errorf("copy object: %w", err)
	}

	// Extract metadata
	stat, err := obj.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat object: %w", err)
	}
	md := &MetaData{
		OutputID: stat.UserMetadata["Outputid"],
	}
	if sizeStr, ok := stat.UserMetadata["Size"]; ok {
		if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			md.Size = size
		}
	}
	if timeStr, ok := stat.UserMetadata["Timenano"]; ok {
		if timenano, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
			md.Timenano = timenano
		}
	}
	return md, nil
}

// Put uploads data from r to S3 with the specified actionID and metadata.
func (s *S3) Put(ctx context.Context, actionID string, meta *MetaData, r io.Reader) error {
	opts := minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		UserMetadata: map[string]string{
			"Outputid": meta.OutputID,
			"Size":     strconv.FormatInt(meta.Size, 10),
			"Timenano": strconv.FormatInt(meta.Timenano, 10),
		},
	}
	_, err := s.client.PutObject(ctx, s.bucket, s.objectName(actionID), r, meta.Size, opts)
	if err != nil {
		return fmt.Errorf("upload object: %w", err)
	}
	return nil
}

func (s *S3) objectName(actionID string) string {
	return fmt.Sprintf("a-%s", encodeID(actionID))
}

// Close performs any necessary cleanup. For S3, there are no resources to close.
func (s *S3) Close() error {
	return nil
}
