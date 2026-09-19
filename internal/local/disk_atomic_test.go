package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mazrean/gocica/log"
)

// failingWriter stands in for a disk that gives out mid-object.
type failingWriter struct {
	io.WriteCloser
	after int
	n     int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > w.after {
		return 0, errors.New("disk full")
	}

	return w.WriteCloser.Write(p)
}

func TestDisk_PartialWriteIsNotPublished(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	disk, err := NewDisk(log.DefaultLogger, DiskDir(dir))
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}

	const outputID = "truncated-object"
	_, w, err := disk.Put(t.Context(), outputID, 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	wrapped, ok := w.(*WriteCloserWithUnlock)
	if !ok {
		t.Fatalf("Put returned %T, want *WriteCloserWithUnlock", w)
	}
	wrapped.WriteCloser = &failingWriter{WriteCloser: wrapped.WriteCloser, after: 4}

	if _, err := w.Write([]byte("more than four bytes")); err == nil {
		t.Fatal("expected a write error")
	}
	// The caller's deferred Close still runs after a failed copy. Publishing here
	// would hand the compiler a truncated object.
	if err := w.Close(); err == nil {
		t.Error("Close must report the failed write")
	}

	path, err := disk.Get(t.Context(), outputID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if path != "" {
		t.Errorf("a truncated object was published at %s", path)
	}
	assertNoTemporaries(t, dir)
}

func TestDisk_SeesObjectsWrittenByAnotherProcess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Stand in for the module proxy daemon, or an earlier `go` invocation in the
	// same job: without this the whole cache blob is downloaded again every time.
	first, err := NewDisk(log.DefaultLogger, DiskDir(dir))
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	const outputID = "written-earlier"
	_, w, err := first.Put(t.Context(), outputID, 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := w.Write([]byte("compiled output")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := NewDisk(log.DefaultLogger, DiskDir(dir))
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}

	path, err := second.Get(t.Context(), outputID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if path == "" {
		t.Fatal("a fresh process did not find an object already on disk")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "compiled output" {
		t.Errorf("content = %q, want %q", content, "compiled output")
	}
}

func TestDisk_ClearsOnlyAbandonedTemporaries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tmp := filepath.Join(dir, tmpDirName)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	orphan := filepath.Join(tmp, "w-orphan")
	if err := os.WriteFile(orphan, []byte("half an object"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	old := time.Now().Add(-2 * staleTemporaryAge)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// The daemon and one cacheprog per `go` command share this directory, so a
	// file being written right now must survive another process starting up.
	live := filepath.Join(tmp, "w-live")
	if err := os.WriteFile(live, []byte("being written"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := NewDisk(log.DefaultLogger, DiskDir(dir)); err != nil {
		t.Fatalf("new disk: %v", err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("an abandoned temporary file survived, stat err = %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("a live temporary file was deleted: %v", err)
	}
}

func TestDisk_GetMissesAnAbsentObject(t *testing.T) {
	t.Parallel()

	disk, err := NewDisk(log.DefaultLogger, DiskDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}

	path, err := disk.Get(context.Background(), "never-written")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
}

func assertNoTemporaries(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(dir, tmpDirName))
	if err != nil {
		t.Fatalf("read temporary dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary directory holds %d leftovers, want 0", len(entries))
	}
}
