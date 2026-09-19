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
