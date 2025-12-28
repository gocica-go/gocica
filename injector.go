package main

import (
	"context"

	"github.com/mazrean/gocica/internal"
	"github.com/mazrean/gocica/internal/backend"
	"github.com/mazrean/gocica/log"
	"github.com/mazrean/gocica/protocol"
	"github.com/mazrean/kessoku"
)

//go:generate go tool github.com/mazrean/kessoku/cmd/kessoku $GOFILE

// Named types for config values to distinguish them in DI
type (
	Dir      string // cache directory path
	Token    string // GitHub token
	CacheURL string // Actions cache URL
	RunnerOS string // runner OS
	Ref      string // GitHub ref
	Sha      string // GitHub SHA
)

// NewDiskWithDI wraps backend.NewDisk to accept named type
func NewDiskWithDI(logger log.Logger, dir Dir) (*backend.Disk, error) {
	return backend.NewDisk(logger, string(dir))
}

// NewGitHubActionsCacheWithDI wraps backend.NewGitHubActionsCache to accept named types
// Context is passed through for proper cancellation/timeout support.
func NewGitHubActionsCacheWithDI(
	ctx context.Context,
	logger log.Logger,
	token Token,
	cacheURL CacheURL,
	runnerOS RunnerOS,
	ref Ref,
	sha Sha,
	localBackend backend.LocalBackend,
) (*backend.GitHubActionsCache, error) {
	return backend.NewGitHubActionsCache(
		ctx,
		logger,
		string(token),
		string(cacheURL),
		string(runnerOS),
		string(ref),
		string(sha),
		localBackend,
	)
}

// NewProcessWithOptions creates a new Process with the given logger and Gocica instance.
// This is a DI-friendly wrapper that constructs ProcessOptions from the dependencies.
// Context is passed through for proper shutdown coordination.
func NewProcessWithOptions(ctx context.Context, logger log.Logger, gocica *internal.Gocica) *protocol.Process {
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
	kessoku.Async(kessoku.Bind[backend.LocalBackend](kessoku.Provide(NewDiskWithDI))),

	// Provider: GitHubActionsCache → RemoteBackend (interface binding)
	// Note: Depends on LocalBackend, so runs after Disk completes
	// AzureUploadClient/AzureDownloadClient are created internally (require signed URLs from GitHub API)
	kessoku.Async(kessoku.Bind[backend.RemoteBackend](kessoku.Provide(NewGitHubActionsCacheWithDI))),

	// Provider: ConbinedBackend → Backend (interface binding)
	// Context is passed through for proper cancellation of background operations
	kessoku.Async(kessoku.Bind[backend.Backend](kessoku.Provide(backend.NewConbinedBackend))),

	// Provider: Gocica
	kessoku.Provide(internal.NewGocica),

	// Provider: Process (target)
	kessoku.Provide(NewProcessWithOptions),
)
