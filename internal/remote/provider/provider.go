package provider

import (
	"context"

	"github.com/mazrean/gocica/internal/remote"
	"github.com/mazrean/gocica/log"
)

type DownloadClientProvider func(context.Context) (remote.DownloadClient, error)

func DownloadClientProviderExecutor(ctx context.Context, f DownloadClientProvider) (remote.DownloadClient, error) {
	if f == nil {
		return nil, nil
	}

	return f(ctx)
}

type UploadClientProvider func(context.Context) (remote.UploadClient, error)

func UploadClientProviderExecutor(ctx context.Context, f UploadClientProvider) (remote.UploadClient, error) {
	if f == nil {
		return nil, nil
	}

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
