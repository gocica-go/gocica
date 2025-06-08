package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/mazrean/gocica/internal/config"
	"github.com/mazrean/gocica/log"
)

var _ Backend = &Disk{}

type Disk struct {
	logger   log.Logger
	rootPath string

	objectMapLocker sync.RWMutex
	objectMap       map[string]<-chan struct{}
}

func NewDisk(logger log.Logger, config *config.Config) (*Disk, error) {
	err := os.MkdirAll(config.Dir, 0755)
	if err != nil {
		return nil, fmt.Errorf("create root directory: %w", err)
	}

	logger.Infof("disk backend initialized.")

	disk := &Disk{
		logger:    logger,
		rootPath:  config.Dir,
		objectMap: map[string]<-chan struct{}{},
	}

	return disk, nil
}

func (d *Disk) Get(ctx context.Context, outputID string) (diskPath string, err error) {
	var (
		l  <-chan struct{}
		ok bool
	)
	func() {
		d.logger.Debugf("read object map lock waiting: outputID=%s", outputID)
		d.objectMapLocker.RLock()
		defer d.objectMapLocker.RUnlock()
		d.logger.Debugf("read object map lock acquired: outputID=%s", outputID)

		l, ok = d.objectMap[outputID]
	}()
	if !ok {
		return "", nil
	}

	select {
	case <-l:
		func() {
			d.objectMapLocker.RLock()
			defer d.objectMapLocker.RUnlock()
			l = d.objectMap[outputID]
		}()
		if l == nil {
			return "", nil
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}

	return d.objectFilePath(outputID), nil
}

func (d *Disk) Put(ctx context.Context, outputID string, _ int64) (string, OpenerWithUnlock, error) {
	var (
		l       <-chan struct{}
		newChan chan struct{}
	)
	func() {
		d.objectMapLocker.Lock()
		defer d.objectMapLocker.Unlock()
		l = d.objectMap[outputID]
		if l == nil {
			newChan = make(chan struct{})
			d.objectMap[outputID] = newChan
		}
	}()
	outputFilePath := d.objectFilePath(outputID)
	if l != nil {
		select {
		case <-l:
			func() {
				d.objectMapLocker.RLock()
				defer d.objectMapLocker.RUnlock()
				l = d.objectMap[outputID]
			}()
			if l != nil {
				return outputFilePath, nil, nil
			}
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}

	wrapped := &FileOpenerWithUnlock{
		filePath: outputFilePath,
		l:        newChan,
		onFailed: func() {
			d.objectMapLocker.Lock()
			defer d.objectMapLocker.Unlock()
			delete(d.objectMap, outputID)
			close(newChan)
		},
	}

	return outputFilePath, wrapped, nil
}

type FileOpenerWithUnlock struct {
	filePath string
	l        chan<- struct{}
	onFailed func()
}

func (f *FileOpenerWithUnlock) Open() (io.WriteCloser, error) {
	file, err := os.Create(f.filePath)
	if err != nil {
		f.onFailed()
		return nil, fmt.Errorf("create output file: %w", err)
	}

	return &WriteCloserWithUnlock{
		WriteCloser: file,
		unlock: sync.OnceFunc(func() {
			close(f.l)
		}),
	}, nil
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
