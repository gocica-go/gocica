package modproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mazrean/gocica/log"
)

type fakeLifecycle struct {
	stopped  atomic.Bool
	ready    atomic.Bool
	release  chan struct{}
	flushErr error
}

func (f *fakeLifecycle) Stop() { f.stopped.Store(true) }

func (f *fakeLifecycle) Ready() bool { return f.ready.Load() }

func (f *fakeLifecycle) WaitFlush(ctx context.Context) error {
	select {
	case <-f.release:
		return f.flushErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newShutdownServer(t *testing.T, life Lifecycle) *httptest.Server {
	t.Helper()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	server := NewServer(log.DefaultLogger, cache, nil)
	server.SetLifecycle(life)

	proxy := httptest.NewServer(server)
	t.Cleanup(proxy.Close)

	return proxy
}

func TestServer_ShutdownWaitsForTheFlush(t *testing.T) {
	t.Parallel()

	life := &fakeLifecycle{release: make(chan struct{})}
	proxy := newShutdownServer(t, life)

	done := make(chan int, 1)
	go func() {
		res, err := http.Post(proxy.URL+shutdownPath, "", nil)
		if err != nil {
			t.Errorf("post shutdown: %v", err)
			done <- 0

			return
		}
		defer res.Body.Close()

		done <- res.StatusCode
	}()

	// The whole point: a post step that got its answer before the upload finished
	// would let the job end, and the runner would kill the daemon mid-flush.
	select {
	case <-done:
		t.Fatal("shutdown answered before the flush completed")
	case <-time.After(200 * time.Millisecond):
	}

	if !life.stopped.Load() {
		t.Error("shutdown did not ask the daemon to stop")
	}

	close(life.release)

	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Errorf("status = %d, want 200", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown never answered")
	}
}

func TestServer_ShutdownReportsAFlushFailure(t *testing.T) {
	t.Parallel()

	life := &fakeLifecycle{release: make(chan struct{}), flushErr: errors.New("commit failed")}
	close(life.release)

	proxy := newShutdownServer(t, life)

	res, err := http.Post(proxy.URL+shutdownPath, "", nil)
	if err != nil {
		t.Fatalf("post shutdown: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "commit failed") {
		t.Errorf("body = %q, want it to carry the flush error", body)
	}
}

func TestServer_ShutdownRejectsGet(t *testing.T) {
	t.Parallel()

	proxy := newShutdownServer(t, &fakeLifecycle{release: make(chan struct{})})

	res, err := http.Get(proxy.URL + shutdownPath)
	if err != nil {
		t.Fatalf("get shutdown: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}

func TestCache_StoreAfterFlushIsNotIndexed(t *testing.T) {
	t.Parallel()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	if err := cache.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	w, err := store.NewWriter()
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := w.Write([]byte("arrived too late")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := cache.Store(t.Context(), "example.com/m/@v/v1.0.0.zip", w, true); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Indexing it would publish an entry whose bytes never reached the blob.
	if _, ok := cache.entries["example.com/m/@v/v1.0.0.zip"]; ok {
		t.Error("an object stored after the flush must not enter the index")
	}
}

func TestServer_HealthzReportsReadiness(t *testing.T) {
	t.Parallel()

	life := &fakeLifecycle{release: make(chan struct{})}
	proxy := newShutdownServer(t, life)

	// The caller needs the address as soon as the socket is up, but must not
	// start the go command until the module cache is warm: restoring extracted
	// modules while the go command extracts into the same directories is a race.
	_, body := get(t, proxy.URL, healthzPath)
	if !strings.Contains(string(body), `"ready":false`) {
		t.Errorf("body = %q, want ready false before the caches are warm", body)
	}

	life.ready.Store(true)

	_, body = get(t, proxy.URL, healthzPath)
	if !strings.Contains(string(body), `"ready":true`) {
		t.Errorf("body = %q, want ready true once the caches are warm", body)
	}
}
