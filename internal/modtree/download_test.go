package modtree

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDownloadPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantOK  bool
		wantMod string
		wantVer string
		wantExt string
	}{
		{name: "zip", path: "github.com/!burnt!sushi/toml/@v/v1.2.3.zip", wantOK: true, wantMod: "github.com/!burnt!sushi/toml", wantVer: "v1.2.3", wantExt: "zip"},
		{name: "mod", path: "golang.org/x/sync/@v/v0.23.0.mod", wantOK: true, wantMod: "golang.org/x/sync", wantVer: "v0.23.0", wantExt: "mod"},
		{name: "info with dots in version", path: "example.com/m/@v/v0.0.0-20210101000000-abcdef123456.info", wantOK: true, wantMod: "example.com/m", wantVer: "v0.0.0-20210101000000-abcdef123456", wantExt: "info"},
		// Anything gocica does not cache has no place in the download cache.
		{name: "list", path: "example.com/m/@v/list"},
		{name: "ziphash", path: "example.com/m/@v/v1.0.0.ziphash"},
		{name: "no @v", path: "example.com/m/v1.0.0.zip"},
		{name: "empty module", path: "/@v/v1.0.0.zip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ParseDownloadPath(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Module.EscapedPath != tt.wantMod || got.Module.EscapedVersion != tt.wantVer || got.Extension != tt.wantExt {
				t.Errorf("got %+v, want %s %s %s", got, tt.wantMod, tt.wantVer, tt.wantExt)
			}
		})
	}
}

func TestRestoreDownloadFile(t *testing.T) {
	t.Parallel()

	gomodcache := t.TempDir()
	f, ok := ParseDownloadPath("github.com/!burnt!sushi/toml/@v/v1.2.3.zip")
	if !ok {
		t.Fatal("parse failed")
	}

	if HasDownloadFile(gomodcache, f) {
		t.Fatal("an empty cache reported a file")
	}

	content := bytes.Repeat([]byte("zip"), 100)
	if err := RestoreDownloadFile(gomodcache, f, bytes.NewReader(content)); err != nil {
		t.Fatalf("restore: %v", err)
	}

	want := filepath.Join(gomodcache, "cache", "download", "github.com", "!burnt!sushi", "toml", "@v", "v1.2.3.zip")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Error("restored content does not match")
	}
	if !HasDownloadFile(gomodcache, f) {
		t.Error("HasDownloadFile did not see the restored file")
	}

	// Nothing half-written should be left lying around next to it.
	entries, err := os.ReadDir(filepath.Dir(want))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gocica-dl-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}
