package local

//go:generate go tool mockgen -source=$GOFILE -destination=mock.go -package=local

import (
	"context"
	"io"
	"strings"
)

type Backend interface {
	Get(ctx context.Context, outputID string) (diskPath string, err error)
	Put(ctx context.Context, outputID string, size int64) (diskPath string, opener OpenerWithUnlock, err error)
}

type OpenerWithUnlock interface {
	Open() (io.WriteCloser, error)
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}
