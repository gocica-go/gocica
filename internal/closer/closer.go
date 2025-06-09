package closer

import (
	"context"
	"sync"

	"github.com/mazrean/gocica/log"
	"golang.org/x/sync/errgroup"
)

var (
	closerLocker sync.RWMutex
	closer       []func(context.Context) error
)

func Add(f func(context.Context) error) {
	closerLocker.Lock()
	defer closerLocker.Unlock()
	closer = append(closer, f)
}

func Close(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)

	closerLocker.RLock()
	defer closerLocker.RUnlock()

	for _, f := range closer {
		eg.Go(func() error {
			defer func() {
				if r := recover(); r != nil {
					log.DefaultLogger.Warnf("panic in closer: %v", r)
				}
			}()

			return f(ctx)
		})
	}

	return eg.Wait()
}
