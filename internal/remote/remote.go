package remote

import (
	"context"
	"io"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
)

type Backend interface {
	MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error)
	WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error
	Put(ctx context.Context, objectID string, size int64, r io.ReadSeeker) error
	Close(ctx context.Context) error
}
