package modproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mazrean/gocica/log"
)

func newTestServer(t *testing.T, upstream http.Handler) (*httptest.Server, func() int64) {
	t.Helper()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	var hits atomic.Int64
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		upstream.ServeHTTP(w, r)
	})

	up := httptest.NewServer(counted)
	t.Cleanup(up.Close)

	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	proxy := httptest.NewServer(NewServer(log.DefaultLogger, cache, u))
	t.Cleanup(proxy.Close)

	return proxy, hits.Load
}

func get(t *testing.T, base, path string) (*http.Response, []byte) {
	t.Helper()

	res, err := http.Get(base + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		// A truncated upstream is a legitimate outcome; report it as an empty body.
		return res, nil
	}

	return res, body
}

const zipPath = "/github.com/google/go-cmp/@v/v0.7.0.zip"

func TestServer_MissThenHit(t *testing.T) {
	t.Parallel()

	content := []byte("pretend this is a module zip")
	proxy, hits := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))

	res, body := get(t, proxy.URL, zipPath)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if string(body) != string(content) {
		t.Fatalf("body = %q, want %q", body, content)
	}

	res, body = get(t, proxy.URL, zipPath)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", res.StatusCode)
	}
	if string(body) != string(content) {
		t.Fatalf("second body = %q, want %q", body, content)
	}
	if got := hits(); got != 1 {
		t.Errorf("upstream was asked %d times, want 1", got)
	}
}

func TestServer_UpstreamStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		upstream   int
		wantStatus int
	}{
		// 404 and 410 mean "walk on" to the go command, so they pass through.
		{name: "not found", upstream: http.StatusNotFound, wantStatus: http.StatusNotFound},
		{name: "gone", upstream: http.StatusGone, wantStatus: http.StatusGone},
		// Anything else is our problem; 502 lets the "|" separator carry the build.
		{name: "server error", upstream: http.StatusInternalServerError, wantStatus: http.StatusBadGateway},
		{name: "rate limited", upstream: http.StatusTooManyRequests, wantStatus: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.upstream)
			}))

			res, _ := get(t, proxy.URL, zipPath)
			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestServer_TruncatedUpstreamIsNeverStored(t *testing.T) {
	t.Parallel()

	full := []byte("the complete module zip, all of it, every byte")
	var truncate atomic.Bool
	truncate.Store(true)

	proxy, hits := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(full)))
		if truncate.Load() {
			// Promise the full length, deliver half. This is the shape that would
			// poison the cache: the go command verifies checksums only after it has
			// finished with the proxy list, so a persisted partial object becomes an
			// unrecoverable SECURITY ERROR on every later build.
			_, _ = w.Write(full[:len(full)/2])

			return
		}
		_, _ = w.Write(full)
	}))

	get(t, proxy.URL, zipPath)

	// The next request must go back to upstream, not serve a half object.
	truncate.Store(false)
	res, body := get(t, proxy.URL, zipPath)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if string(body) != string(full) {
		t.Fatalf("body = %q, want the complete object", body)
	}
	if got := hits(); got != 2 {
		t.Errorf("upstream was asked %d times, want 2 (the truncated object must not be cached)", got)
	}
}

func TestServer_ConcurrentMissesShareOneFetch(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	content := []byte("shared object")
	proxy, hits := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write(content)
	}))

	const n = 32
	var wg sync.WaitGroup
	bodies := make([][]byte, n)
	statuses := make([]int, n)
	for i := range n {
		wg.Go(func() {

			res, err := http.Get(proxy.URL + zipPath)
			if err != nil {
				t.Errorf("get: %v", err)

				return
			}
			defer res.Body.Close()

			statuses[i] = res.StatusCode
			bodies[i], _ = io.ReadAll(res.Body)
		})
	}

	close(release)
	wg.Wait()

	for i := range n {
		if statuses[i] != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, statuses[i])
		}
		if string(bodies[i]) != string(content) {
			t.Errorf("request %d: body = %q, want %q", i, bodies[i], content)
		}
	}
	if got := hits(); got != 1 {
		t.Errorf("upstream was asked %d times, want 1", got)
	}
}

func TestServer_UncacheablePathsAre404(t *testing.T) {
	t.Parallel()

	paths := []string{
		// A 200 here would make the go command use us as its checksum database for
		// the rest of the process, with no fallback.
		"/sumdb/sum.golang.org/supported",
		"/sumdb/sum.golang.org/lookup/example.com/m@v1.0.0",
		"/example.com/m/@v/list",
		"/example.com/m/@latest",
		"/example.com/m/@v/master.info",
		"/golang.org/toolchain/@v/v0.0.1-go1.27.1.linux-amd64.zip",
	}

	proxy, hits := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("upstream must never be asked"))
	}))

	for _, path := range paths {
		res, _ := get(t, proxy.URL, path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, res.StatusCode)
		}
	}

	if got := hits(); got != 0 {
		t.Errorf("upstream was asked %d times, want 0", got)
	}
}

func TestServer_Healthz(t *testing.T) {
	t.Parallel()

	proxy, _ := newTestServer(t, http.NotFoundHandler())

	res, body := get(t, proxy.URL, healthzPath)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if want := "{\"ok\":true,\"ready\":false,\"stored\":0}\n"; string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestServer_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	proxy, _ := newTestServer(t, http.NotFoundHandler())

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, proxy.URL+zipPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}

func TestParseUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: "https://proxy.golang.org"},
		{name: "first http entry wins", in: "https://corp.example.com,https://proxy.golang.org,direct", want: "https://corp.example.com"},
		{name: "pipe separated", in: "https://a.example.com|https://b.example.com", want: "https://a.example.com"},
		{name: "direct only", in: "direct", want: ""},
		{name: "off", in: "off", want: ""},
		// A previously exported gocica address must not become our own upstream.
		{name: "skips loopback", in: "http://127.0.0.1:41235|https://proxy.golang.org,direct", want: "https://proxy.golang.org"},
		{name: "skips localhost", in: "http://localhost:41235,direct", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ParseUpstream(tt.in)
			switch {
			case tt.want == "" && got != nil:
				t.Errorf("ParseUpstream(%q) = %v, want nil", tt.in, got)
			case tt.want != "" && got == nil:
				t.Errorf("ParseUpstream(%q) = nil, want %q", tt.in, tt.want)
			case tt.want != "" && got.String() != tt.want:
				t.Errorf("ParseUpstream(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
