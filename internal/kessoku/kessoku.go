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
func NewProcessWithOptions(logger log.Logger, cacheProg *cacheprog.CacheProg) *protocol.Process {
	return protocol.NewProcess(
		protocol.WithLogger(logger),
		protocol.WithGetHandler(cacheProg.Get),
		protocol.WithPutHandler(cacheProg.Put),
		protocol.WithCloseHandler(cacheProg.Close),
	)
}

// InitializeProcess is the main DI injector function.
// It creates a fully configured Process with all dependencies wired up.
// Unsatisfied dependencies (logger, dir, token, cacheURL, runnerOS, ref, sha) become function parameters.
var _ = kessoku.Inject[*protocol.Process](
	"InitializeProcess",
	kessoku.Async(kessoku.Bind[local.Backend](kessoku.Provide(local.NewDisk))),

	kessoku.Bind[remote.Backend](kessoku.Provide(remote.NewBackend)),
	kessoku.Async(kessoku.Provide(remote.NewUploader)),
	kessoku.Async(kessoku.Bind[remote.BaseBlobProvider](kessoku.Provide(remote.NewDownloader))),
	kessoku.Async(kessoku.Provide(provider.DownloadClientProviderExecutor)),
	kessoku.Async(kessoku.Provide(provider.UploadClientProviderExecutor)),
	kessoku.Provide(provider.Switch),

	kessoku.Async(kessoku.Bind[cacheprog.Backend](kessoku.Provide(cacheprog.NewConbinedBackend))),

	kessoku.Provide(cacheprog.NewCacheProg),

	kessoku.Provide(NewProcessWithOptions),
)
