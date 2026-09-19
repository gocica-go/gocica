package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	"github.com/mazrean/gocica/internal/kessoku"
	"github.com/mazrean/gocica/internal/local"
	"github.com/mazrean/gocica/internal/modproxy"
	mylog "github.com/mazrean/gocica/internal/pkg/log"
	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/internal/remote/provider"
	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
)

//go:generate go tool buf generate

var (
	version  = "dev"
	revision = "none"
)

// cacheProgCmd runs the GOCACHEPROG protocol over stdin/stdout.
//
// It is the default command: `gocica --dir=...` keeps working for everyone who
// already has GOCACHEPROG pointing at the bare binary.
type cacheProgCmd struct{}

// serveCmd runs the GOPROXY daemon.
type serveCmd struct {
	Addr            string        `kong:"default='127.0.0.1:0',help='Listen address. Must stay on loopback: the go command cannot authenticate to a plain HTTP proxy.',env='GOCICA_MODULE_PROXY_ADDR'"`
	Upstream        string        `kong:"help='Upstream proxy to fetch misses from. Defaults to the http(s) entries of the ambient GOPROXY.',env='GOCICA_MODULE_PROXY_UPSTREAM'"`
	MaxLifetime     time.Duration `kong:"default='6h',help='Exit after this long, so an orphaned daemon cannot outlive its job.',env='GOCICA_MODULE_PROXY_MAX_LIFETIME'"`
	ExportGithubEnv bool          `kong:"name='export-github-env',help='Append GOPROXY to $GITHUB_ENV once listening.',env='GOCICA_MODULE_PROXY_EXPORT_GITHUB_ENV'"`
	PrewarmBuild    bool          `kong:"name='prewarm-build-cache',default='true',negatable,help='Also pull the build cache onto local disk while the daemon is idle, so the first go command does not pay for it.',env='GOCICA_MODULE_PROXY_PREWARM_BUILD_CACHE'"`
	StateFile       string        `kong:"help='Where to record the URL and pid. Defaults to <dir>/mod/proxy.json.',env='GOCICA_MODULE_PROXY_STATE_FILE'"`
	Prefetch        int           `kong:"default='24',help='How many cached modules to pull from the remote at once on startup. 0 uses the default, a negative value disables prefetching.',env='GOCICA_MODULE_PROXY_PREFETCH'"`
}

// proxyStopCmd asks a running daemon to flush and exit.
type proxyStopCmd struct {
	StateFile string        `kong:"help='State file written by serve. Defaults to <dir>/mod/proxy.json.',env='GOCICA_MODULE_PROXY_STATE_FILE'"`
	Timeout   time.Duration `kong:"default='10m',help='How long to wait for the flush to finish.'"`
}

// CLI represents command line options and configuration file values
var CLI struct {
	Version  kong.VersionFlag `kong:"short='v',help='Show version and exit.'"`
	Dir      string           `kong:"short='d',optional,help='Directory to store cache files',env='GOCICA_DIR'"`
	LogLevel string           `kong:"short='l',default='info',enum='debug,info,warn,error,silent',help='Log level',env='GOCICA_LOG_LEVEL'"`
	Github   struct {
		CacheURL string `kong:"help='GitHub Actions Cache URL',env='GOCICA_GITHUB_CACHE_URL,ACTIONS_RESULTS_URL'"`
		Token    string `kong:"help='GitHub token',env='GOCICA_GITHUB_TOKEN,ACTIONS_RUNTIME_TOKEN'"`
		RunnerOS string `kong:"help='GitHub runner OS',env='GOCICA_GITHUB_RUNNER_OS,RUNNER_OS'"`
		Ref      string `kong:"help='GitHub base ref of the workflow or the target branch of the pull request',env='GOCICA_GITHUB_REF,GITHUB_REF'"`
		Sha      string `kong:"help='GitHub SHA of the commit',env='GOCICA_GITHUB_SHA,GITHUB_SHA'"`
	} `kong:"optional,group='github',embed,prefix='github.'"`
	Dev DevFlag `kong:"group='dev',embed,prefix='dev.'"`

	CacheProg cacheProgCmd `kong:"cmd,name='cacheprog',default='withargs',help='Serve the GOCACHEPROG protocol over stdin/stdout (default).'"`
	Serve     serveCmd     `kong:"cmd,name='serve',help='Serve the GOPROXY protocol for the Go module cache.'"`
	ProxyStop proxyStopCmd `kong:"cmd,name='proxy-stop',help='Flush and stop a running GOPROXY daemon.'"`
}

// loadConfig loads and parses configuration from command line arguments
func loadConfig() (*kong.Context, error) {
	// Parse command line arguments
	parser := kong.Must(&CLI,
		kong.Name("gocica"),
		kong.Description("A fast GOCACHEPROG implementation for CI"),
		kong.Vars{"version": fmt.Sprintf("%s (%s)", version, revision)},
		kong.UsageOnError(),
	)
	ctx, err := parser.Parse(os.Args[1:])
	if err != nil {
		return nil, fmt.Errorf("failed to parse arguments: %w", err)
	}

	// If directory is not specified, use cache directory
	if CLI.Dir == "" {
		cacheDir, err := os.UserCacheDir()
		if err == nil {
			CLI.Dir = filepath.Join(cacheDir, "gocica")
		}
	}

	// Validate directory
	if CLI.Dir == "" {
		return nil, fmt.Errorf("cache directory is not specified. please specify using the -dir flag or config file")
	}

	return ctx, nil
}

func main() {
	// Load configuration
	kongCtx, err := loadConfig()
	if err != nil {
		panic(fmt.Errorf("invalid configuration: %w", err))
	}

	logger := newLogger()

	// Start profiling. Enable profiling only in development mode.
	if err := CLI.Dev.StartProfiling(); err != nil {
		logger.Warnf("failed to start profiling: %v", err)
	}
	defer CLI.Dev.StopProfiling()

	logger.Debugf("configuration: %+v", CLI)

	switch kongCtx.Command() {
	case "serve":
		if err := runServe(logger); err != nil {
			logger.Errorf("module proxy: %v", err)
			CLI.Dev.StopProfiling()
			os.Exit(1)
		}
	case "proxy-stop":
		if err := runProxyStop(logger); err != nil {
			logger.Errorf("stop module proxy: %v", err)
			CLI.Dev.StopProfiling()
			os.Exit(1)
		}
	default:
		runCacheProg(logger)
	}
}

func newLogger() log.Logger {
	// Initialize default logger with info level
	logger := log.DefaultLogger

	// Set log level
	switch CLI.LogLevel {
	case "silent":
		logger = mylog.NewLogger(mylog.Silent)
	case "error":
		logger = mylog.NewLogger(mylog.Error)
	case "warn":
		logger = mylog.NewLogger(mylog.Warn)
	case "info":
		// default info level
	case "debug":
		logger = mylog.NewLogger(mylog.Debug)
	default:
		logger.Warnf("invalid log level: %s. ignore and use default info level instead", CLI.LogLevel)
	}

	return logger
}

func githubConfig() *provider.GHACacheConfig {
	return &provider.GHACacheConfig{
		Token:    CLI.Github.Token,
		CacheURL: CLI.Github.CacheURL,
		RunnerOS: CLI.Github.RunnerOS,
		Ref:      CLI.Github.Ref,
		Sha:      CLI.Github.Sha,
	}
}

func runCacheProg(logger log.Logger) {
	// Initialize process via DI (FR-002: Context parameter, FR-007: Degraded mode handling)
	// Use a cancellable context so we can clean up background goroutines on initialization failure.
	// The second context parameter is for GitHubActionsCache initialization (kessoku DI limitation).
	ctx, cancel := context.WithCancel(context.Background())
	// Defer cancel to ensure cleanup even on panic (idempotent - safe to call multiple times)
	defer cancel()

	process, err := kessoku.InitializeProcess(
		ctx,
		logger,
		local.DiskDir(CLI.Dir),
		githubConfig(),
	)
	if err != nil {
		// Degraded mode: log warning and continue with no-cache Process
		logger.Warnf("failed to initialize process: %v. no cache will be used.", err)
		process = protocol.NewProcess(protocol.WithLogger(logger))
	}

	if err := process.Run(); err != nil {
		panic(fmt.Errorf("unexpected error: failed to run process: %w", err))
	}
}

// moduleDir is where the module proxy keeps its objects. It is deliberately a
// subtree of its own: module objects and build objects have different sizes,
// lifetimes and cache namespaces.
func moduleDir() string {
	return filepath.Join(CLI.Dir, "mod")
}

func moduleStateFile(configured string) string {
	if configured != "" {
		return configured
	}

	return filepath.Join(moduleDir(), modproxy.StateFileName)
}

func runServe(logger log.Logger) error {
	dir := moduleDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create module cache directory: %w", err)
	}

	upstreamValue := CLI.Serve.Upstream
	if upstreamValue == "" {
		upstreamValue = os.Getenv("GOPROXY")
	}
	upstream := modproxy.ParseUpstream(upstreamValue)
	if upstream == nil {
		logger.Warnf("no http(s) upstream proxy configured. every miss will be a 404 and the go command will fall through on its own.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The module namespace is a separate GitHub Actions cache entry so its size
	// and eviction are independent of the build cache.
	moduleConfig := githubConfig()
	moduleConfig.Prefix = provider.ModuleCachePrefix
	moduleConfig.KeyVersion = provider.ModuleCacheVersion

	daemon, err := kessoku.InitializeModuleProxy(
		ctx,
		logger,
		modproxy.DaemonConfig{
			Addr:                CLI.Serve.Addr,
			StateFile:           moduleStateFile(CLI.Serve.StateFile),
			MaxLifetime:         CLI.Serve.MaxLifetime,
			PrefetchConcurrency: CLI.Serve.Prefetch,
		},
		upstream,
		modproxy.StoreDir(dir),
		moduleConfig,
	)
	if err != nil {
		return fmt.Errorf("initialize module proxy: %w", err)
	}

	goproxy := daemon.GOPROXY(os.Getenv("GOPROXY"))
	if CLI.Serve.ExportGithubEnv {
		if err := exportGithubEnv("GOPROXY", goproxy); err != nil {
			// Not fatal: the daemon is up and usable, the caller just has to export
			// GOPROXY itself.
			logger.Warnf("export GOPROXY to $GITHUB_ENV: %v", err)
		}
	}

	if CLI.Serve.PrewarmBuild {
		daemon.SetAfterPrefetch(func(ctx context.Context) {
			prewarmBuildCache(ctx, logger)
		})
	}

	fmt.Printf("{\"url\":%q,\"goproxy\":%q}\n", daemon.URL(), goproxy)

	if err := daemon.Run(ctx); err != nil {
		return fmt.Errorf("run module proxy: %w", err)
	}

	return nil
}

// prewarmBuildCache pulls the build cache blob onto local disk while the daemon
// is otherwise idle.
//
// Without it the first `go` command of the job restores it inside its own wall
// time, and every later `go` command in the same job pays again. This is the one
// thing actions/setup-go does that gocica structurally did not: restore before
// the build rather than during it.
//
// It is wired by hand rather than through the injector because it needs a second
// remote in the same process -- the build cache namespace, not the module one --
// and the graph is keyed by type.
func prewarmBuildCache(ctx context.Context, logger log.Logger) {
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("panic while prewarming the build cache: %v", r)
		}
	}()

	downloadProvider, _, err := provider.GHACacheProvider(ctx, logger, githubConfig())
	if err != nil {
		logger.Warnf("prewarm the build cache: %v", err)

		return
	}

	client, err := downloadProvider(ctx)
	if err != nil || client == nil {
		logger.Debugf("no build cache to prewarm: %v", err)

		return
	}

	downloader, err := core.NewDownloader(ctx, logger, client)
	if err != nil {
		logger.Warnf("prewarm the build cache: %v", err)

		return
	}

	disk, err := local.NewDisk(logger, local.DiskDir(CLI.Dir))
	if err != nil {
		logger.Warnf("prewarm the build cache: %v", err)

		return
	}

	if err := core.PrewarmLocal(ctx, logger, downloader, disk); err != nil {
		logger.Warnf("prewarm the build cache: %v", err)
	}
}

func exportGithubEnv(key, value string) error {
	path := os.Getenv("GITHUB_ENV")
	if path == "" {
		return errors.New("GITHUB_ENV is not set")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open GITHUB_ENV: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s=%s\n", key, value); err != nil {
		return fmt.Errorf("write GITHUB_ENV: %w", err)
	}

	return nil
}

func runProxyStop(logger log.Logger) error {
	path := moduleStateFile(CLI.ProxyStop.StateFile)

	state, err := modproxy.ReadState(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A post step must never fail the job just because the daemon is gone.
			logger.Warnf("no module proxy state file at %s. nothing to stop.", path)

			return nil
		}

		return fmt.Errorf("read state file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), CLI.ProxyStop.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, state.URL+"/-/shutdown", nil)
	if err != nil {
		return fmt.Errorf("create shutdown request: %w", err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		// The daemon exits as soon as it has flushed, so it may drop the connection
		// before the response lands. Either way, a post step must not fail the job.
		logger.Warnf("module proxy at %s is not answering: %v. assuming it already exited.", state.URL, err)

		return nil
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read shutdown response: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("module proxy returned %d: %s", res.StatusCode, body)
	}

	logger.Infof("module proxy stopped: %s", body)

	return nil
}
