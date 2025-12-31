package provider

import (
	"context"

	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/log"
)

type DownloadClientProvider func(context.Context) (core.DownloadClient, error)

func DownloadClientProviderExecutor(ctx context.Context, f DownloadClientProvider) (core.DownloadClient, error) {
	return f(ctx)
}

type UploadClientProvider func(context.Context) (core.UploadClient, error)

func UploadClientProviderExecutor(ctx context.Context, f UploadClientProvider) (core.UploadClient, error) {
	return f(ctx)
}

func Switch(
	ctx context.Context,
	logger log.Logger,
	ghaCacheConfig *GHACacheConfig,
) (DownloadClientProvider, UploadClientProvider, error) {
	switch {
	case ghaCacheConfig != nil:
		return GHACacheProvider(ctx, logger, ghaCacheConfig)
	default:
		return nil, nil, nil
	}
}
