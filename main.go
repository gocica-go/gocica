package main

import (
	"os"

	"github.com/mazrean/gocica/internal/config"
	"github.com/mazrean/gocica/internal/wire"
	"github.com/mazrean/gocica/log"
)

//go:generate go tool buf generate

var (
	name        = "gocica"
	description = "Go Compiler Cache for GitHub Actions"
	version     = "dev"
	revision    = "none"
)

func main() {
	app, err := wire.InjectApp(config.CmdInfo{
		Name:        name,
		Description: description,
		Version:     version,
		Revision:    revision,
	})
	if err != nil {
		log.DefaultLogger.Errorf("failed to create app: %v", err)
		os.Exit(1)
	}

	if err := app.Run(); err != nil {
		log.DefaultLogger.Errorf("failed to run app: %v", err)
		os.Exit(1)
	}
}
