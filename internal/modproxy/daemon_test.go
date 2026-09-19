package modproxy

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mazrean/gocica/log"
)

func newTestDaemon(t *testing.T, config DaemonConfig) *Daemon {
	t.Helper()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	daemon, err := NewDaemon(log.DefaultLogger, NewServer(log.DefaultLogger, cache, nil), config)
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}

	return daemon
}

func TestDaemon_BindsLoopbackAndWritesState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stateFile := filepath.Join(dir, StateFileName)
	daemon := newTestDaemon(t, DaemonConfig{StateFile: stateFile})

	host, _, err := net.SplitHostPort(daemon.listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("listening on %s; the proxy has no way to authenticate callers, so it must stay on loopback", host)
	}

	done := make(chan error, 1)
	go func() { done <- daemon.Run(t.Context()) }()

	waitFor(t, func() bool {
		_, err := os.Stat(stateFile)

		return err == nil
	}, "state file")

	state, err := ReadState(stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.URL != daemon.URL() {
		t.Errorf("state URL = %q, want %q", state.URL, daemon.URL())
	}
	if state.PID != os.Getpid() {
		t.Errorf("state PID = %d, want %d", state.PID, os.Getpid())
	}

	res, err := http.Get(daemon.URL() + healthzPath)
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	_ = res.Body.Close()

	// Stop is idempotent; a signal and an explicit stop can race.
	daemon.Stop()
	daemon.Stop()

	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Errorf("state file must be removed on exit, stat err = %v", err)
	}
}

func TestDaemon_MaxLifetimeStops(t *testing.T) {
	t.Parallel()

	daemon := newTestDaemon(t, DaemonConfig{MaxLifetime: 50 * time.Millisecond})

	done := make(chan error, 1)
	go func() { done <- daemon.Run(t.Context()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon outlived its maximum lifetime")
	}
}

func TestDaemon_GOPROXY(t *testing.T) {
	t.Parallel()

	daemon := newTestDaemon(t, DaemonConfig{})

	tests := []struct {
		name     string
		previous string
		want     string
	}{
		{name: "empty falls back to the default list", previous: "", want: daemon.URL() + "|https://proxy.golang.org,direct"},
		{name: "existing value is kept as the fallback", previous: "https://corp.example.com,direct", want: daemon.URL() + "|https://corp.example.com,direct"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := daemon.GOPROXY(tt.previous)
			if got != tt.want {
				t.Errorf("GOPROXY(%q) = %q, want %q", tt.previous, got, tt.want)
			}
			// "|" and not ",": the go command must fall back on any error, not just
			// 404 and 410, or a daemon that dies mid-build breaks it.
			if got[len(daemon.URL())] != '|' {
				t.Errorf("GOPROXY must separate the daemon from the fallback with '|', got %q", got)
			}
		})
	}
}

func TestDaemon_BindFailureIsReported(t *testing.T) {
	t.Parallel()

	// Take a port, then ask the daemon for the same one. Binding has to fail in
	// the constructor, before anything publishes a GOPROXY pointing at it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	if _, err := NewDaemon(log.DefaultLogger, NewServer(log.DefaultLogger, cache, nil), DaemonConfig{
		Addr: listener.Addr().String(),
	}); err == nil {
		t.Error("expected a bind error, got nil")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}
