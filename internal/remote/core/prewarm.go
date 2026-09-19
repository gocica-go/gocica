package core

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/mazrean/gocica/internal/local"
	"github.com/mazrean/gocica/log"
)

// PrewarmLocal pulls every object in the remote blob onto local disk.
//
// The build cache is restored by whichever `go` command happens to start first,
// which means the restore lands inside that command's own wall time -- and, since
// the disk store only publishes an object once it is complete, a second `go`
// command in the same job would otherwise download the whole blob again. Running
// this from the long-lived proxy daemon moves the transfer off the critical path
// entirely, which is what actions/setup-go gets from restoring a tarball in its
// own step.
func PrewarmLocal(ctx context.Context, logger log.Logger, downloader *Downloader, localBackend local.Backend) error {
	if downloader.IsEmpty() {
		return nil
	}

	started := time.Now()
	err := downloader.DownloadAllOutputBlocks(ctx, func(ctx context.Context, objectID string) (io.WriteCloser, error) {
		_, w, err := localBackend.Put(ctx, objectID, 0)
		if err != nil {
			return nil, fmt.Errorf("put local cache: %w", err)
		}

		return w, nil
	}, func(objectID string) bool {
		// Another process in the same job may have put it there already.
		return localBackend.Has(ctx, objectID)
	})
	if err != nil {
		return fmt.Errorf("download all output blocks: %w", err)
	}

	logger.Infof("prewarmed the build cache in %s.", time.Since(started))

	return nil
}
