# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoCICa is a Go compiler build and module caching tool for CI environments. It implements Go's GOCACHEPROG feature to provide a cache optimized for GitHub Actions, storing cache entries both locally on disk and remotely in GitHub Actions Cache.

## Build and Development Commands

```bash
# Build the binary
go build -o gocica .

# Build with dev features (profiling support)
go build -tags=dev -o gocica .

# Run all tests
go test ./... -v

# Run a single test
go test -v -run TestName ./path/to/package

# Run tests with coverage
go test ./... -v -coverprofile=coverage.txt -race -vet=off

# Lint (uses golangci-lint v2)
go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run

# Generate protobuf code
go generate ./...
# or directly: go tool buf generate
```

## Architecture

### GOCACHEPROG Protocol

The core of this project implements Go's GOCACHEPROG protocol, which communicates with the Go compiler via JSON messages over stdin/stdout:
- `protocol/` - Protocol implementation that handles get/put/close commands from the Go compiler
- Request/Response types defined in `protocol/model.go`
- Main processing loop in `protocol/proccess.go`

### Layered Cache Architecture

```
Go Compiler <-> Protocol <-> Gocica <-> CombinedBackend
                                             |
                              +--------------+--------------+
                              |                             |
                        LocalBackend              RemoteBackend
                      (internal/backend/)       (internal/backend/)
                              |                             |
                          Disk Cache          GitHub Actions Cache API
                                              (via Azure Blob Storage)
```

- **Protocol** (`protocol/proccess.go`): Handles stdin/stdout JSON protocol with Go compiler
- **Gocica** (`internal/gocica.go`): Main entry point implementing get/put/close handlers
- **CombinedBackend** (`internal/backend/backend.go`): Orchestrates local and remote backends
- **Disk** (`internal/backend/disk.go`): Disk-based local cache storage
- **GitHubActionsCache** (`internal/backend/github_actions_cache.go`): GitHub Actions Cache API client using Azure Blob Storage

### Configuration

CLI configuration via Kong (defined in `main.go`):
- `-d, --dir`: Cache directory (default: user cache dir)
- `-l, --log-level`: Log level (debug/info/warn/error/silent)
- GitHub-related config via environment variables (ACTIONS_RESULTS_URL, ACTIONS_RUNTIME_TOKEN, RUNNER_OS, GITHUB_REF, GITHUB_SHA)

## Key Implementation Details

- Uses `bytedance/sonic` for fast JSON encoding/decoding (via `internal/pkg/json/json.go`)
- Remote uploads happen asynchronously in goroutines via `errgroup`
- Protocol uses base64-encoded body data for binary content
- Build tag `dev` enables profiling features (CPU, memory, mutex, block profiling)
- Protobuf is used for serializing index metadata (`internal/proto/gocica/v1/`)

## Tool Dependencies

Tools are managed via go workspace in `tools/go.mod`:
- buf: Protocol buffer code generation
- golangci-lint: Linting
- goreleaser: Release builds
- protoc-gen-go: Protobuf Go code generator

## Active Technologies
- Go 1.24+ (001-kessoku-di)
- Local disk cache + GitHub Actions Cache (Azure Blob Storage) (001-kessoku-di)

## Recent Changes
- 001-kessoku-di: Added Go 1.24+
