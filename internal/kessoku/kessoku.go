package kessoku

import (
	"github.com/mazrean/gocica/internal/cacheprog"
	"github.com/mazrean/gocica/internal/local"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/internal/remote/provider"
	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
	"github.com/mazrean/kessoku"
)

//go:generate go tool github.com/mazrean/kessoku/cmd/kessoku $GOFILE

// NewProcessWithOptions creates a new Process with the given logger and Gocica instance.
// This is a DI-friendly wrapper that constructs ProcessOptions from the dependencies.
func NewProcessWithOptions(logger log.Logger, gocica *cacheprog.CacheProg) *protocol.Process {
	return protocol.NewProcess(
		protocol.WithLogger(logger),
		protocol.WithGetHandler(gocica.Get),
		protocol.WithPutHandler(gocica.Put),
		protocol.WithCloseHandler(gocica.Close),
	)
}

// InitializeProcess is the main DI injector function.
// It creates a fully configured Process with all dependencies wired up.
// Unsatisfied dependencies (logger, dir, token, cacheURL, runnerOS, ref, sha) become function parameters.
var _ = kessoku.Inject[*protocol.Process](
	"InitializeProcess",
	// Provider: Disk → LocalBackend (async for parallel initialization, interface binding)
	kessoku.Async(kessoku.Bind[local.Backend](kessoku.Provide(local.NewDisk))),

	// Provider: GitHubActionsCache → RemoteBackend (interface binding)
	// Depends on LocalBackend, GitHubCacheClient, Uploader, and Downloader
	kessoku.Async(kessoku.Bind[remote.Backend](kessoku.Provide(provider.NewGitHubActionsCache))),

	// Provider: CombinedBackend → Backend (interface binding)
	// Context is passed through for proper cancellation of background operations
	kessoku.Async(kessoku.Bind[cacheprog.Backend](kessoku.Provide(cacheprog.NewConbinedBackend))),

	// Provider: Gocica
	kessoku.Provide(cacheprog.NewCacheProg),

	// Provider: Process (target)
	kessoku.Provide(NewProcessWithOptions),
)
