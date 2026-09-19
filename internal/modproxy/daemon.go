package modproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/mazrean/gocica/internal/pkg/json"
	"github.com/mazrean/gocica/log"
)

const (
	// readHeaderTimeout bounds a client that opens a connection and never finishes
	// its request line. The only client is the local go command, so it is generous.
	readHeaderTimeout = 30 * time.Second
	// drainTimeout bounds how long Run waits for in-flight requests at shutdown.
	drainTimeout = 60 * time.Second
	// StateFileName is where serve records how to reach it.
	StateFileName = "proxy.json"
)

// DaemonConfig configures the GOPROXY daemon.
type DaemonConfig struct {
	// Addr is the listen address. Port 0 picks a free one. It must stay on the
	// loopback interface: the go command cannot attach credentials to a plain HTTP
	// proxy, so the only access control available is the network boundary.
	Addr string
	// StateFile records the URL and pid for `gocica proxy-stop`.
	StateFile string
	// MaxLifetime makes an orphaned daemon exit on its own. Zero disables it.
	MaxLifetime time.Duration
}

// State is the contents of the state file.
type State struct {
	PID       int    `json:"pid"`
	URL       string `json:"url"`
	StartedAt string `json:"started_at"`
}

// Daemon owns the listener and the lifecycle of a Server.
type Daemon struct {
	logger    log.Logger
	server    *Server
	listener  net.Listener
	httpSrv   *http.Server
	stateFile string
	lifetime  time.Duration

	stopOnce sync.Once
	stop     chan struct{}
	flushed  chan struct{}
	flushErr error
}

// NewDaemon binds the listen address.
//
// Binding happens here, in the constructor, so that a port conflict fails before
// anything publishes a GOPROXY value pointing at a socket that does not exist.
func NewDaemon(logger log.Logger, server *Server, config DaemonConfig) (*Daemon, error) {
	addr := config.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	daemon := &Daemon{
		logger:    logger,
		server:    server,
		listener:  listener,
		stateFile: config.StateFile,
		lifetime:  config.MaxLifetime,
		stop:      make(chan struct{}),
		flushed:   make(chan struct{}),
	}
	daemon.httpSrv = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	server.SetLifecycle(daemon)

	return daemon, nil
}

// URL is the base URL to put in GOPROXY.
func (d *Daemon) URL() string {
	return "http://" + d.listener.Addr().String()
}

// GOPROXY returns the value to export, keeping the caller's existing setting as
// the fallback. The "|" separator matters: unlike ",", it makes the go command
// fall back on *any* error, so a daemon that dies mid-build never breaks it.
func (d *Daemon) GOPROXY(previous string) string {
	if previous == "" {
		previous = "https://proxy.golang.org,direct"
	}

	return d.URL() + "|" + previous
}

// Stop asks the daemon to flush and exit. It is safe to call repeatedly.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		close(d.stop)
	})
}

// WaitFlush blocks until the cache has been flushed and reports how that went.
//
// The /-/shutdown handler waits on this so the post step learns whether the
// upload actually finished. It cannot simply call http.Server.Shutdown itself:
// that waits for in-flight requests, and the shutdown request is one of them.
func (d *Daemon) WaitFlush(ctx context.Context) error {
	select {
	case <-d.flushed:
		return d.flushErr
	case <-ctx.Done():
		return fmt.Errorf("wait for flush: %w", ctx.Err())
	}
}

// Run serves until Stop, a signal, ctx cancellation or MaxLifetime, then drains
// in-flight requests and flushes the cache to the remote.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.writeState(); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	defer d.removeState()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- d.httpSrv.Serve(d.listener)
	}()

	var lifetime <-chan time.Time
	if d.lifetime > 0 {
		timer := time.NewTimer(d.lifetime)
		defer timer.Stop()
		lifetime = timer.C
	}

	d.logger.Infof("module proxy listening on %s", d.URL())

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	case sig := <-signals:
		d.logger.Infof("received %s. shutting down the module proxy.", sig)
	case <-lifetime:
		d.logger.Warnf("module proxy reached its maximum lifetime. shutting down.")
	case <-ctx.Done():
	case <-d.stop:
	}

	return d.shutdown(context.WithoutCancel(ctx))
}

func (d *Daemon) shutdown(ctx context.Context) error {
	// Flush first, then release anyone waiting on /-/shutdown, and only then
	// drain. Draining first would deadlock against the shutdown request itself,
	// and Cache.Flush refuses to index anything new from here on, so a request
	// that lands during the flush cannot leave the index pointing at bytes that
	// never made it into the blob.
	d.flushErr = d.server.cache.Flush(ctx)
	close(d.flushed)

	drainCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()

	if err := d.httpSrv.Shutdown(drainCtx); err != nil {
		d.logger.Warnf("drain module proxy: %v", err)
	}

	if d.flushErr != nil {
		return fmt.Errorf("flush module cache: %w", d.flushErr)
	}

	return nil
}

func (d *Daemon) writeState() error {
	if d.stateFile == "" {
		return nil
	}

	state := State{
		PID:       os.Getpid(),
		URL:       d.URL(),
		StartedAt: time.Now().Format(time.RFC3339),
	}

	dir := filepath.Dir(d.stateFile)
	f, err := os.CreateTemp(dir, "proxy-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	name := f.Name()

	if err := json.NewEncoder(f).Encode(state); err != nil {
		_ = f.Close()
		_ = os.Remove(name)

		return fmt.Errorf("encode state: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("close temporary state file: %w", err)
	}

	// Rename so a reader never sees a half-written state file.
	if err := os.Rename(name, d.stateFile); err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("rename state file: %w", err)
	}

	return nil
}

func (d *Daemon) removeState() {
	if d.stateFile == "" {
		return
	}

	_ = os.Remove(d.stateFile)
}

// ReadState loads a state file written by a running daemon.
func ReadState(path string) (State, error) {
	f, err := os.Open(path)
	if err != nil {
		return State{}, fmt.Errorf("open state file: %w", err)
	}
	defer f.Close()

	var state State
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode state file: %w", err)
	}

	return state, nil
}

// ParseUpstream picks the upstream proxy out of a GOPROXY value.
//
// gocica can only forward to an HTTP proxy, so "direct" and "off" are left to the
// go command: a miss becomes a 404 and the go command handles the rest of its own
// list itself.
func ParseUpstream(goproxy string) *url.URL {
	if goproxy == "" {
		goproxy = "https://proxy.golang.org,direct"
	}

	for _, entry := range splitProxyList(goproxy) {
		u, err := url.Parse(entry)
		if err != nil {
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		// Never point at ourselves: GOPROXY may already carry a previously
		// exported gocica address.
		if host, _, err := net.SplitHostPort(u.Host); err == nil && isLoopback(host) {
			continue
		}
		if isLoopback(u.Host) {
			continue
		}

		return u
	}

	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func splitProxyList(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == ',' || s[i] == '|' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}

	return append(out, s[start:])
}
