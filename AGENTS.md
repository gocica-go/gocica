# gocica

Go build/module cache for CI. Implements the Go toolchain's `GOCACHEPROG` protocol, serving cache
entries from a local disk store backed by GitHub Actions Cache.

## Commands

- Build: `go build -tags=dev -o gocica .` (CI build; drop `-tags=dev` for release — profiling flags disappear)
- Test: `go test ./... -v -race -vet=off`
- Test single: `go test ./internal/local -run TestXxx -v`
- Lint: `go tool lint ./... ./tools/...`
- Format: `gofmt -l .` must print nothing (CI fails on any output)
- Generate: `go generate ./...` (buf → `internal/proto`, kessoku → `internal/kessoku`, odjson → `protocol/odjson_gen.go`)

## Tech Stack

- Go 1.27 (`go.work` spans the root module and `./tools`)
- CLI: `alecthomas/kong` — flags and env vars are struct tags in `main.go` (`GOCICA_*`, with GitHub Actions fallbacks)
- DI: `mazrean/kessoku` (compile-time, `internal/kessoku`)
- Protobuf: `buf` + `protoc-gen-go` for the cache index / metadata format
- Remote: GitHub Actions Cache API; blobs move over Azure Blob SAS URLs the API hands back
- JSON: `encoding/json/v2` + `encoding/jsontext`, accelerated by `mazrean/odjson` codegen; compression: `DataDog/zstd` (forked to `gocica-go/zstd`)

## Project Structure

```
main.go               # kong CLI, flag/env definitions, wiring
dev_flag.go           # profiling flags, dev build tag only
protocol/             # GOCACHEPROG wire types + process loop
log/                  # logger interface (public package)
proto/                # .proto sources for the cache index
internal/cacheprog/   # Get/Put/Close orchestration; ConbinedBackend (local + remote)
internal/local/       # disk backend
internal/remote/      # core (upload/download), provider (GHA Cache), storage (Azure Blob)
internal/proto/       # generated protobuf (do not hand-edit)
internal/kessoku/     # DI: kessoku.go declares, kessoku_band.go is generated
internal/pkg/         # shared helpers (http, io, json, locker, log, metrics)
tools/                # separate module: tool directive + tools/lint
specs/                # feature specs (spec-driven development)
```

## Coding Standards

- Wrap errors with context: `fmt.Errorf("get local cache: %w", err)`. This is the pattern everywhere.
- The cache sits on the compiler's hot path — avoid per-request allocations and extra syscalls.
- A remote failure degrades gracefully: `ConbinedBackend` warns and falls back to local-only rather than failing the build. Keep it that way.
- Generated files (`internal/proto`, `internal/kessoku/kessoku_band.go`, `protocol/odjson_gen.go`) are regenerated, never edited by hand.

## Boundaries

- ALWAYS: run `go tool lint ./... ./tools/...` and the tests before calling work complete.
- ALWAYS: rerun `go generate ./...` after touching `proto/`, a kessoku injector, or the `protocol` JSON structs.
- ASK FIRST: new dependencies, changes to the on-disk cache format, CI workflow edits.
- NEVER: commit tokens, cache artifacts, `coverage.txt`, or profile output.

## Conditional Rules

`.claude/rules/` holds path-scoped rules that load automatically:
`conventions.md` (always) and `go-conventions.md` (Go files).
