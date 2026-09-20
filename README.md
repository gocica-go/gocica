# GoCICa

GoCICa is a build and module caching tool for Go in CI environments.

It implements two things on top of the GitHub Actions cache:

- the Go toolchain's `GOCACHEPROG` protocol, for the **build cache**, and
- the `GOPROXY` protocol, for the **module cache**.

Together they replace `actions/setup-go`'s cache entirely, so set `cache: false`.

## Usage

```yaml
- uses: actions/setup-go@v6
  with:
    go-version-file: go.mod
    cache: false # GoCICa serves both caches
- uses: gocica-go/gocica-action@v0.1.0-alpha10
- run: go build ./...
```

The action exports `GOCACHEPROG`, starts the module proxy, points `GOPROXY` at it,
and flushes both caches in its post step.

### Without the action

```sh
gocica serve --export-github-env &   # writes GOPROXY to $GITHUB_ENV
export GOCACHEPROG=gocica
go build ./...
gocica proxy-stop                    # flush the module cache
```

## Commands

| Command | Purpose |
|---|---|
| `gocica` | Serve the `GOCACHEPROG` protocol over stdin/stdout. This is the default, so an existing `GOCACHEPROG=gocica` keeps working. |
| `gocica serve` | Serve `GOPROXY` for the module cache. |
| `gocica proxy-stop` | Flush the module cache and stop `serve`. |

Run `gocica --help` for the flags; every one of them also reads a `GOCICA_*`
environment variable.

## How the module proxy behaves

- **A failure never breaks the build.** The exported value is
  `GOPROXY=http://127.0.0.1:<port>|<your previous GOPROXY>`. The `|` separator
  makes the go command fall back on *any* error, not just 404 and 410, so a proxy
  that never starts or dies mid-build is invisible.
- **Modules come back already extracted.** Before it reports ready, the daemon
  restores every cached module into `GOMODCACHE` in extracted form, so a warm
  `go build` unzips nothing. Measured against `tailscale/tailscale`, extracting
  them in the go command instead takes `go mod download` from about 1s to
  7-13s, or a build-only `go build` from 12-14s to 20-28s.
  `--no-extracted-module-cache` turns it off and keeps the ~400MB of trees out
  of the blob.
- **The daemon does not need the toolchain.** When `go` is not on `PATH` yet it
  resolves `GOMODCACHE` by the go command's own default rule, so `gocica serve`
  can start before `actions/setup-go` and warm up while the toolchain downloads.
- **Only immutable things are cached**: `.info`, `.mod` and `.zip` at canonical
  versions. `@v/list`, `@latest`, `/sumdb/…` and `golang.org/toolchain` are
  answered with 404 so the go command resolves them itself. In particular,
  GoCICa never acts as a checksum database proxy: your `GOSUMDB` and `go.sum`
  verification are untouched.
- **Modules matched by `GONOPROXY` or `GOPRIVATE` never reach GoCICa.** The go
  command bypasses every proxy for those, so private modules are not cached.
- **The proxy listens on loopback only.** The go command cannot authenticate to a
  plain HTTP proxy, so the network boundary is the only access control there is.
  On a GitHub-hosted runner that boundary is the job; on a shared self-hosted
  runner, anything else on the machine can read the cache.

## Performance

Measured by `.github/workflows/perf.yaml` against `tailscale/tailscale`,
`go build ./cmd/...`, on `ubuntu-latest`. The number is the job's wall time, not
the build command: GoCICa warms its caches before the go command starts, and
`actions/setup-go` restores and saves in its own steps, so timing only `go build`
hides both.

| | GoCICa | setup-go cache |
|---|--:|--:|
| cold | 189s | 182s |
| warm | 42s | 44s |
| warm, one dependency changed | **149s** | 201s |

**Warm runs are a wash, and the reason is structural.** GoCICa has no
cache-restore step, where `setup-go` spends around 22s on one; it spends about
the same warming its own caches before the go command starts. Roughly 1.4 GB has
to reach the disk either way, and that is the floor. Repetitions of the same
measurement span 37-48s against 38-50s, so treat any single warm comparison as
a tie.

**A changed dependency is a different matter.** `setup-go`'s cache key is the
hash of `go.sum` and it has no restore keys, so one moved module throws away the
module cache and the build cache together: it refetched and re-extracted every
module (19.4s against 2.5s) and rebuilt from nothing (156s against 119s).
GoCICa's restore key chain still finds the previous blob and fetches only what
moved -- walking tailscale v1.84.0 to v1.86.0 to v1.88.0, 48 and 67 modules out
of about 1500.
