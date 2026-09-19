package modtree

import (
	"archive/tar"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

var testModule = Module{EscapedPath: "github.com/!burnt!sushi/toml", EscapedVersion: "v1.2.3"}

// extractedModule lays out a module cache the way the go command leaves one.
func extractedModule(t *testing.T, withZiphash, withPartial bool) string {
	t.Helper()

	gomodcache := t.TempDir()
	dir := testModule.Dir(gomodcache)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "deep"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	files := map[string]string{
		"go.mod":  "module github.com/BurntSushi/toml\n",
		"toml.go": "package toml\n",
		filepath.Join("internal", "deep", "x.go"): "package deep\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0444); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	downloadDir := filepath.Join(gomodcache, "cache", "download", "github.com", "!burnt!sushi", "toml", "@v")
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		t.Fatalf("mkdir download: %v", err)
	}
	if withZiphash {
		if err := os.WriteFile(filepath.Join(downloadDir, "v1.2.3.ziphash"), []byte("h1:abcdef=="), 0644); err != nil {
			t.Fatalf("write ziphash: %v", err)
		}
	}
	if withPartial {
		if err := os.WriteFile(filepath.Join(downloadDir, "v1.2.3.partial"), nil, 0644); err != nil {
			t.Fatalf("write partial: %v", err)
		}
	}

	return gomodcache
}

func TestPackUnpackRoundTrip(t *testing.T) {
	t.Parallel()

	source := extractedModule(t, true, false)

	buf := &bytes.Buffer{}
	if err := Pack(source, testModule, buf); err != nil {
		t.Fatalf("pack: %v", err)
	}

	dest := t.TempDir()
	if err := Unpack(dest, testModule, buf); err != nil {
		t.Fatalf("unpack: %v", err)
	}

	// The three things the go command checks before it will trust an extracted
	// module, and the contents it will then compile.
	dir := testModule.Dir(dest)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("module directory missing: %v", err)
	}
	if _, err := os.Stat(testModule.partialPath(dest)); !os.IsNotExist(err) {
		t.Errorf("a .partial marker must not be restored, stat err = %v", err)
	}
	ziphash, err := os.ReadFile(testModule.ziphashPath(dest))
	if err != nil {
		t.Fatalf("read ziphash: %v", err)
	}
	if string(ziphash) != "h1:abcdef==" {
		t.Errorf("ziphash = %q, want %q", ziphash, "h1:abcdef==")
	}

	for name, want := range map[string]string{
		"go.mod":  "module github.com/BurntSushi/toml\n",
		"toml.go": "package toml\n",
		filepath.Join("internal", "deep", "x.go"): "package deep\n",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)

			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestPackRefusesAnInterruptedExtraction(t *testing.T) {
	t.Parallel()

	source := extractedModule(t, true, true)

	// A .partial marker means the go command itself does not trust the tree.
	// Publishing it would hand every later build a module with missing files.
	err := Pack(source, testModule, &bytes.Buffer{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("pack err = %v, want fs.ErrNotExist", err)
	}
}

func TestPackRefusesAMissingZiphash(t *testing.T) {
	t.Parallel()

	source := extractedModule(t, false, false)

	// Without it the go command re-extracts, so a tree packed without one is
	// useless at best.
	if err := Pack(source, testModule, &bytes.Buffer{}); err == nil {
		t.Error("expected an error for a module with no ziphash")
	}
}

func TestUnpackRejectsEscapingPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry string
	}{
		{name: "parent", entry: "../escaped.go"},
		{name: "nested parent", entry: "internal/../../escaped.go"},
		{name: "absolute", entry: "/etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			buf := &bytes.Buffer{}
			tw := tar.NewWriter(buf)
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: ziphashEntry, Mode: 0644, Size: 3}); err != nil {
				t.Fatalf("write header: %v", err)
			}
			if _, err := tw.Write([]byte("h1:")); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: tt.entry, Mode: 0644, Size: 0}); err != nil {
				t.Fatalf("write header: %v", err)
			}
			if err := tw.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			dest := t.TempDir()
			if err := Unpack(dest, testModule, buf); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("unpack err = %v, want ErrUnsafePath", err)
			}
			if _, err := os.Stat(testModule.Dir(dest)); !os.IsNotExist(err) {
				t.Error("a rejected archive must not leave a module directory behind")
			}
		})
	}
}

func TestUnpackRequiresAZiphash(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "toml.go", Mode: 0444, Size: 0}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dest := t.TempDir()
	// Restoring the tree without the hash would leave a directory the go command
	// re-extracts anyway, wasting the transfer.
	if err := Unpack(dest, testModule, buf); err == nil {
		t.Error("expected an error for an archive with no ziphash")
	}
	if _, err := os.Stat(testModule.Dir(dest)); !os.IsNotExist(err) {
		t.Error("a rejected archive must not leave a module directory behind")
	}
}

func TestUnpackReplacesAnExistingTree(t *testing.T) {
	t.Parallel()

	source := extractedModule(t, true, false)
	buf := &bytes.Buffer{}
	if err := Pack(source, testModule, buf); err != nil {
		t.Fatalf("pack: %v", err)
	}

	dest := extractedModule(t, true, false)
	stale := filepath.Join(testModule.Dir(dest), "stale.go")
	if err := os.WriteFile(stale, []byte("package stale\n"), 0644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	if err := Unpack(dest, testModule, buf); err != nil {
		t.Fatalf("unpack: %v", err)
	}

	// A replaced tree must not keep files the archive does not have: the go
	// command hashes the directory, not the files it happens to read.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a file absent from the archive survived the restore, stat err = %v", err)
	}
}

func TestModulePaths(t *testing.T) {
	t.Parallel()

	const root = "/mod"
	if got, want := testModule.Dir(root), filepath.Join(root, "github.com", "!burnt!sushi", "toml@v1.2.3"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	if got, want := testModule.ziphashPath(root), filepath.Join(root, "cache", "download", "github.com", "!burnt!sushi", "toml", "@v", "v1.2.3.ziphash"); got != want {
		t.Errorf("ziphashPath = %q, want %q", got, want)
	}
}
