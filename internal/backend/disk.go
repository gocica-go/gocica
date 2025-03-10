package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/mazrean/gocica/log"
)

const (
	metadataFilePath = "r-metadata"
)

var _ LocalBackend = &Disk{}

type Disk struct {
	logger   log.Logger
	rootPath string

	objectMapLocker sync.RWMutex
	objectMap       map[string]*sync.RWMutex
}

func NewDisk(logger log.Logger, dir string) (*Disk, error) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		return nil, fmt.Errorf("create root directory: %w", err)
	}

	logger.Infof("disk backend initialized.")

	disk := &Disk{
		logger:    logger,
		rootPath:  dir,
		objectMap: map[string]*sync.RWMutex{},
	}

	return disk, nil
}

func (d *Disk) Get(_ context.Context, outputID string) (diskPath string, err error) {
	var (
		l  *sync.RWMutex
		ok bool
	)
	func() {
		d.objectMapLocker.RLock()
		defer d.objectMapLocker.RUnlock()
		l, ok = d.objectMap[outputID]
	}()
	if !ok {
		return "", nil
	}

	l.RLock()
	defer l.RUnlock()
	return d.objectFilePath(outputID), nil
}

var ErrSizeMismatch = errors.New("size mismatch")

func (d *Disk) Put(_ context.Context, outputID string, size int64) (string, io.WriteCloser, error) {
	outputFilePath := d.objectFilePath(outputID)

	var f *os.File
	f, err := os.Create(outputFilePath)
	if err != nil {
		return "", nil, fmt.Errorf("create output file: %w", err)
	}

	var l *sync.RWMutex
	func() {
		d.objectMapLocker.Lock()
		defer d.objectMapLocker.Unlock()
		var ok bool
		l, ok = d.objectMap[outputID]
		if ok {
			l.Lock()
		} else {
			l = &sync.RWMutex{}
			l.Lock()
			d.objectMap[outputID] = l
		}
	}()
	wrapped := &WriteCloserWithUnlock{
		WriteCloser: f,
		unlock:      l.Unlock,
	}

	return outputFilePath, wrapped, nil
}

type WriteCloserWithUnlock struct {
	io.WriteCloser
	unlock func()
}

func (w *WriteCloserWithUnlock) Close() error {
	defer w.unlock()
	return w.WriteCloser.Close()
}

func (d *Disk) objectFilePath(id string) string {
	return filepath.Join(d.rootPath, fmt.Sprintf("o-%s", encodeID(id)))
}

func (d *Disk) Close(context.Context) error {
	return nil
}
