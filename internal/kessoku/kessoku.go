package kessoku

import (
	"github.com/mazrean/gocica/internal/cacheprog"
	"github.com/mazrean/gocica/internal/local"
	"github.com/mazrean/gocica/internal/modproxy"
	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/internal/remote/core"
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

	kessoku.Bind[remote.Backend](kessoku.Provide(core.NewBackend)),
	kessoku.Value[core.CompressionPolicy](core.DefaultCompressionPolicy),
	kessoku.Async(kessoku.Provide(core.NewUploader)),
	kessoku.Async(kessoku.Bind[core.BaseBlobProvider](kessoku.Provide(core.NewDownloader))),
	kessoku.Async(kessoku.Provide(provider.DownloadClientProviderExecutor)),
	kessoku.Async(kessoku.Provide(provider.UploadClientProviderExecutor)),
	kessoku.Provide(provider.Switch),

	kessoku.Async(kessoku.Bind[cacheprog.Backend](kessoku.Provide(cacheprog.NewConbinedBackend))),

	kessoku.Provide(cacheprog.NewCacheProg),

	kessoku.Provide(NewProcessWithOptions),
)

// InitializeModuleProxy builds the GOPROXY daemon.
//
// It is a separate injector on purpose. InitializeProcess is all-or-nothing: any
// provider error there leaves main with a handler-less Process, i.e. no caching
// at all. Wiring the module proxy into the same graph would let a proxy problem
// take the build cache down with it, and vice versa.
var _ = kessoku.Inject[*modproxy.Daemon](
	"InitializeModuleProxy",
	kessoku.Async(kessoku.Provide(modproxy.NewStore)),
	kessoku.Provide(modproxy.NewCompressionRegistry),
	kessoku.Provide(modproxy.NewCompressionPolicy),

	kessoku.Async(kessoku.Bind[core.BaseBlobProvider](kessoku.Provide(core.NewDownloader))),
	kessoku.Async(kessoku.Provide(core.NewUploader)),
	kessoku.Async(kessoku.Provide(provider.DownloadClientProviderExecutor)),
	kessoku.Async(kessoku.Provide(provider.UploadClientProviderExecutor)),
	kessoku.Provide(provider.Switch),

	kessoku.Provide(modproxy.NewCache),
	kessoku.Provide(modproxy.NewServer),
	kessoku.Provide(modproxy.NewDaemon),
)
