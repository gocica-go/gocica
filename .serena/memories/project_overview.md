# GoCICa Project Overview

## Purpose
GoCICa is a Go compiler build and module caching tool for CI environments. It implements Go's GOCACHEPROG feature to provide a cache optimized for GitHub Actions, storing cache entries both locally on disk and remotely in GitHub Actions Cache.

## Tech Stack
- Language: Go
- CLI Framework: Kong (github.com/alecthomas/kong)
- JSON Processing: bytedance/sonic (fast JSON encoding/decoding)
- Remote Storage: GitHub Actions Cache API (via Azure Blob Storage)
- Protocol Buffers: buf + protoc-gen-go
- Linting: golangci-lint v2
- Release: goreleaser

## Architecture
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

## Key Components
- `main.go` - Entry point with CLI parsing
- `protocol/` - GOCACHEPROG protocol implementation (stdin/stdout JSON)
- `internal/gocica.go` - Main application logic (get/put/close handlers)
- `internal/backend/backend.go` - CombinedBackend orchestrating local and remote
- `internal/backend/disk.go` - Disk-based local cache storage
- `internal/backend/github_actions_cache.go` - GitHub Actions Cache API client
- `internal/proto/gocica/v1/` - Protobuf definitions for index metadata
- `tools/` - Go workspace for tool dependencies
