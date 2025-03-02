package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/mazrean/gocica/internal"
	"github.com/mazrean/gocica/internal/backend"
	mylog "github.com/mazrean/gocica/internal/pkg/log"
	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
)

//go:generate go tool buf generate

var Version = "dev"

// CLI represents command line options and configuration file values
var CLI struct {
	Version  kong.VersionFlag `kong:"short='v',help='Show version and exit.'"`
	Config   kong.ConfigFlag  `kong:"chort='c',help='Load configuration from a file.'"`
	Dir      string           `kong:"short='d',optional,help='Directory to store cache files',env='GOCICA_DIR'"`
	LogLevel string           `kong:"short='l',default='info',enum='debug,info,error,silent',help='Log level',env='GOCICA_LOG_LEVEL'"`
	Remote   string           `kong:"short='r',default='none',enum='none,s3,github',help='Remote backend',env='GOCICA_REMOTE'"`
	S3       struct {
		Region          string `kong:"help='AWS region',env='GOCICA_S3_REGION'"`
		Bucket          string `kong:"help='S3 bucket name',env='GOCICA_S3_BUCKET'"`
		AccessKey       string `kong:"help='AWS access key',env='GOCIAC_S3_ACCESS_KEY'"`
		SecretAccessKey string `kong:"help='AWS secret access key',env='GOCIAC_S3_SECRET_ACCESS_KEY'"`
		Endpoint        string `kong:"help='S3 endpoint',env='GOCICA_S3_ENDPOINT',default='https://s3.amazonaws.com'"`
		DisableSSL      bool   `kong:"help='Disable SSL for S3 connection',env='GOCICA_S3_DISABLE_SSL'"`
		UsePathStyle    bool   `kong:"help='Use path style for S3 connection',env='GOCICA_S3_USE_PATH_STYLE'"`
	} `kong:"optional,group='s3',embed,prefix='s3.'"`
	Github struct {
		Token string `kong:"help='GitHub token',env='GOCICA_GITHUB_TOKEN,ACTIONS_RUNTIME_TOKEN'"`
	} `kong:"optional,group='github',embed,prefix='github.'"`
	Dev DevFlag `kong:"group='dev',embed,prefix='dev.'"`
}

// loadConfig loads and parses configuration from command line arguments and config files
func loadConfig(logger log.Logger) (*kong.Context, error) {
	// Find config file paths
	var configPaths []string
	wd, err := os.Getwd()
	if err == nil {
		configPaths = append(configPaths, filepath.Join(wd, ".gocica.json"))
	} else {
		logger.Infof("failed to get working directory. ignoring config file in working directory")
	}

	userHomeDir, err := os.UserHomeDir()
	if err == nil {
		configPaths = append(configPaths, filepath.Join(userHomeDir, ".gocica.json"))
	} else {
		logger.Infof("failed to get user home directory. ignoring config file in user home directory")
	}

	// Parse command line arguments and config files
	parser := kong.Must(&CLI,
		kong.Name("gocica"),
		kong.Description("A fast GOCACHEPROG implementation for CI"),
		kong.Configuration(kong.JSON, configPaths...),
		kong.Vars{"version": Version},
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

func createBackend(logger log.Logger) (backend.Backend, error) {
	// Initialize backend storage
	diskBackend, err := backend.NewDisk(logger, CLI.Dir)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend: %w", err)
	}

	if CLI.Remote == "none" {
		return backend.NewNoRemoteBackend(logger, diskBackend)
	}

	var remoteBackend backend.RemoteBackend
	switch CLI.Remote {
	case "s3":
		// If S3 bucket is not specified, use disk backend only
		if CLI.S3.Bucket == "" {
			logger.Infof("S3 bucket is not specified. use disk backend only")
			return backend.NewNoRemoteBackend(logger, diskBackend)
		}

		// Initialize S3 backend
		remoteBackend, err = backend.NewS3(
			logger,
			CLI.S3.Endpoint,
			CLI.S3.Region,
			CLI.S3.AccessKey,
			CLI.S3.SecretAccessKey,
			CLI.S3.Bucket,
			!CLI.S3.DisableSSL,
			CLI.S3.UsePathStyle,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create remote backend: %w", err)
		}
	case "github":
		// If GitHub token is not specified, use disk backend only
		if CLI.Github.Token == "" {
			logger.Infof("GitHub token is not specified. use disk backend only")
			return backend.NewNoRemoteBackend(logger, diskBackend)
		}

		// Initialize GitHub Actions Cache backend
		remoteBackend = backend.NewGitHubActionsCache(logger, CLI.Github.Token)
	}

	// Initialize combined backend
	combinedBackend, err := backend.NewConbinedBackend(logger, diskBackend, remoteBackend)
	if err != nil {
		return nil, fmt.Errorf("failed to create combined backend: %w", err)
	}

	return combinedBackend, nil
}

func main() {
	// Initialize default logger with info level
	logger := log.DefaultLogger

	// Load configuration
	_, err := loadConfig(logger)
	if err != nil {
		logger.Errorf("invalid configuration: %v", err)
		os.Exit(1)
	}

	if err := CLI.Dev.StartProfiling(); err != nil {
		logger.Errorf("failed to start profiling: %v", err)
	}
	defer CLI.Dev.StopProfiling()

	// Set log level
	switch CLI.LogLevel {
	case "silent":
		logger = mylog.NewLogger(mylog.Silent)
	case "error":
		logger = mylog.NewLogger(mylog.Error)
	case "info":
	case "debug":
		logger = mylog.NewLogger(mylog.Debug)
	default:
		logger.Infof("invalid log level: %s. ignore log level setting.", CLI.LogLevel)
	}

	logger.Debugf("configuration: %+v", CLI)

	// Initialize backend
	backend, err := createBackend(logger)
	if err != nil {
		logger.Errorf("unexpected error: failed to create combined backend: %v", err)
		os.Exit(1)
	}

	// Create application instance
	app := internal.NewGocica(logger, backend)

	// Initialize and run process
	process := protocol.NewProcess(
		protocol.WithGetHandler(app.Get),
		protocol.WithPutHandler(app.Put),
		protocol.WithCloseHandler(app.Close),
		protocol.WithLogger(logger),
	)

	if err := process.Run(); err != nil {
		logger.Errorf("unexpected error: failed to run process: %v", err)
		os.Exit(1)
	}
}
