package backend

import (
	"context"
	"io"
)

type Backend interface {
	Get(ctx context.Context, actionID string) (diskPath string, meta *MetaData, err error)
	Put(ctx context.Context, actionID, outputID string, size int64, body io.Reader) (diskPath string, err error)
	Close() error
}

type MetaData struct {
	// OutputID is the unique identifier for the object.
	OutputID string
	// Size is the size of the object in bytes.
	Size int64
	// Timenano is the time the object was created in Unix nanoseconds.
	Timenano int64
}
