package local

//go:generate go tool mockgen -source=$GOFILE -destination=mock.go -package=local

import (
	"context"
	"io"
	"strings"
)

type Backend interface {
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Lock(ctx context.Context, outputIDs ...string) error
	// Put is used to write an output block to the disk.
	// Note: Lock must be acquired **before calling** this method.
	// Note: The returned WriteCloser must be closed to release the lock.
	Put(ctx context.Context, outputID string, size int64) (diskPath string, w io.WriteCloser, err error)
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}
