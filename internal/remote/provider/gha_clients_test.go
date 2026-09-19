package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/log"
)

// twirpServer stands in for the GitHub Actions cache API.
type twirpServer struct {
	server *httptest.Server
	calls  map[string]*atomic.Int64
	status map[string]int
	body   map[string]string
}

func newTwirpServer(t *testing.T) *twirpServer {
	t.Helper()

	s := &twirpServer{
		calls:  map[string]*atomic.Int64{},
		status: map[string]int{},
		body:   map[string]string{},
	}
	for _, endpoint := range []string{"CreateCacheEntry", "GetCacheEntryDownloadURL", "FinalizeCacheEntryUpload"} {
		s.calls[endpoint] = &atomic.Int64{}
	}

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if c, ok := s.calls[endpoint]; ok {
			c.Add(1)
		}

		if status, ok := s.status[endpoint]; ok && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("{}"))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.body[endpoint]))
	}))
	t.Cleanup(s.server.Close)

	return s
}

func (s *twirpServer) count(endpoint string) int64 {
	return s.calls[endpoint].Load()
}

func (s *twirpServer) client(t *testing.T) *ghaCacheClient {
	t.Helper()

	client, err := newGitHubCacheClient(t.Context(), log.DefaultLogger, &GHACacheConfig{
		CacheURL: s.server.URL,
		RunnerOS: "Linux",
		Ref:      "refs/heads/main",
		Sha:      "deadbeef",
	})
	if err != nil {
		t.Fatalf("new github cache client: %v", err)
	}

	return client
}

func TestLazyUploadClient_DoesNotReserveUntilSomethingIsUploaded(t *testing.T) {
	t.Parallel()

	server := newTwirpServer(t)
	server.status["CreateCacheEntry"] = http.StatusConflict

	client := &lazyUploadClient{logger: log.DefaultLogger, cacheClient: server.client(t)}

	// Constructing must not touch the remote: reserving the key up front burns it
	// on runs that upload nothing and locks it out of every retry.
	if got := server.count("CreateCacheEntry"); got != 0 {
		t.Fatalf("CreateCacheEntry was called %d times before any upload, want 0", got)
	}

	_, err := client.UploadBlock(t.Context(), "block", myio.NopSeekCloser(strings.NewReader("x")))
	if !errors.Is(err, core.ErrUploadDisabled) {
		t.Fatalf("UploadBlock err = %v, want ErrUploadDisabled", err)
	}
	if got := server.count("CreateCacheEntry"); got != 1 {
		t.Errorf("CreateCacheEntry was called %d times, want 1", got)
	}

	// A refused entry stays refused without asking again.
	if err := client.Commit(t.Context(), nil, 0); !errors.Is(err, core.ErrUploadDisabled) {
		t.Errorf("Commit err = %v, want ErrUploadDisabled", err)
	}
	if got := server.count("CreateCacheEntry"); got != 1 {
		t.Errorf("CreateCacheEntry was called %d times in total, want 1", got)
	}
	if got := server.count("FinalizeCacheEntryUpload"); got != 0 {
		t.Errorf("a disabled client finalized the entry %d times, want 0", got)
	}
}

func TestLazyUploadClient_DegradesOnAFailedReservation(t *testing.T) {
	t.Parallel()

	server := newTwirpServer(t)
	server.status["CreateCacheEntry"] = http.StatusInternalServerError

	client := &lazyUploadClient{logger: log.DefaultLogger, cacheClient: server.client(t)}

	// A remote that will not take our writes must not fail the build.
	if _, err := client.UploadBlock(t.Context(), "block", myio.NopSeekCloser(strings.NewReader("x"))); !errors.Is(err, core.ErrUploadDisabled) {
		t.Errorf("UploadBlock err = %v, want ErrUploadDisabled", err)
	}
	if err := client.UploadBlockFromURL(t.Context(), "block", "http://example.com", 0, 1); !errors.Is(err, core.ErrUploadDisabled) {
		t.Errorf("UploadBlockFromURL err = %v, want ErrUploadDisabled", err)
	}
}

func TestRefreshingDownloadClient_RefreshesAnExpiringURL(t *testing.T) {
	t.Parallel()

	server := newTwirpServer(t)
	server.body["GetCacheEntryDownloadURL"] = `{"ok":true,"signed_download_url":"https://example.blob.core.windows.net/c/b?sig=refreshed"}`

	client, err := newRefreshingDownloadClient(log.DefaultLogger, server.client(t), "https://example.blob.core.windows.net/c/b?sig=original")
	if err != nil {
		t.Fatalf("new refreshing download client: %v", err)
	}

	url, err := client.GetURL(t.Context())
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	if !strings.Contains(url, "sig=original") {
		t.Errorf("url = %q, want the original while it is still fresh", url)
	}
	if got := server.count("GetCacheEntryDownloadURL"); got != 0 {
		t.Errorf("a fresh URL was refreshed %d times, want 0", got)
	}

	// A daemon outlives the URLs GitHub hands out, and an expired one on the
	// base-copy path silently drops the whole previous blob.
	client.fetchedAt = time.Now().Add(-2 * sasRefreshInterval)

	url, err = client.GetURL(t.Context())
	if err != nil {
		t.Fatalf("get url after expiry: %v", err)
	}
	if !strings.Contains(url, "sig=refreshed") {
		t.Errorf("url = %q, want the refreshed one", url)
	}
	if got := server.count("GetCacheEntryDownloadURL"); got != 1 {
		t.Errorf("GetCacheEntryDownloadURL was called %d times, want 1", got)
	}
}

func TestRefreshingDownloadClient_KeepsTheOldURLWhenRefreshFails(t *testing.T) {
	t.Parallel()

	server := newTwirpServer(t)
	server.status["GetCacheEntryDownloadURL"] = http.StatusInternalServerError

	client, err := newRefreshingDownloadClient(log.DefaultLogger, server.client(t), "https://example.blob.core.windows.net/c/b?sig=original")
	if err != nil {
		t.Fatalf("new refreshing download client: %v", err)
	}
	client.fetchedAt = time.Now().Add(-2 * sasRefreshInterval)

	// The old URL may still be valid, and there is nothing better to fall back to.
	url, err := client.GetURL(t.Context())
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	if !strings.Contains(url, "sig=original") {
		t.Errorf("url = %q, want the previous one kept", url)
	}
}
