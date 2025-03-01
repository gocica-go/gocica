package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/mazrean/gocica/internal"
	"github.com/mazrean/gocica/internal/backend"
	"github.com/mazrean/gocica/internal/pkg/log"
	"github.com/mazrean/gocica/protocol"
)

//go:generate go tool buf generate

func main() {
	logger := log.NewLogger(log.Info)

	var (
		dir       string
		logLevel  string
		isMemMode bool
	)

	var defaultCacheDir string
	cacheDir, err := os.UserCacheDir()
	if err == nil {
		defaultCacheDir = filepath.Join(cacheDir, "gocica")
	} else {
		logger.Debugf("could not get user cache directory: %v", err)
	}
	flag.StringVar(&dir, "dir", defaultCacheDir, "directory to store data")
	flag.StringVar(&logLevel, "log-level", "info", "log level(debug, info, error, none)")
	flag.BoolVar(&isMemMode, "mem-mode", false, "use memory database")
	flag.Parse()

	switch logLevel {
	case "none":
		logger = log.NewLogger(log.None)
	case "error":
		logger = log.NewLogger(log.Error)
	case "info":
	case "debug":
		logger = log.NewLogger(log.Debug)
	default:
		logger.Errorf("invalid log level: %s", logLevel)
		os.Exit(1)
	}

	if dir == "" {
		logger.Errorf("could not determine cache directory. Please specify one using the -dir flag")
		os.Exit(1)
	}

	backend, err := backend.NewDisk(dir, isMemMode)
	if err != nil {
		logger.Errorf("failed to create backend: %v", err)
		os.Exit(1)
	}

	app := internal.NewGocica(logger, backend)

	process := protocol.NewProcess(
		protocol.WithGetHandler(app.Get),
		protocol.WithPutHandler(app.Put),
		protocol.WithCloseHandler(app.Close),
		protocol.WithLogger(logger),
	)

	if err := process.Run(); err != nil {
		logger.Errorf("failed to run process: %v", err)
	}
}
