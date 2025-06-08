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

func (d *Disk) Lock(_ context.Context, outputIDs ...string) error {
	d.logger.Debugf("lock waiting")
	d.objectMapLocker.Lock()
	defer d.objectMapLocker.Unlock()
	d.logger.Debugf("lock acquired")

	for _, outputID := range outputIDs {
		var l *objectLocker
		var ok bool
		l, ok = d.objectMap[outputID]
		if !ok {
			l = &objectLocker{}
			d.objectMap[outputID] = l
		}
		d.logger.Debugf("lock waiting outputID=%s", outputID)
		l.l.Lock()
		d.logger.Debugf("lock acquired outputID=%s", outputID)
	}

	return nil
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

// Put is used to write an output block to the disk.
// Note: Lock must be acquired **before calling** this method.
func (d *Disk) Put(_ context.Context, outputID string, _ int64) (string, io.WriteCloser, error) {
	outputFilePath := d.objectFilePath(outputID)

	f, err := os.Create(outputFilePath)
	if err != nil {
		return "", nil, fmt.Errorf("create output file: %w", err)
	}
	d.logger.Debugf("output file created: path=%s", outputFilePath)

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
		return "", nil, fmt.Errorf("object not found: outputID=%s", outputID)
	}

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
