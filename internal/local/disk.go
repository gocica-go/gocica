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
	objectMap       map[string]*objectLocker
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
		objectMap: map[string]*objectLocker{},
	}

	return disk, nil
}

type objectLocker struct {
	l  sync.RWMutex
	ok bool
}

func (d *Disk) Get(_ context.Context, outputID string) (diskPath string, err error) {
	var (
		l  *objectLocker
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

	d.logger.Debugf("read lock waiting outputID=%s", outputID)
	l.l.RLock()
	defer l.l.RUnlock()
	d.logger.Debugf("read lock acquired outputID=%s", outputID)
	if !l.ok {
		return "", nil
	}
	return d.objectFilePath(outputID), nil
}

func (d *Disk) Put(_ context.Context, outputID string, _ int64) (string, OpenerWithUnlock, error) {
	var (
		l  *objectLocker
		ok bool
	)
	func() {
		d.objectMapLocker.Lock()
		defer d.objectMapLocker.Unlock()
		l, ok = d.objectMap[outputID]
		if !ok {
			l = &objectLocker{}
			d.objectMap[outputID] = l
		}
	}()

	outputFilePath := d.objectFilePath(outputID)
	l.l.Lock()
	wrapped := &FileOpenerWithUnlock{
		filePath: outputFilePath,
		l:        l,
	}

	return outputFilePath, wrapped, nil
}

type FileOpenerWithUnlock struct {
	filePath string
	l        *objectLocker
}

func (f *FileOpenerWithUnlock) Open() (io.WriteCloser, error) {
	file, err := os.Create(f.filePath)
	if err != nil {
		f.l.l.Unlock()
		return nil, fmt.Errorf("create output file: %w", err)
	}

	return &WriteCloserWithUnlock{
		WriteCloser: file,
		unlock: sync.OnceFunc(func() {
			f.l.l.Unlock()
			f.l.ok = true
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
