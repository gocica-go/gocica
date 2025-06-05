//go:build wireinject

package wire

import (
	"github.com/google/wire"
	"github.com/mazrean/gocica/internal/cacheprog"
)

var cacheprogSet = wire.NewSet(
	wire.Bind(new(cacheprog.Backend), new(*cacheprog.CombinedBackend)),
	cacheprog.NewCombinedBackend,
	cacheprog.NewCacheProg,
)
