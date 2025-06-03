//go:build wireinject

package wire

import (
	"github.com/google/wire"
	"github.com/mazrean/gocica/internal/remote"
)

var remoteSet = wire.NewSet(
	wire.Bind(new(remote.Backend), new(*remote.GitHubActionsCache)),
	remote.NewGitHubActionsCache,
)
