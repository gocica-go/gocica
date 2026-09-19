// Package modtree packs and restores the extracted form of a module in the Go
// module cache.
//
// Serving zips over GOPROXY still leaves the go command to unzip them, which on
// a large module set costs more than the transfer does. The go command treats a
// module as already extracted when three things hold (cmd/go/internal/modfetch,
// DownloadDir):
//
//   - $GOMODCACHE/<escaped path>@<escaped version>/ exists as a directory,
//   - cache/download/<escaped path>/@v/<escaped version>.partial does not exist,
//   - cache/download/<escaped path>/@v/<escaped version>.ziphash does exist.
//
// Restoring those directly is what this package is for. Note what it implies:
// the go command then trusts the tree instead of re-hashing a zip against
// go.sum, so the integrity of the cache is gocica's responsibility. That is the
// same bargain actions/setup-go's cache makes, and it is why every restored
// object is verified against its own content hash first.
package modtree

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ziphashEntry is the tar entry carrying the module's h1: hash. It is stored
// inside the archive rather than beside it so a tree and its hash cannot be
// separated.
const ziphashEntry = ".gocica-ziphash"

// ErrUnsafePath is returned for an archive entry that would write outside the
// module directory.
var ErrUnsafePath = errors.New("unsafe path in module tree")

// Module identifies a module by the escaped forms the module cache uses on disk.
type Module struct {
	// EscapedPath is the module path as the GOPROXY protocol spells it, e.g.
	// "github.com/!burnt!sushi/toml".
	EscapedPath string
	// EscapedVersion is the version as the GOPROXY protocol spells it.
	EscapedVersion string
}

// Dir is where the extracted sources live.
func (m Module) Dir(gomodcache string) string {
	return filepath.Join(gomodcache, filepath.FromSlash(m.EscapedPath)+"@"+m.EscapedVersion)
}

// ziphashPath is where the go command looks for the module's h1: hash.
func (m Module) ziphashPath(gomodcache string) string {
	return filepath.Join(gomodcache, "cache", "download",
		filepath.FromSlash(m.EscapedPath), "@v", m.EscapedVersion+".ziphash")
}

// partialPath marks an extraction the go command should not trust.
func (m Module) partialPath(gomodcache string) string {
	return filepath.Join(gomodcache, "cache", "download",
		filepath.FromSlash(m.EscapedPath), "@v", m.EscapedVersion+".partial")
}

// Pack writes the module's extracted tree and its h1: hash to w as a tar
// archive. It reports fs.ErrNotExist when the module is not extracted, or is
// extracted but not trustworthy.
func Pack(gomodcache string, m Module, w io.Writer) error {
	if _, err := os.Stat(m.partialPath(gomodcache)); err == nil {
		return fmt.Errorf("%s: extraction was interrupted: %w", m.EscapedPath, fs.ErrNotExist)
	}

	ziphash, err := os.ReadFile(m.ziphashPath(gomodcache))
	if err != nil {
		return fmt.Errorf("read ziphash: %w", err)
	}

	root := m.Dir(gomodcache)
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("stat module directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory: %w", root, fs.ErrNotExist)
	}

	tw := tar.NewWriter(w)

	if err := writeEntry(tw, ziphashEntry, 0644, int64(len(ziphash)), func(w io.Writer) error {
		_, err := w.Write(ziphash)

		return err
	}); err != nil {
		return fmt.Errorf("write ziphash entry: %w", err)
	}

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("relative path: %w", err)
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}

		switch {
		case d.IsDir():
			return writeHeader(tw, &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name + "/",
				Mode:     int64(info.Mode().Perm()),
			})
		case info.Mode().IsRegular():
			return writeEntry(tw, name, int64(info.Mode().Perm()), info.Size(), func(w io.Writer) error {
				f, err := os.Open(p)
				if err != nil {
					return fmt.Errorf("open %s: %w", p, err)
				}
				defer f.Close()

				if _, err := io.Copy(w, f); err != nil {
					return fmt.Errorf("copy %s: %w", p, err)
				}

				return nil
			})
		default:
			// Module zips carry only regular files and directories, so anything
			// else means the tree has been tampered with locally.
			return fmt.Errorf("%s: unexpected file type %v", p, info.Mode().Type())
		}
	})
	if err != nil {
		return fmt.Errorf("walk module directory: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}

	return nil
}

func writeHeader(tw *tar.Writer, h *tar.Header) error {
	if err := tw.WriteHeader(h); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	return nil
}

func writeEntry(tw *tar.Writer, name string, mode, size int64, write func(io.Writer) error) error {
	if err := writeHeader(tw, &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     mode,
		Size:     size,
	}); err != nil {
		return err
	}

	return write(tw)
}

// Unpack restores a module's tree into the module cache.
//
// The tree is assembled under a temporary name and moved into place in one step,
// so the go command never sees a directory it would trust but that is only half
// written. The ziphash lands last, because it is the thing that makes the go
// command trust the directory at all.
func Unpack(gomodcache string, m Module, r io.Reader) error {
	final := m.Dir(gomodcache)
	if err := os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	staging, err := os.MkdirTemp(filepath.Dir(final), ".gocica-tree-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	var ziphash []byte
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		if header.Name == ziphashEntry {
			ziphash, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("read ziphash entry: %w", err)
			}

			continue
		}

		target, err := safeJoin(staging, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create directory: %w", err)
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, fs.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: unsupported archive entry type %v", header.Name, header.Typeflag)
		}
	}

	if len(ziphash) == 0 {
		return errors.New("archive has no ziphash entry")
	}

	// Directories are left writable, which is what `go build -modcacherw` does.
	// Sealing them the way an ordinary extraction does would only make the tree
	// harder to replace or clean later, and the go command does not look at the
	// modes -- it looks for the directory, the missing .partial and the ziphash.
	if err := os.RemoveAll(final); err != nil {
		return fmt.Errorf("clear existing module directory: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		return fmt.Errorf("move module directory into place: %w", err)
	}

	if err := writeZiphash(m.ziphashPath(gomodcache), ziphash); err != nil {
		return err
	}

	return nil
}

func writeFile(target string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()

		return fmt.Errorf("write file: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}

	return nil
}

func writeZiphash(target string, ziphash []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create ziphash directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".ziphash-")
	if err != nil {
		return fmt.Errorf("create temporary ziphash: %w", err)
	}
	name := tmp.Name()

	if _, err := tmp.Write(ziphash); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)

		return fmt.Errorf("write ziphash: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("close ziphash: %w", err)
	}

	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("move ziphash into place: %w", err)
	}

	return nil
}

// safeJoin refuses an archive entry that would write outside root.
func safeJoin(root, name string) (string, error) {
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, name)
	}

	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, name)
	}

	return filepath.Join(root, filepath.FromSlash(cleaned)), nil
}
