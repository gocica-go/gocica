package modproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	myhttp "github.com/mazrean/gocica/internal/pkg/http"
	"github.com/mazrean/gocica/internal/pkg/metrics"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/singleflight"
)

const (
	healthzPath  = "/-/healthz"
	shutdownPath = "/-/shutdown"
)

var upstreamGauge = metrics.NewGauge("modproxy_upstream_duration")

// Server serves the GOPROXY protocol out of a Cache, falling back to an upstream
// proxy on a miss.
type Server struct {
	logger   log.Logger
	cache    *Cache
	upstream *url.URL
	client   *http.Client

	fetchGroup singleflight.Group
	lifecycle  Lifecycle
}

// Lifecycle is the part of the daemon the shutdown endpoint drives.
type Lifecycle interface {
	Stop()
	WaitFlush(ctx context.Context) error
}

// NewServer builds the proxy handler. A nil upstream turns every miss into a 404,
// which makes the go command fall through to the next GOPROXY entry itself.
func NewServer(logger log.Logger, cache *Cache, upstream *url.URL) *Server {
	return &Server{
		logger:   logger,
		cache:    cache,
		upstream: upstream,
		client:   myhttp.NewClient(),
	}
}

// SetLifecycle installs the daemon driven by POST /-/shutdown.
func (s *Server) SetLifecycle(l Lifecycle) {
	s.lifecycle = l
}

// ServeHTTP implements the GOPROXY protocol.
//
// It is deliberately a bare handler rather than an http.ServeMux: the mux cleans
// request paths, collapsing "//" and resolving "." and ".." with a redirect,
// which would corrupt module paths.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case healthzPath:
		s.handleHealthz(w, r)

		return
	case shutdownPath:
		s.handleShutdown(w, r)

		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	req, ok := classify(r.URL.Path)
	if !ok {
		// 404 is the answer for everything we do not cache, including "@v/list",
		// "@latest" and "/sumdb/...". The go command reads it as fs.ErrNotExist and
		// walks on to the next GOPROXY entry, which is exactly what we want: those
		// endpoints are mutable, and answering the sumdb probe would bind the go
		// command to us as its checksum database with no way back.
		http.NotFound(w, r)

		return
	}

	if f, _, ok := s.cache.Get(r.Context(), req.path); ok {
		defer f.Close()

		w.Header().Set("Content-Type", req.kind.contentType())
		http.ServeContent(w, r, "", time.Time{}, f)

		return
	}

	s.serveUpstream(w, r, req)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"ok\":true,\"stored\":%d}\n", s.cache.Stored())
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if s.lifecycle == nil {
		http.Error(w, "shutdown is not available", http.StatusNotImplemented)

		return
	}

	// Answer only once the cache has actually been published. A post step that
	// returned early would let the job finish -- and the runner kill us -- with
	// the upload still in flight.
	s.lifecycle.Stop()
	err := s.lifecycle.WaitFlush(r.Context())

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "{\"ok\":false,\"error\":%q}\n", err.Error())

		return
	}

	fmt.Fprintf(w, "{\"ok\":true,\"stored\":%d}\n", s.cache.Stored())
}

// serveUpstream fetches a missing object, streaming it to the client and into the
// store at the same time. Concurrent requests for the same path share one fetch.
func (s *Server) serveUpstream(w http.ResponseWriter, r *http.Request, req request) {
	if s.upstream == nil {
		http.NotFound(w, r)

		return
	}

	var leader bool
	res, err, _ := s.fetchGroup.Do(req.path, func() (any, error) {
		leader = true

		return s.fetchAndServe(w, r, req), nil
	})
	if err != nil {
		s.logger.Warnf("fetch %s: %v", req.path, err)
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)

		return
	}

	if leader {
		return
	}

	// A follower: the leader has already written the object (or failed). Serve it
	// from the store, or report the same status the leader saw.
	status, _ := res.(int)
	if f, _, ok := s.cache.Get(r.Context(), req.path); ok {
		defer f.Close()

		w.Header().Set("Content-Type", req.kind.contentType())
		http.ServeContent(w, r, "", time.Time{}, f)

		return
	}

	if status == 0 {
		status = http.StatusBadGateway
	}
	http.Error(w, http.StatusText(status), status)
}

// fetchAndServe performs the upstream request. It returns the status code it
// produced, for the benefit of any follower waiting on the same path.
func (s *Server) fetchAndServe(w http.ResponseWriter, r *http.Request, req request) int {
	target := *s.upstream
	target.Path = joinPath(target.Path, req.path)

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		s.logger.Warnf("create upstream request: %v", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)

		return http.StatusBadGateway
	}

	var res *http.Response
	upstreamGauge.Stopwatch(func() {
		res, err = s.client.Do(upstreamReq)
	}, "fetch")
	if err != nil {
		s.logger.Debugf("upstream %s: %v", req.path, err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)

		return http.StatusBadGateway
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// 404 and 410 are meaningful to the go command: they mean "walk on".
		// Anything else is our problem, not the module's, so report 502 and let the
		// "|" separator in GOPROXY carry the build past us.
		status := res.StatusCode
		if status != http.StatusNotFound && status != http.StatusGone {
			status = http.StatusBadGateway
		}
		http.Error(w, http.StatusText(status), status)

		return status
	}

	writer, err := s.cache.store.NewWriter()
	if err != nil {
		s.logger.Warnf("create object writer: %v", err)
		http.Error(w, "cache write failed", http.StatusBadGateway)

		return http.StatusBadGateway
	}

	contentLength := res.ContentLength
	w.Header().Set("Content-Type", req.kind.contentType())
	if contentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}
	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodHead {
		s.cache.store.Abort(writer)

		return http.StatusOK
	}

	// The client and the store are fed from the same read. If upstream truncates,
	// the go command sees a short body and retries the next proxy, and the partial
	// object never reaches the store.
	n, copyErr := io.Copy(io.MultiWriter(w, writer), res.Body)
	switch {
	case copyErr != nil:
		s.logger.Warnf("stream %s: %v", req.path, copyErr)
		s.cache.store.Abort(writer)

		return http.StatusBadGateway
	case contentLength >= 0 && n != contentLength:
		s.logger.Warnf("stream %s: got %d bytes, want %d", req.path, n, contentLength)
		s.cache.store.Abort(writer)

		return http.StatusBadGateway
	}

	if _, err := s.cache.Store(r.Context(), req.path, writer, req.kind != kindZip); err != nil {
		s.logger.Warnf("store %s: %v", req.path, err)
	}

	return http.StatusOK
}

func joinPath(base, p string) string {
	switch {
	case base == "" || base == "/":
		return "/" + p
	default:
		return strings.TrimSuffix(base, "/") + "/" + p
	}
}
