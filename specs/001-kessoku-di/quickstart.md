# Quickstart: Kessoku DI Integration

**Date**: 2025-12-27
**Target Audience**: Developers implementing the kessoku DI integration

## Prerequisites

1. Go 1.24+ installed
2. kessoku CLI tool installed:
   ```bash
   go get -tool github.com/mazrean/kessoku/cmd/kessoku
   ```

## Step 1: Add kessoku Dependency

Add to `tools/go.mod`:
```go
require github.com/mazrean/kessoku v1.x.x
```

Run:
```bash
cd tools && go mod tidy
```

## Step 2: Create Injector File

Create `injector.go` at repository root:

```go
//go:build !test

package main

import (
	"context"

	"github.com/mazrean/kessoku"
	"github.com/mazrean/gocica/internal"
	"github.com/mazrean/gocica/internal/backend"
	"github.com/mazrean/gocica/internal/pkg/log"
	"github.com/mazrean/gocica/protocol"
)

//go:generate go tool github.com/mazrean/kessoku/cmd/kessoku $GOFILE

// InitializeBackend creates the cache backend with parallel initialization
var _ = kessoku.Inject[backend.Backend](
	"InitializeBackend",
	// External values (injected from main)
	kessoku.Value[log.Logger](nil),    // Placeholder, actual value passed at call
	kessoku.Value[string](""),         // dir
	kessoku.Value[string](""),         // token
	kessoku.Value[string](""),         // cacheURL
	kessoku.Value[string](""),         // runnerOS
	kessoku.Value[string](""),         // ref
	kessoku.Value[string](""),         // sha

	// Async providers (parallel execution)
	kessoku.Async(kessoku.Bind[backend.LocalBackend](kessoku.Provide(backend.NewDisk))),
	kessoku.Async(kessoku.Bind[backend.RemoteBackend](kessoku.Provide(backend.NewGitHubActionsCache))),

	// Sequential provider
	kessoku.Bind[backend.Backend](kessoku.Provide(backend.NewConbinedBackend)),
)
```

## Step 3: Generate Injector Code

Run from repository root:
```bash
go generate ./...
```

This creates `injector_band.go` with the generated `InitializeBackend` function.

## Step 4: Update main.go

Replace `createBackend` call with injector:

```go
func main() {
	// ... CLI parsing, logger creation ...

	// Initialize backend using DI
	ctx := context.Background()
	backend, err := InitializeBackend(
		ctx,
		logger,
		CLI.Dir,
		CLI.Github.Token,
		CLI.Github.CacheURL,
		CLI.Github.RunnerOS,
		CLI.Github.Ref,
		CLI.Github.Sha,
	)
	if err != nil {
		logger.Warnf("failed to create backend: %v. no cache will be used.", err)
		// Continue without backend...
	}

	// ... rest of initialization ...
}
```

## Step 5: Verify

1. Run tests:
   ```bash
   go test ./... -v
   ```

2. Build and run:
   ```bash
   go build -o gocica .
   ./gocica --help
   ```

3. Verify parallel initialization (with debug logging):
   ```bash
   ./gocica -l debug 2>&1 | grep -E "(Disk|GitHub)"
   ```

## Expected Behavior

- Disk and GitHubActionsCache initialize in parallel
- ConbinedBackend waits for both to complete
- Total startup time ≈ max(disk_init, github_init) instead of sum

## Troubleshooting

### "undefined: InitializeBackend"

Run `go generate ./...` to create the generated file.

### "cycle detected in dependency graph"

Check that there are no circular dependencies between providers.

### Startup time regression

Verify async markers are present on Disk and GitHubActionsCache providers.

## Testing with Mocks

For unit tests, create a test injector:

```go
//go:build test

package main

import (
	"github.com/mazrean/kessoku"
	"github.com/mazrean/gocica/internal/backend"
)

//go:generate go tool github.com/mazrean/kessoku/cmd/kessoku $GOFILE

var _ = kessoku.Inject[backend.Backend](
	"InitializeTestBackend",
	kessoku.Bind[backend.LocalBackend](kessoku.Provide(NewMockDisk)),
	kessoku.Bind[backend.RemoteBackend](kessoku.Provide(NewMockRemote)),
	kessoku.Bind[backend.Backend](kessoku.Provide(backend.NewConbinedBackend)),
)
```

## Next Steps

1. Run `/speckit.tasks` to generate implementation tasks
2. Implement changes according to task order
3. Run `/speckit.analyze` to verify consistency
