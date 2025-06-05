//go:build wireinject

//go:generate go tool github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/mazrean/gocica/internal/cacheprog"
	"github.com/mazrean/gocica/internal/config"
	mylog "github.com/mazrean/gocica/internal/pkg/log"
	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
)

type App struct {
	logger    log.Logger
	config    *config.Config
	cacheprog *cacheprog.CacheProg
}

func newApp(
	logger log.Logger,
	config *config.Config,
	cacheprog *cacheprog.CacheProg,
) *App {
	return &App{
		logger:    logger,
		config:    config,
		cacheprog: cacheprog,
	}
}

func (a *App) Run() error {
	if err := a.config.Dev.StartProfiling(); err != nil {
		a.logger.Warnf("failed to start profiling: %v", err)
	}
	defer a.config.Dev.StopProfiling()

	eg := errgroup.Group{}
	eg.Go(func() error {
		return a.cacheprog.Run()
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}

func newLogger(config *config.Config) log.Logger {
	switch config.LogLevel {
	case "silent":
		return mylog.NewLogger(mylog.Silent)
	case "error":
		return mylog.NewLogger(mylog.Error)
	case "warn":
		return mylog.NewLogger(mylog.Warn)
	case "info":
		return mylog.NewLogger(mylog.Info)
	case "debug":
		return mylog.NewLogger(mylog.Debug)
	}

	log.DefaultLogger.Warnf("invalid log level: %s. ignore and use default info level instead", config.LogLevel)
	return log.DefaultLogger
}

func InjectApp(cmdInfo config.CmdInfo) (*App, error) {
	wire.Build(
		newLogger,
		config.Load,

		newApp,

		cacheprogSet,
		localSet,
		remoteSet,
	)

	return nil, nil
}
