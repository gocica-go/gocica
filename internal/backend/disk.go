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
	"google.golang.org/protobuf/proto"
)

const (
	metadataFilePath = "r-metadata"
)

var _ LocalBackend = &Disk{}

type Disk struct {
	logger   log.Logger
	rootPath string

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

	logger.Infof("disk backend initialized.")

	disk := &Disk{
		logger:    logger,
		rootPath:  dir,
		objectMap: objectMap,
	}

	return disk, nil
}

func (d *Disk) MetaData(context.Context) (map[string]*v1.IndexEntry, error) {
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

func (d *Disk) WriteMetaData(_ context.Context, metaDataMap map[string]*v1.IndexEntry) error {
	indexEntryMap := &v1.IndexEntryMap{
		Entries: metaDataMap,
	}
	buf, err := proto.Marshal(indexEntryMap)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	err = os.WriteFile(filepath.Join(d.rootPath, metadataFilePath), buf, 0600)
	if err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}

	return nil
}

func (d *Disk) Get(_ context.Context, outputID string) (diskPath string, err error) {
	d.objectMapLocker.RLock()
	defer d.objectMapLocker.RUnlock()

	if _, ok := d.objectMap[outputID]; !ok {
		return "", nil
	}

	return d.objectFilePath(outputID), nil
}

var ErrSizeMismatch = errors.New("size mismatch")

func (d *Disk) Put(_ context.Context, outputID string, size int64, body io.Reader) (string, error) {
	defer func() {
		_, err := io.Copy(io.Discard, body)
		if err != nil {
			d.logger.Warnf("discard body: %v", err)
		}
	}()
	outputFilePath := d.objectFilePath(outputID)

	var ok bool
	func() {
		d.objectMapLocker.RLock()
		defer d.objectMapLocker.RUnlock()
		_, ok = d.objectMap[outputID]
	}()
	if ok {
		return "", nil
	}

	var f *os.File
	f, err := os.Create(outputFilePath)
	if err != nil {
		return "", fmt.Errorf("create output file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close output file: %w", closeErr))
		}
	}()

	if size != 0 {
		n, err := io.Copy(f, body)
		if err != nil {
			return "", fmt.Errorf("write output file: %w", err)
		}

		if n != size {
			return "", ErrSizeMismatch
		}
	}

	d.objectMapLocker.Lock()
	defer d.objectMapLocker.Unlock()
	d.objectMap[outputID] = struct{}{}

	return outputFilePath, nil
}

func (d *Disk) objectFilePath(id string) string {
	return filepath.Join(d.rootPath, fmt.Sprintf("o-%s", encodeID(id)))
}

func (d *Disk) Close(context.Context) error {
	return nil
}
