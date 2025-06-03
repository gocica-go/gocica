package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/mazrean/gocica/internal/cacheprog"
	"github.com/mazrean/gocica/internal/local"
	mylog "github.com/mazrean/gocica/internal/pkg/log"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
)

//go:generate go tool buf generate

var (
	version  = "dev"
	revision = "none"
)

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

func createBackend(logger log.Logger) (cacheprog.Backend, error) {
	// Initialize backend storage
	diskBackend, err := local.NewDisk(logger, CLI.Dir)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend: %w", err)
	}

	var remoteBackend remote.Backend
	// If GitHub token is not specified, use disk backend only
	if CLI.Github.Token == "" {
		return nil, fmt.Errorf("GitHub token is not specified")
	}

	// Initialize GitHub Actions Cache backend
	remoteBackend, err = remote.NewGitHubActionsCache(
		logger,
		CLI.Github.Token,
		CLI.Github.CacheURL,
		CLI.Github.RunnerOS, CLI.Github.Ref, CLI.Github.Sha,
		diskBackend,
	)
	if err != nil {
		return nil, fmt.Errorf("create GitHub Actions Cache backend: %w", err)
	}

	// Initialize combined backend
	combinedBackend, err := cacheprog.NewConbinedBackend(logger, diskBackend, remoteBackend)
	if err != nil {
		return nil, fmt.Errorf("failed to create combined backend: %w", err)
	}

	return combinedBackend, nil
}

func main() {
	// Load configuration
	_, err := loadConfig()
	if err != nil {
		panic(fmt.Errorf("invalid configuration: %w", err))
	}

	// Initialize default logger with info level
	logger := log.DefaultLogger

	// Start profiling. Enable profiling only in development mode.
	if err := CLI.Dev.StartProfiling(); err != nil {
		logger.Warnf("failed to start profiling: %v", err)
	}
	defer CLI.Dev.StopProfiling()

	// Set log level
	switch CLI.LogLevel {
	case "silent":
		logger = mylog.NewLogger(mylog.Silent)
	case "error":
		logger = mylog.NewLogger(mylog.Error)
	case "warn":
		logger = mylog.NewLogger(mylog.Warn)
	case "info":
	case "debug":
		logger = mylog.NewLogger(mylog.Debug)
	default:
		logger.Warnf("invalid log level: %s. ignore and use default info level instead", CLI.LogLevel)
	}

	logger.Debugf("configuration: %+v", CLI)

	options := make([]protocol.ProcessOption, 0, 4)
	options = append(options, protocol.WithLogger(logger))

	// Initialize backend
	backend, err := createBackend(logger)
	if err != nil {
		// If backend initialization failed, no cache will be used
		logger.Warnf("failed to create backend: %v. no cache will be used.", err)
	} else {
		// Create application instance
		app := cacheprog.NewGocica(logger, backend)

		options = append(options,
			protocol.WithGetHandler(app.Get),
			protocol.WithPutHandler(app.Put),
			protocol.WithCloseHandler(app.Close),
		)
	}

	// Initialize and run process
	process := protocol.NewProcess(options...)

	if err := process.Run(); err != nil {
		panic(fmt.Errorf("unexpected error: failed to run process: %w", err))
	}
}
