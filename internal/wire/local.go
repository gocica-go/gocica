//go:build wireinject

package wire

import (
	"github.com/google/wire"
	"github.com/mazrean/gocica/internal/local"
)

var localSet = wire.NewSet(
	wire.Bind(new(local.Backend), new(*local.Disk)),
	local.NewDisk,
)
