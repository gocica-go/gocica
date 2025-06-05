package local

import (
	"context"
	"io"
	"strings"
)

type Backend interface {
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Put(ctx context.Context, outputID string, size int64) (diskPath string, w io.WriteCloser, err error)
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}
