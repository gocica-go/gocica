package modproxy

import "testing"

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		urlPath     string
		wantOK      bool
		wantKind    kind
		wantModule  string
		wantVersion string
	}{
		{
			name:        "zip",
			urlPath:     "/github.com/stretchr/testify/@v/v1.9.0.zip",
			wantOK:      true,
			wantKind:    kindZip,
			wantModule:  "github.com/stretchr/testify",
			wantVersion: "v1.9.0",
		},
		{
			name:        "mod",
			urlPath:     "/golang.org/x/sync/@v/v0.23.0.mod",
			wantOK:      true,
			wantKind:    kindMod,
			wantModule:  "golang.org/x/sync",
			wantVersion: "v0.23.0",
		},
		{
			name:        "escaped uppercase in module path",
			urlPath:     "/github.com/!burnt!sushi/toml/@v/v1.2.3.info",
			wantOK:      true,
			wantKind:    kindInfo,
			wantModule:  "github.com/!burnt!sushi/toml",
			wantVersion: "v1.2.3",
		},
		{
			name:        "pseudo-version",
			urlPath:     "/example.com/m/@v/v0.0.0-20210101000000-abcdef123456.zip",
			wantOK:      true,
			wantKind:    kindZip,
			wantModule:  "example.com/m",
			wantVersion: "v0.0.0-20210101000000-abcdef123456",
		},
		{
			name:        "incompatible build metadata",
			urlPath:     "/example.com/m/@v/v2.0.0+incompatible.zip",
			wantOK:      true,
			wantKind:    kindZip,
			wantModule:  "example.com/m",
			wantVersion: "v2.0.0+incompatible",
		},
		{
			name:        "major version suffix in path",
			urlPath:     "/gopkg.in/yaml.v2/@v/v2.4.0.zip",
			wantOK:      true,
			wantKind:    kindZip,
			wantModule:  "gopkg.in/yaml.v2",
			wantVersion: "v2.4.0",
		},
		// Resolution queries: caching these pins a moving target.
		{name: "branch info", urlPath: "/example.com/m/@v/master.info"},
		{name: "short sha info", urlPath: "/example.com/m/@v/abc1234.info"},
		{name: "major only info", urlPath: "/example.com/m/@v/v1.info"},
		{name: "minor only info", urlPath: "/example.com/m/@v/v1.2.info"},
		{name: "leading zero", urlPath: "/example.com/m/@v/v1.02.3.zip"},
		// Mutable or protocol endpoints.
		{name: "list", urlPath: "/example.com/m/@v/list"},
		{name: "latest", urlPath: "/example.com/m/@latest"},
		{name: "sumdb supported", urlPath: "/sumdb/sum.golang.org/supported"},
		{name: "sumdb lookup", urlPath: "/sumdb/sum.golang.org/lookup/example.com/m@v1.0.0"},
		// Malformed or dangerous.
		{name: "no @v", urlPath: "/example.com/m/v1.0.0.zip"},
		{name: "unknown extension", urlPath: "/example.com/m/@v/v1.0.0.ziphash"},
		{name: "no extension", urlPath: "/example.com/m/@v/v1.0.0"},
		{name: "dot dot element", urlPath: "/example.com/../m/@v/v1.0.0.zip"},
		{name: "double slash", urlPath: "/example.com//m/@v/v1.0.0.zip"},
		{name: "empty module path", urlPath: "/@v/v1.0.0.zip"},
		{name: "unescaped uppercase", urlPath: "/github.com/BurntSushi/toml/@v/v1.2.3.zip"},
		{name: "control character", urlPath: "/example.com/m\x00/@v/v1.0.0.zip"},
		// Deliberately excluded.
		{name: "toolchain", urlPath: "/golang.org/toolchain/@v/v0.0.1-go1.27.1.linux-amd64.zip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := classify(tt.urlPath)
			if ok != tt.wantOK {
				t.Fatalf("classify(%q) ok = %v, want %v", tt.urlPath, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}

			if got.kind != tt.wantKind {
				t.Errorf("kind = %v, want %v", got.kind, tt.wantKind)
			}
			if got.modulePath != tt.wantModule {
				t.Errorf("modulePath = %q, want %q", got.modulePath, tt.wantModule)
			}
			if got.version != tt.wantVersion {
				t.Errorf("version = %q, want %q", got.version, tt.wantVersion)
			}
			if got.path != tt.urlPath[1:] {
				t.Errorf("path = %q, want %q", got.path, tt.urlPath[1:])
			}
		})
	}
}

func TestUnescapeUpper(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"v1.2.3", "v1.2.3"},
		{"!burnt!sushi", "BurntSushi"},
		{"v1.0.0-!r!c1", "v1.0.0-RC1"},
		{"trailing!", "trailing!"}, // a dangling "!" escapes nothing; it fails the canonical check later
	}

	for _, tt := range tests {
		if got := unescapeUpper(tt.in); got != tt.want {
			t.Errorf("unescapeUpper(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
