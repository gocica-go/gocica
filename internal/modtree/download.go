package modtree

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DownloadFile is one file of the go command's download cache:
// $GOMODCACHE/cache/download/<escaped path>/@v/<escaped version>.<ext>.
type DownloadFile struct {
	Module    Module
	Extension string
}

// ParseDownloadPath reads a GOPROXY request path back into the file it caches.
// It accepts exactly what gocica caches: .info, .mod and .zip.
func ParseDownloadPath(requestPath string) (DownloadFile, bool) {
	modulePath, rest, ok := strings.Cut(requestPath, "/@v/")
	if !ok || modulePath == "" {
		return DownloadFile{}, false
	}

	dot := strings.LastIndex(rest, ".")
	if dot <= 0 {
		return DownloadFile{}, false
	}

	version, ext := rest[:dot], rest[dot+1:]
	switch ext {
	case "info", "mod", "zip":
	default:
		return DownloadFile{}, false
	}

	return DownloadFile{
		Module:    Module{EscapedPath: modulePath, EscapedVersion: version},
		Extension: ext,
	}, true
}

// Path is where the go command looks for this file.
func (f DownloadFile) Path(gomodcache string) string {
	return filepath.Join(gomodcache, "cache", "download",
		filepath.FromSlash(f.Module.EscapedPath), "@v", f.Module.EscapedVersion+"."+f.Extension)
}

// RestoreDownloadFile writes one file of the download cache.
//
// Putting the zips here rather than only serving them over HTTP is what makes
// `go mod download` free: it fetches a module's zip unconditionally, and hashes
// it unless the .ziphash is already there, neither of which it does for a file
// it already has.
func RestoreDownloadFile(gomodcache string, f DownloadFile, r io.Reader) error {
	target := f.Path(gomodcache)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".gocica-dl-")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	name := tmp.Name()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)

		return fmt.Errorf("write file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("close file: %w", err)
	}

	// Rename so the go command never sees a partly written zip and trusts it.
	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("move file into place: %w", err)
	}

	return nil
}

// HasDownloadFile reports whether the file is already in the download cache.
func HasDownloadFile(gomodcache string, f DownloadFile) bool {
	_, err := os.Stat(f.Path(gomodcache))

	return err == nil
}
