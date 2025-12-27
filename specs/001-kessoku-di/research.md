# Research: Kessoku DI Integration

**Date**: 2025-12-27
**Status**: Complete

## Research Tasks

### 1. kessoku Framework Core Concepts

**Task**: Understand kessoku DI framework architecture and API

**Findings**:

kessoku is a compile-time dependency injection code generator for Go with built-in support for parallel execution.

#### Core API Functions

| Function | Purpose | Example |
|----------|---------|---------|
| `kessoku.Inject[T](name, ...providers)` | Declares an injector with return type T | `kessoku.Inject[*App]("InitializeApp", ...)` |
| `kessoku.Provide(fn)` | Marks a constructor as a sequential provider | `kessoku.Provide(NewDatabase)` |
| `kessoku.Async(provider)` | Enables parallel execution for a provider | `kessoku.Async(kessoku.Provide(NewCache))` |
| `kessoku.Bind[Interface](impl)` | Binds an interface to an implementation | `kessoku.Bind[Storage](kessoku.Provide(NewS3Storage))` |
| `kessoku.Value(val)` | Provides a constant value as dependency | `kessoku.Value(myLogger)` |
| `kessoku.Set(...providers)` | Groups multiple providers for reuse | `kessoku.Set(NewDB, NewCache)` |

#### Generated Function Signatures

- **No async, no errors**: `func InitializeApp() *App`
- **With errors**: `func InitializeApp() (*App, error)`
- **With async**: `func InitializeApp(ctx context.Context) *App`
- **With async and errors**: `func InitializeApp(ctx context.Context) (*App, error)`

**Decision**: Use kessoku.Inject with Async for parallel initialization of independent backends
**Rationale**: kessoku provides compile-time DI with zero runtime overhead, plus built-in parallel execution via errgroup
**Alternatives Considered**:
- google/wire: No parallel execution support
- uber-go/fx: Runtime reflection overhead, more complex

---

### 2. External Dependency Injection Pattern

**Task**: How to inject dependencies created outside DI (logger, config values)

**Findings**:

`kessoku.Value` wraps pre-existing values as dependencies:

```go
// Logger created in main based on CLI flags
logger := mylog.NewLogger(mylog.Debug)

// Inject as a value dependency
var _ = kessoku.Inject[*App](
    "InitializeApp",
    kessoku.Value(logger),
    kessoku.Provide(NewApp),
)
```

For configuration values, inject each value individually:

```go
var _ = kessoku.Inject[*App](
    "InitializeApp",
    kessoku.Value(cacheDir),      // string
    kessoku.Value(githubToken),   // string
    kessoku.Value(cacheURL),      // string
    // ...
)
```

**Decision**: Inject logger and individual config values via kessoku.Value
**Rationale**: Keeps CLI parsing outside DI scope while allowing injected components to receive configuration
**Alternatives Considered**:
- Inject entire CLI struct: Less granular, harder to test
- Create config providers: Unnecessary complexity for static values

---

### 3. Interface Binding Pattern

**Task**: How to bind interfaces to implementations for testability

**Findings**:

`kessoku.Bind[Interface]` maps interface types to implementations:

```go
var _ = kessoku.Inject[*MyService](
    "InitializeService",
    kessoku.Bind[Backend](kessoku.Provide(NewCombinedBackend)),
    kessoku.Provide(NewMyService), // Receives Backend interface
)
```

For GoCICa, the following interfaces need binding:
- `backend.Backend` -> `*backend.ConbinedBackend`
- `backend.LocalBackend` -> `*backend.Disk`
- `backend.RemoteBackend` -> `*backend.GitHubActionsCache`
- `blob.UploadClient` -> `*blob.AzureUploadClient`
- `blob.DownloadClient` -> `*blob.AzureDownloadClient`

**Decision**: Use kessoku.Bind for all interface types listed in FR-008
**Rationale**: Enables test mocking without modifying production code
**Alternatives Considered**: None - this is the standard kessoku pattern for interface binding

---

### 4. Parallel Initialization Strategy

**Task**: Identify which components can initialize in parallel

**Findings**:

Analyzing the dependency graph:

```
                    +---------+
                    |  Logger |  (kessoku.Value)
                    +----+----+
                         |
         +---------------+---------------+
         |                               |
         v                               v
    +----+-----+                  +------+------+
    |   Disk   |                  | GitHubCache |
    | (Async)  |                  |   (Async)   |
    +----+-----+                  +------+------+
         |                               |
         +---------------+---------------+
                         |
                         v
               +---------+---------+
               | ConbinedBackend   |
               +--------+----------+
                        |
                        v
                  +-----+-----+
                  |  Gocica   |
                  +-----+-----+
                        |
                        v
                  +-----+-----+
                  |  Process  |
                  +-----------+
```

**Parallel candidates** (no inter-dependencies):
- `Disk` initialization (directory creation)
- `GitHubActionsCache` setup (HTTP client, URL parsing, blob client creation)

**Sequential requirements**:
- `ConbinedBackend` depends on both Disk and RemoteBackend
- `Gocica` depends on ConbinedBackend
- `Process` depends on Gocica handlers

**Decision**: Mark Disk and GitHubActionsCache providers with kessoku.Async
**Rationale**: Maximizes parallel initialization while respecting dependencies
**Alternatives Considered**: Sequential initialization - would work but miss optimization opportunity

---

### 5. Error Handling Strategy

**Task**: How to handle errors from providers

**Findings**:

kessoku automatically handles error propagation:

1. **Sequential providers**: `if err != nil { return nil, err }` generated after each call
2. **Async providers**: Errors collected via errgroup, first error returned by `eg.Wait()`
3. **Injector signature**: Returns `(T, error)` if any provider can fail

For GoCICa:
- Backend initialization failure should log warning and continue with no-cache mode
- This matches existing behavior in `createBackend()`

**Implementation approach**:
```go
// In main.go
backend, err := InitializeBackend(ctx, logger, config...)
if err != nil {
    logger.Warnf("failed to create backend: %v. no cache will be used.", err)
    // Continue without backend handlers
}
```

**Decision**: Let injector return errors, handle degraded mode in main
**Rationale**: Preserves existing behavior (FR-007) while using idiomatic kessoku patterns
**Alternatives Considered**: Wrap providers to suppress errors - would hide important failures

---

### 6. File Organization

**Task**: Where to place kessoku.Inject declarations

**Findings**:

kessoku requires `//go:generate go tool kessoku $GOFILE` directive in the file containing `kessoku.Inject` calls.

Options:
1. **Single injector.go at root**: All DI declarations in one place
2. **Per-package injectors**: Distributed across packages
3. **In main.go**: Mixed with CLI code

**Decision**: Create `injector.go` at repository root
**Rationale**:
- Keeps all DI logic in one place for easy understanding
- Separates concerns from CLI parsing (main.go)
- Generated `injector_band.go` appears alongside
**Alternatives Considered**:
- Per-package: More complex, harder to understand dependency graph
- In main.go: Mixes concerns, harder to test

---

### 7. Tool Installation

**Task**: How to add kessoku as a tool dependency

**Findings**:

Add to `tools/go.mod`:
```go
require github.com/mazrean/kessoku v1.x.x
```

Run code generation:
```bash
go generate ./...
# or: go tool kessoku injector.go
```

Generated files should be committed (FR-010) for reproducible builds.

**Decision**: Add kessoku to tools/go.mod, commit generated files
**Rationale**: Consistent with existing tool management pattern (buf, golangci-lint)
**Alternatives Considered**: Global installation - less reproducible across environments

---

## Summary

All research tasks complete. Key decisions:

1. **DI Framework**: kessoku with compile-time code generation
2. **External Dependencies**: Use kessoku.Value for logger and config values
3. **Interface Binding**: Use kessoku.Bind for all interfaces (5 total)
4. **Parallel Init**: Async for Disk and GitHubActionsCache
5. **Error Handling**: Injector returns errors, main handles degraded mode
6. **File Organization**: Single injector.go at repository root
7. **Tool Management**: Add to tools/go.mod, commit generated files
