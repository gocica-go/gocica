package main

import (
	"context"

	"github.com/mazrean/gocica/internal/cacheprog"
	"github.com/mazrean/gocica/internal/local"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/internal/remote/blob"
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

// NewDiskWithDI wraps local.NewDisk to accept named type
func NewDiskWithDI(logger log.Logger, dir Dir) (*local.Disk, error) {
	return local.NewDisk(logger, string(dir))
}

// NewGitHubCacheClientWithDI wraps blob.NewGitHubCacheClient to accept named types.
// This creates the GitHub Cache API client for downloading and uploading cache.
func NewGitHubCacheClientWithDI(
	ctx context.Context,
	logger log.Logger,
	token Token,
	cacheURL CacheURL,
	runnerOS RunnerOS,
	ref Ref,
	sha Sha,
) (*blob.GitHubCacheClient, error) {
	return blob.NewGitHubCacheClient(
		ctx,
		logger,
		string(token),
		string(cacheURL),
		string(runnerOS),
		string(ref),
		string(sha),
	)
}

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
	kessoku.Async(kessoku.Bind[local.Backend](kessoku.Provide(NewDiskWithDI))),

	// Provider: GitHubCacheClient (async, creates API client for GitHub Cache)
	kessoku.Async(kessoku.Provide(NewGitHubCacheClientWithDI)),

	// Provider: DownloadClient (async, creates Azure blob client for downloading)
	// Returns nil if download URL is empty
	kessoku.Async(kessoku.Provide(blob.NewDownloadClient)),

	// Provider: Downloader (async, creates blob downloader)
	// Returns nil if download client is nil
	kessoku.Async(kessoku.Provide(blob.NewDownloader)),

	// Provider: UploadClient (async, creates Azure blob client for uploading)
	// Returns nil if upload URL is empty
	kessoku.Async(kessoku.Provide(blob.NewUploadClient)),

	// Provider: Uploader (creates blob uploader)
	// Returns nil if upload client is nil
	kessoku.Provide(blob.NewUploaderOrNil),

	// Provider: GitHubActionsCache → RemoteBackend (interface binding)
	// Depends on LocalBackend, GitHubCacheClient, Uploader, and Downloader
	kessoku.Async(kessoku.Bind[remote.Backend](kessoku.Provide(remote.NewGitHubActionsCache))),

	// Provider: CombinedBackend → Backend (interface binding)
	// Context is passed through for proper cancellation of background operations
	kessoku.Async(kessoku.Bind[cacheprog.Backend](kessoku.Provide(cacheprog.NewConbinedBackend))),

	// Provider: Gocica
	kessoku.Provide(cacheprog.NewCacheProg),

	// Provider: Process (target)
	kessoku.Provide(NewProcessWithOptions),
)
