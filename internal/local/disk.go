package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mazrean/gocica/log"
)

type DiskDir string

var _ Backend = &Disk{}

const tmpDirName = "tmp"

type Disk struct {
	logger   log.Logger
	rootPath string
	tmpPath  string

	objectMapLocker sync.RWMutex
	objectMap       map[string]*objectLocker
}

func NewDisk(logger log.Logger, dir DiskDir) (*Disk, error) {
	strDir := string(dir)

	err := os.MkdirAll(strDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("create root directory: %w", err)
	}

	// Objects are written here and renamed into place, so a reader -- including
	// one in another process -- never sees a half-written file. Anything left
	// behind by a process that died mid-write is dropped.
	tmpPath := filepath.Join(strDir, tmpDirName)
	if err := os.MkdirAll(tmpPath, 0755); err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	if err := clearStaleTemporaries(tmpPath); err != nil {
		return nil, fmt.Errorf("clear stale temporary files: %w", err)
	}

	logger.Infof("disk backend initialized.")

	disk := &Disk{
		logger:    logger,
		rootPath:  strDir,
		tmpPath:   tmpPath,
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
		// Nothing in this process has written it, but a previous `go` invocation
		// or the module proxy daemon may have. Since an object only ever appears
		// under its final name by rename, anything present is complete.
		path := d.objectFilePath(outputID)
		if _, statErr := os.Stat(path); statErr != nil {
			return "", nil
		}

		return path, nil
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

// Has reports whether the object is on disk. Objects only ever appear under
// their final name by rename, so anything present is complete.
//
// It deliberately does not take the per-object lock: the caller asking is often
// the one holding it.
func (d *Disk) Has(_ context.Context, outputID string) bool {
	_, err := os.Stat(d.objectFilePath(outputID))

	return err == nil
}

var ErrSizeMismatch = errors.New("size mismatch")

func (d *Disk) Put(_ context.Context, outputID string, _ int64) (string, io.WriteCloser, error) {
	outputFilePath := d.objectFilePath(outputID)

	f, err := os.CreateTemp(d.tmpPath, "w-")
	if err != nil {
		return "", nil, fmt.Errorf("create output file: %w", err)
	}

	d.logger.Debugf("output file created: path=%s", f.Name())
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
		tmpPath:     f.Name(),
		finalPath:   outputFilePath,
		unlock: sync.OnceFunc(func() {
			d.logger.Debugf("lock released outputID=%s", outputID)
			l.l.Unlock()
		}),
		markOK: func() { l.ok = true },
	}

	return outputFilePath, wrapped, nil
}

// WriteCloserWithUnlock publishes the object on Close, and only if every write
// and the rename succeeded.
//
// Publishing regardless would hand the compiler a truncated object: a copy that
// fails mid-way still reaches this Close through the caller's defer.
type WriteCloserWithUnlock struct {
	io.WriteCloser
	tmpPath   string
	finalPath string
	unlock    func()
	markOK    func()
	writeErr  error
}

func (w *WriteCloserWithUnlock) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err != nil {
		w.writeErr = err
	}

	return n, err
}

func (w *WriteCloserWithUnlock) Close() error {
	defer w.unlock()

	closeErr := w.WriteCloser.Close()
	if w.writeErr != nil || closeErr != nil {
		_ = os.Remove(w.tmpPath)

		return errors.Join(w.writeErr, closeErr)
	}

	// Rename is atomic within a directory, so the object becomes visible whole or
	// not at all -- to this process and to any other.
	if err := os.Rename(w.tmpPath, w.finalPath); err != nil {
		_ = os.Remove(w.tmpPath)

		return fmt.Errorf("publish output file: %w", err)
	}

	w.markOK()

	return nil
}

func (d *Disk) objectFilePath(id string) string {
	return filepath.Join(d.rootPath, fmt.Sprintf("o-%s", encodeID(id)))
}

func (d *Disk) Close(context.Context) error {
	return nil
}

func encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}

// staleTemporaryAge is how old a leftover temporary file must be before it is
// assumed abandoned.
//
// Sweeping the directory outright would delete files another process is writing
// right now: the proxy daemon and one cacheprog per `go` command share it.
const staleTemporaryAge = time.Hour

// clearStaleTemporaries removes abandoned temporary files, leaving live ones be.
func clearStaleTemporaries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read temporary directory: %w", err)
	}

	limit := time.Now().Add(-staleTemporaryAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.ModTime().After(limit) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}

	return nil
}
