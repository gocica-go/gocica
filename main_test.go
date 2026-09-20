package main

import (
	"path/filepath"
	"testing"
)

func TestDefaultGoModCache(t *testing.T) {
	t.Parallel()

	sep := string(filepath.ListSeparator)
	tests := map[string]struct {
		env  map[string]string
		want string
	}{
		"gopath":             {env: map[string]string{"GOPATH": "/gp", "HOME": "/h"}, want: filepath.Join("/gp", "pkg", "mod")},
		"first gopath entry": {env: map[string]string{"GOPATH": "/a" + sep + "/b", "HOME": "/h"}, want: filepath.Join("/a", "pkg", "mod")},
		"home":               {env: map[string]string{"HOME": "/h"}, want: filepath.Join("/h", "go", "pkg", "mod")},
		"empty gopath":       {env: map[string]string{"GOPATH": "", "HOME": "/h"}, want: filepath.Join("/h", "go", "pkg", "mod")},
		"nothing":            {env: map[string]string{}, want: ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := defaultGoModCache(func(k string) string { return tt.env[k] })
			if got != tt.want {
				t.Errorf("defaultGoModCache() = %q, want %q", got, tt.want)
			}
		})
	}
}
