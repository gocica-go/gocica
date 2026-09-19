// Package modproxy implements a caching Go module proxy (GOPROXY) backed by the
// same remote cache as the build cache.
package modproxy

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrHashMismatch is returned when committed bytes do not hash to the expected
// object ID. It is never fatal: the caller discards the object and refetches.
var ErrHashMismatch = errors.New("object hash mismatch")

const tmpDirName = "tmp"

// StoreDir is the directory the module objects live in. It is a named type so
// dependency injection can tell it apart from the build cache directory.
type StoreDir string

// Store is a content-addressed blob store.
//
// Objects are written to a temporary file and only ever linked into place under
// their content hash, so a reader sees a whole object or nothing at all. That
// matters more here than for the build cache: the go command verifies module
// checksums *after* it has finished walking the GOPROXY list, so a truncated zip
// that is served with a matching Content-Length is an unrecoverable
// "SECURITY ERROR", not a fallback.
type Store struct {
	root string
	tmp  string
}

// NewStore creates a store rooted at dir, clearing any temporary files left
// behind by a previous process.
func NewStore(dir StoreDir) (*Store, error) {
	root := string(dir)
	tmp := filepath.Join(root, tmpDirName)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	// Only abandoned files: another gocica process may be writing here now.
	if err := clearStaleTemporaries(tmp); err != nil {
		return nil, fmt.Errorf("clear stale temporary files: %w", err)
	}

	return &Store{root: root, tmp: tmp}, nil
}

// Writer accumulates an object's bytes and its hash.
type Writer struct {
	f    *os.File
	hash hash.Hash
	size int64
}

// NewWriter opens a temporary file in the store's directory. The caller must
// finish with either Commit or Abort.
func (s *Store) NewWriter() (*Writer, error) {
	f, err := os.CreateTemp(s.tmp, "w-")
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}

	return &Writer{f: f, hash: sha256.New()}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.size += int64(n)
	if n > 0 {
		// hash.Hash never returns an error.
		_, _ = w.hash.Write(p[:n])
	}
	if err != nil {
		return n, fmt.Errorf("write temporary file: %w", err)
	}

	return n, nil
}

// Size reports how many bytes have been written so far.
func (w *Writer) Size() int64 {
	return w.size
}

// ObjectID reports the ID the bytes written so far would be stored under.
func (w *Writer) ObjectID() string {
	return encodeObjectID(w.hash.Sum(nil))
}

// Commit links the written bytes into the store under their content hash and
// returns that ID. When expect is non-empty and does not match, the object is
// discarded and ErrHashMismatch is returned.
func (s *Store) Commit(w *Writer, expect string) (string, int64, error) {
	id := w.ObjectID()
	if expect != "" && expect != id {
		s.Abort(w)

		return "", 0, fmt.Errorf("%w: want %s, got %s", ErrHashMismatch, expect, id)
	}

	if err := w.f.Close(); err != nil {
		_ = os.Remove(w.f.Name())

		return "", 0, fmt.Errorf("close temporary file: %w", err)
	}

	// Rename within a directory is atomic, so a concurrent reader sees either the
	// previous complete object or this one, never a partial file.
	if err := os.Rename(w.f.Name(), s.Path(id)); err != nil {
		_ = os.Remove(w.f.Name())

		return "", 0, fmt.Errorf("rename temporary file: %w", err)
	}

	return id, w.size, nil
}

// Abort discards a writer's temporary file.
func (s *Store) Abort(w *Writer) {
	_ = w.f.Close()
	_ = os.Remove(w.f.Name())
}

// Open returns the stored object and its size, or os.ErrNotExist.
func (s *Store) Open(id string) (*os.File, int64, error) {
	f, err := os.Open(s.Path(id))
	if err != nil {
		return nil, 0, fmt.Errorf("open object: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, 0, fmt.Errorf("stat object: %w", err)
	}

	return f, info.Size(), nil
}

// Has reports whether the object is present.
func (s *Store) Has(id string) bool {
	_, err := os.Stat(s.Path(id))

	return err == nil
}

// Path returns the on-disk path of an object.
func (s *Store) Path(id string) string {
	return filepath.Join(s.root, "o-"+strings.ReplaceAll(id, "/", "-"))
}

// encodeObjectID renders a digest the same way the Go toolchain renders its cache
// IDs: standard base64, 44 characters. Object IDs double as Azure block IDs, and
// every block ID in one blob has to be the same length.
func encodeObjectID(sum []byte) string {
	return base64.StdEncoding.EncodeToString(sum)
}

// ObjectIDForContent returns the object ID the given bytes would be stored under.
func ObjectIDForContent(b []byte) string {
	sum := sha256.Sum256(b)

	return encodeObjectID(sum[:])
}

var _ io.Writer = (*Writer)(nil)

// staleTemporaryAge is how old a leftover temporary file must be before it is
// assumed abandoned.
//
// Sweeping the directory outright would delete files another gocica process is
// writing right now.
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
