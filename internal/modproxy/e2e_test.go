package modproxy_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mazrean/gocica/internal/modproxy"
	"github.com/mazrean/gocica/log"
)

const (
	testModule  = "example.com/m"
	testVersion = "v1.0.0"
)

// TestGoCommandEndToEnd drives the real go binary through the proxy.
//
// Everything else in this package tests gocica's own view of the protocol. This
// is the one that checks the view that matters: whether the go command accepts
// what we serve, including the checksum it computes over the zip we hand back.
func TestGoCommandEndToEnd(t *testing.T) {
	t.Parallel()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}

	var upstreamHits atomic.Int64
	var upstreamDown atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamDown.Load() {
			http.Error(w, "upstream is down", http.StatusInternalServerError)

			return
		}

		upstreamHits.Add(1)
		serveFixture(t, w, r)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	dir := t.TempDir()
	daemon := startDaemon(t, filepath.Join(dir, "store"), upstreamURL)

	project := filepath.Join(dir, "proj")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	writeFile(t, filepath.Join(project, "go.mod"), fmt.Sprintf("module example.com/app\n\ngo 1.24\n\nrequire %s %s\n", testModule, testVersion))

	modcache := filepath.Join(dir, "modcache")
	env := append(os.Environ(),
		"GOMODCACHE="+modcache,
		"GOFLAGS=-mod=mod -modcacherw",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOPROXY="+daemon.URL()+"|"+upstream.URL,
	)

	// Cold: the module comes from upstream and lands in the cache.
	runGo(t, goBin, project, env, "mod", "download", testModule)
	if got := upstreamHits.Load(); got == 0 {
		t.Fatal("upstream was never asked; the cold run did not go through the proxy")
	}

	// Warm: with upstream refusing everything, a success proves gocica served it.
	cold := upstreamHits.Load()
	upstreamDown.Store(true)
	if err := os.RemoveAll(modcache); err != nil {
		t.Fatalf("clear module cache: %v", err)
	}

	runGo(t, goBin, project, env, "mod", "download", testModule)
	if got := upstreamHits.Load(); got != cold {
		t.Errorf("upstream was asked %d more times on the warm run; it should have been served from the cache", got-cold)
	}

	// With the daemon gone, "|" has to carry the build to the next entry. Bring
	// upstream back so there is something to fall through to.
	upstreamDown.Store(false)
	daemon.Stop()
	if err := os.RemoveAll(modcache); err != nil {
		t.Fatalf("clear module cache: %v", err)
	}

	runGo(t, goBin, project, env, "mod", "download", testModule)
}

func startDaemon(t *testing.T, storeDir string, upstream *url.URL) *modproxy.Daemon {
	t.Helper()

	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatalf("create store dir: %v", err)
	}

	store, err := modproxy.NewStore(modproxy.StoreDir(storeDir))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	cache, err := modproxy.NewCache(t.Context(), log.DefaultLogger, store, nil, nil, modproxy.NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	daemon, err := modproxy.NewDaemon(log.DefaultLogger, modproxy.NewServer(log.DefaultLogger, cache, upstream), modproxy.DaemonConfig{
		StateFile: filepath.Join(storeDir, modproxy.StateFileName),
	})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- daemon.Run(context.WithoutCancel(t.Context())) }()
	t.Cleanup(func() {
		daemon.Stop()
		<-done
	})

	waitForHealthz(t, daemon.URL())

	return daemon
}

func waitForHealthz(t *testing.T, base string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(base + "/-/healthz")
		if err == nil {
			_ = res.Body.Close()

			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("module proxy never became healthy")
}

// serveFixture answers the GOPROXY protocol for a single synthetic module.
func serveFixture(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	goMod := fmt.Sprintf("module %s\n\ngo 1.24\n", testModule)

	switch {
	case strings.HasSuffix(r.URL.Path, "/@v/list"):
		fmt.Fprintln(w, testVersion)
	case strings.HasSuffix(r.URL.Path, "/@v/"+testVersion+".info"):
		fmt.Fprintf(w, "{\"Version\":%q,\"Time\":\"2024-01-01T00:00:00Z\"}\n", testVersion)
	case strings.HasSuffix(r.URL.Path, "/@v/"+testVersion+".mod"):
		fmt.Fprint(w, goMod)
	case strings.HasSuffix(r.URL.Path, "/@v/"+testVersion+".zip"):
		w.Header().Set("Content-Type", "application/zip")
		if _, err := w.Write(moduleZip(t, goMod)); err != nil {
			t.Errorf("write zip: %v", err)
		}
	default:
		http.NotFound(w, r)
	}
}

// moduleZip builds a module zip in the layout the go command expects:
// every path prefixed with "<module>@<version>/".
func moduleZip(t *testing.T, goMod string) []byte {
	t.Helper()

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	prefix := testModule + "@" + testVersion + "/"
	files := map[string]string{
		prefix + "go.mod": goMod,
		prefix + "m.go":   "package m\n\n// Hello is here so the package is not empty.\nfunc Hello() string { return \"hello\" }\n",
	}
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	return buf.Bytes()
}

func runGo(t *testing.T, goBin, dir string, env []string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), goBin, args...)
	cmd.Dir = dir
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
