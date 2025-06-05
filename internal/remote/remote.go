package remote

import (
	"context"
	"io"
)

type MetaData struct {
	OutputID string
	Size     int64
	Timenano int64
}

type Backend interface {
	// MetaData returns the metadata of the object.
	// If the object is not found, it returns nil.
	MetaData(ctx context.Context, actionID string) (*MetaData, error)
	// Put uploads the object to the backend.
	// If the object already exists, it overwrites the object.
	Put(ctx context.Context, actionID, objectID string, size int64, r io.ReadSeeker) error
}
