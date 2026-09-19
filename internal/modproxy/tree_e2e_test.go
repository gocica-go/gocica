package modproxy_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mazrean/gocica/internal/modproxy"
	"github.com/mazrean/gocica/log"
)

// TestExtractedTreeSurvivesAFreshModuleCache is the claim the extracted-tree
// cache makes: a module restored this way is one the real go command will use
// without unzipping anything, and without asking anyone for the zip.
func TestExtractedTreeSurvivesAFreshModuleCache(t *testing.T) {
	t.Parallel()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}

	var upstreamDown atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamDown.Load() {
			http.Error(w, "upstream is down", http.StatusInternalServerError)

			return
		}
		serveFixture(t, w, r)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	dir := t.TempDir()
	store, err := modproxy.NewStore(modproxy.StoreDir(filepath.Join(dir, "store")))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := modproxy.NewCache(t.Context(), log.DefaultLogger, store, nil, nil, modproxy.NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}
	daemon := startDaemonWith(t, cache, upstreamURL)

	project := filepath.Join(dir, "proj")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	writeFile(t, filepath.Join(project, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire "+testModule+" "+testVersion+"\n")
	writeFile(t, filepath.Join(project, "app.go"), "package app\n\nimport \"example.com/m\"\n\nvar _ = m.Hello\n")

	first := filepath.Join(dir, "modcache-first")
	env := func(modcache string) []string {
		return append(os.Environ(),
			"GOMODCACHE="+modcache,
			"GOFLAGS=-mod=mod -modcacherw",
			"GOSUMDB=off",
			"GOTOOLCHAIN=local",
			"GOPROXY="+daemon.URL()+"|"+upstream.URL,
		)
	}

	// A first build populates the module cache the ordinary way.
	runGo(t, goBin, project, env(first), "mod", "download", testModule)

	cache.PackTrees(t.Context(), first)

	// Now a machine that has never seen this module: nothing extracted, and an
	// upstream that refuses everything. Only the restored tree can carry it.
	second := filepath.Join(dir, "modcache-second")
	cache.RestoreTrees(t.Context(), second)
	upstreamDown.Store(true)

	extracted := filepath.Join(second, testModule+"@"+testVersion)
	if _, err := os.Stat(extracted); err != nil {
		t.Fatalf("the module was not restored in extracted form: %v", err)
	}

	// `go build` needs only the extracted tree.
	runGo(t, goBin, project, env(second), "build", "./...")

	// `go mod download` needs the zip as well -- it fetches one whether or not the
	// module is extracted -- so restoring the download cache is what makes it free
	// rather than merely fast. Upstream is still refusing everything.
	cache.RestoreDownloadCache(t.Context(), second)
	runGo(t, goBin, project, env(second), "mod", "download", testModule)
}

func startDaemonWith(t *testing.T, cache *modproxy.Cache, upstream *url.URL) *modproxy.Daemon {
	t.Helper()

	daemon, err := modproxy.NewDaemon(log.DefaultLogger, modproxy.NewServer(log.DefaultLogger, cache, upstream), modproxy.DaemonConfig{})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- daemon.Run(t.Context()) }()
	t.Cleanup(func() {
		daemon.Stop()
		<-done
	})

	waitForHealthz(t, daemon.URL())

	return daemon
}
