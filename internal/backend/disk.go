package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
)

const (
	metadataFilePath = "r-metadata"
)

var _ Backend = &Disk{}

type Disk struct {
	logger          log.Logger
	rootPath        string
	objectMapLocker sync.RWMutex
	objectMap       map[string]struct{}
}

func NewDisk(logger log.Logger, dir string) (*Disk, error) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		return nil, fmt.Errorf("create root directory: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read root directory: %w", err)
	}

	objectMap := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, "o-") {
			objectMap[decodeID(strings.TrimPrefix(name, "o-"))] = struct{}{}
		}
	}

	disk := &Disk{
		logger:    logger,
		rootPath:  dir,
		objectMap: objectMap,
	}

	return disk, nil
}

func (d *Disk) MetaData(ctx context.Context) (map[string]*v1.IndexEntry, error) {
	buf, err := os.ReadFile(filepath.Join(d.rootPath, metadataFilePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read metadata file: %w", err)
	}

	indexEntryMap := &v1.IndexEntryMap{}
	if err := proto.Unmarshal(buf, indexEntryMap); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}

	return indexEntryMap.Entries, nil
}

func (d *Disk) WriteMetaData(ctx context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	indexEntryMap := &v1.IndexEntryMap{
		Entries: metaDataMap,
	}
	buf, err := proto.Marshal(indexEntryMap)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	err = os.WriteFile(filepath.Join(d.rootPath, metadataFilePath), buf, 0644)
	if err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}

	return nil
}

func (d *Disk) Get(ctx context.Context, outputID string) (diskPath string, err error) {
	d.objectMapLocker.RLock()
	defer d.objectMapLocker.RUnlock()

	if _, ok := d.objectMap[outputID]; !ok {
		return "", nil
	}

	return d.objectFilePath(outputID), nil
}

var sf = &singleflight.Group{}

func (d *Disk) Put(ctx context.Context, outputID string, size int64, body io.Reader) (diskPath string, err error) {
	outputFilePath := d.objectFilePath(outputID)

	_, err, _ = sf.Do(outputID, func() (interface{}, error) {
		f, err := os.Create(outputFilePath)
		if err != nil {
			return nil, fmt.Errorf("create output file: %w", err)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close output file: %w", closeErr))
			}
		}()

		if size != 0 {
			_, err = io.Copy(f, body)
			if err != nil {
				return nil, fmt.Errorf("write output file: %w", err)
			}
		}

		return nil, nil
	})
	if err != nil {
		return "", fmt.Errorf("do singleflight: %w", err)
	}

	d.objectMapLocker.Lock()
	defer d.objectMapLocker.Unlock()
	d.objectMap[outputID] = struct{}{}

	return outputFilePath, nil
}

func (d *Disk) objectFilePath(id string) string {
	return filepath.Join(d.rootPath, fmt.Sprintf("o-%s", encodeID(id)))
}

func (d *Disk) Close() error {
	return nil
}
