# Repository Guidelines

## Project Structure & Module Organization
- Entry point: `main.go` wires CLI parsing, backend selection, and the cache process runner.
- Core logic: `internal/gocica.go` orchestrates cache GET/PUT/CLOSE; `internal/backend` contains disk and GitHub Actions Cache implementations plus the combined backend; `internal/pkg` hosts logging helpers; `internal/metrics` collects profiling/metrics in dev builds.
- Protocol layer: `protocol` holds the GOCACHEPROG process wrapper and tests. `proto` and `buf.gen.yaml` drive protobuf codegen (`go generate ./...` runs `buf generate`).
- Binaries: `dist/` carries prebuilt artifacts. Tooling deps live in `tools/`. Logs and profiles (`*.prof`, `log/`, `out.log`) are disposable.

## Build, Test, and Development Commands
- `go build -tags=dev -o gocica .` — local build with dev-only profiling hooks (matches CI). Drop `-tags=dev` for release-like builds.
- `go run .` — run the cache program directly; supply GitHub cache env vars (`GOCICA_GITHUB_TOKEN`, etc.) when testing remote caching.
- `go test ./... -race -coverprofile=coverage.txt -vet=off` — full test suite with race detector and coverage (CI default).
- `golangci-lint run ./...` — linting (same as CI action). Ensure it passes before opening a PR.
- `go generate ./...` — regenerate protocol code via `buf`; rerun after editing files under `proto/`.

## Coding Style & Naming Conventions
- Standard Go formatting: `gofmt` on save; keep imports sorted. Avoid clever indirection; prefer simple, explicit flows.
- Package layout follows Go conventions: lowercase package names, exported symbols documented with full-sentence comments when part of the public surface.
- Flags/envs mirror CLI fields in `main.go`; prefer descriptive names over abbreviations.

## Testing Guidelines
- Use Go’s testing package; name files `*_test.go` and functions `TestXxx`. Favor table tests for handler/backends to cover edge cases (cache hits/misses, error paths).
- Include race-sensitive cases when touching concurrent code (`-race` is expected to stay green).
- Keep coverage reports (`coverage.txt`) out of commits; they are CI artifacts only.

## Commit & Pull Request Guidelines
- Commit messages: short, imperative summaries; include scope or dependency bump details (e.g., `Bump github.com/...` as seen in history). Squash noisy fixups before review.
- Pull requests: describe behavior changes, risks, and test evidence (`go test ...` output). Link issues when relevant. Add screenshots only if touching user-facing logs/UX (rare here).
- Keep diffs focused; avoid drive-by refactors unless they reduce complexity or fix an actual bug.

## Security & Configuration Tips
- Do not commit tokens or cache artifacts. GitHub cache credentials come from env vars; keep them scoped and ephemeral.
- Default cache path falls back to the user cache dir; override via `-dir` or `GOCICA_DIR` when running locally to avoid polluting system caches.
