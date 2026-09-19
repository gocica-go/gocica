package local

import (
	"context"
	"io"
)

type Backend interface {
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	// Has reports whether the object is already published, without waiting for a
	// write that may be in flight. It is a hint for skipping work, not a lock.
	Has(ctx context.Context, outputID string) bool
	Put(ctx context.Context, outputID string, size int64) (diskPath string, w io.WriteCloser, err error)
	Close(ctx context.Context) error
}
