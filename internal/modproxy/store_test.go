package modproxy

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStore_CommitAndOpen(t *testing.T) {
	t.Parallel()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	content := []byte("module example.com/m\n\ngo 1.27\n")
	w, err := store.NewWriter()
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("write: %v", err)
	}

	id, size, err := store.Commit(w, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if want := ObjectIDForContent(content); id != want {
		t.Errorf("id = %q, want %q", id, want)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if len(id) != 44 {
		t.Errorf("object ID must be 44 characters to satisfy the Azure block ID rule, got %d", len(id))
	}

	f, gotSize, err := store.Open(id)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
	if gotSize != int64(len(content)) {
		t.Errorf("open size = %d, want %d", gotSize, len(content))
	}
}

func TestStore_CommitRejectsHashMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewStore(StoreDir(dir))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	w, err := store.NewWriter()
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := w.Write([]byte("corrupted")); err != nil {
		t.Fatalf("write: %v", err)
	}

	wrong := ObjectIDForContent([]byte("what we asked for"))
	if _, _, err := store.Commit(w, wrong); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("commit err = %v, want ErrHashMismatch", err)
	}

	if store.Has(wrong) {
		t.Error("a mismatching object must not be stored")
	}
	assertTmpEmpty(t, dir)
}

func TestStore_AbortLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewStore(StoreDir(dir))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	w, err := store.NewWriter()
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := w.Write([]byte("half a zip")); err != nil {
		t.Fatalf("write: %v", err)
	}
	store.Abort(w)

	assertTmpEmpty(t, dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "o-") {
			t.Errorf("aborted write became a visible object: %s", e.Name())
		}
	}
}

func TestStore_NewStoreClearsStaleTemporaries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tmp := filepath.Join(dir, tmpDirName)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "w-stale"), []byte("leftover"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := NewStore(StoreDir(dir)); err != nil {
		t.Fatalf("new store: %v", err)
	}
	assertTmpEmpty(t, dir)
}

func TestStore_ConcurrentIdenticalCommits(t *testing.T) {
	t.Parallel()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	content := []byte("the same bytes from every goroutine")
	want := ObjectIDForContent(content)

	var wg sync.WaitGroup
	ids := make([]string, 16)
	for i := range ids {
		wg.Go(func() {

			w, err := store.NewWriter()
			if err != nil {
				t.Errorf("new writer: %v", err)

				return
			}
			if _, err := w.Write(content); err != nil {
				t.Errorf("write: %v", err)

				return
			}
			id, _, err := store.Commit(w, want)
			if err != nil {
				t.Errorf("commit: %v", err)

				return
			}
			ids[i] = id
		})
	}
	wg.Wait()

	for i, id := range ids {
		if id != want {
			t.Errorf("ids[%d] = %q, want %q", i, id, want)
		}
	}
}

func TestStore_OpenMissing(t *testing.T) {
	t.Parallel()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if _, _, err := store.Open(ObjectIDForContent([]byte("absent"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("open err = %v, want os.ErrNotExist", err)
	}
}

func assertTmpEmpty(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(dir, tmpDirName))
	if err != nil {
		t.Fatalf("read temporary dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary directory must be empty, got %d entries", len(entries))
	}
}
