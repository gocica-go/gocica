package local

import (
	"context"
	"errors"
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
		d.objectMapLocker.RLock()
		defer d.objectMapLocker.RUnlock()
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

var ErrSizeMismatch = errors.New("size mismatch")

func (d *Disk) Put(_ context.Context, outputID string, _ int64) (string, io.WriteCloser, error) {
	outputFilePath := d.objectFilePath(outputID)

	var f *os.File
	f, err := os.Create(outputFilePath)
	if err != nil {
		return "", nil, fmt.Errorf("create output file: %w", err)
	}

	d.logger.Debugf("output file created: path=%s", outputFilePath)
	var l *objectLocker
	func() {
		d.objectMapLocker.Lock()
		defer d.objectMapLocker.Unlock()
		var ok bool
		l, ok = d.objectMap[outputID]
		if !ok {
			l = &objectLocker{}
			d.objectMap[outputID] = l
		}
	}()
	d.logger.Debugf("write lock waiting outputID=%s", outputID)
	l.l.Lock()
	d.logger.Debugf("write lock acquired outputID=%s", outputID)
	wrapped := &WriteCloserWithUnlock{
		WriteCloser: f,
		unlock: sync.OnceFunc(func() {
			d.logger.Debugf("lock released outputID=%s", outputID)
			l.ok = true
			l.l.Unlock()
		}),
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
