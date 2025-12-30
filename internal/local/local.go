package local

import (
	"context"
	"io"
)

type Backend interface {
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Put(ctx context.Context, outputID string, size int64) (diskPath string, w io.WriteCloser, err error)
	Close(ctx context.Context) error
}
