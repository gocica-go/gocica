# Feature Specification: Kessoku DI Integration

**Feature Branch**: `001-kessoku-di`
**Created**: 2025-12-27
**Status**: Draft
**Input**: User description: "mazrean/kessokuによるDIで初期化を行うようにして。mazrean/kessokuについてはDeepwiki MCPを用いて調査して。"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Developer Uses DI for Application Initialization (Priority: P1)

As a developer maintaining GoCICa, I want the application initialization to be managed by kessoku DI framework so that dependencies are clearly defined, testable, and initialization can be parallelized where possible.

**Why this priority**: This is the core functionality that replaces manual initialization with DI-managed initialization. All other improvements depend on this foundation.

**Independent Test**: Can be fully tested by running `go generate ./...` to generate DI code and verifying the application starts correctly with the same behavior as before.

**Acceptance Scenarios**:

1. **Given** the developer has added kessoku annotations to the codebase, **When** `go generate ./...` is run, **Then** kessoku generates `*_band.go` files with injector functions
2. **Given** the generated injector exists, **When** the application starts, **Then** all components (Disk, GitHubActionsCache, CombinedBackend, Gocica, Process) are initialized correctly
3. **Given** the DI-managed initialization is in place, **When** running the application, **Then** the behavior is identical to the current manual initialization

---

### User Story 2 - Parallel Backend Initialization (Priority: P2)

As a CI system using GoCICa, I want independent backend components to initialize in parallel so that application startup time is minimized.

**Why this priority**: Parallel initialization provides performance benefits, but requires the P1 DI foundation to be in place first.

**Independent Test**: Can be tested by adding trace logging to each provider and verifying concurrent execution via log timestamps.

**Acceptance Scenarios**:

1. **Given** Disk backend and GitHub Actions Cache setup are independent, **When** the application starts, **Then** these operations execute concurrently using `kessoku.Async`
2. **Given** CombinedBackend depends on both backends, **When** initializing, **Then** it waits for both Disk and RemoteBackend to complete before starting
3. **Given** parallel initialization is enabled, **When** the application starts, **Then** trace logs show overlapping timestamps for independent providers

---

### User Story 3 - Testable Component Wiring (Priority: P3)

As a developer writing tests, I want to be able to easily swap implementations for testing so that I can test components in isolation.

**Why this priority**: Improved testability is a secondary benefit of DI that enhances developer experience but is not essential for basic functionality.

**Independent Test**: Can be tested by creating a test injector with mock implementations and verifying compilation succeeds.

**Acceptance Scenarios**:

1. **Given** interfaces Backend, LocalBackend, RemoteBackend, UploadClient, DownloadClient are bound via `kessoku.Bind`, **When** writing tests, **Then** mock implementations can be provided to the injector
2. **Given** the DI framework generates type-safe code, **When** a required dependency is missing, **Then** compilation fails with a clear error message

---

### Edge Cases

- What happens when configuration is invalid (e.g., missing GitHub token)?
  - The backend injector returns an error; main logs a warning and continues with no-cache mode (current behavior preserved)
- What happens when a provider returns an error during initialization?
  - Error propagates through the injector and is returned to main; main decides whether to continue degraded or exit
- What happens when async providers fail concurrently?
  - errgroup captures the first error; all goroutines are allowed to complete; the first error is returned
- What happens when context is cancelled during async initialization?
  - Providers receiving context should respect cancellation; errgroup propagates context cancellation as an error
- What is the cleanup ordering on error?
  - No cleanup is performed by the injector; cleanup is the responsibility of the caller (main) based on what was successfully initialized
- How do injector errors surface to main?
  - Injector returns `(*protocol.Process, error)`; main handles the error (log warning + degraded mode or panic depending on severity)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST use kessoku DI framework for component initialization instead of manual constructor calls in `createBackend()` and related code
- **FR-002**: System MUST define an injector function `InitializeProcess(ctx context.Context) (*protocol.Process, error)` that accepts a context for cancellation support
- **FR-003**: System MUST define providers for all major components:
  - Logger (injected via `kessoku.Value` as it is created outside DI based on CLI log level)
  - Disk (implements LocalBackend)
  - GitHubActionsCache (implements RemoteBackend), including:
    - blob.Uploader
    - blob.Downloader
    - blob.AzureUploadClient (implements blob.UploadClient)
    - blob.AzureDownloadClient (implements blob.DownloadClient)
  - CombinedBackend (note: code uses `ConbinedBackend` spelling, preserve this)
  - Gocica
  - Process (with ProcessOptions)
- **FR-004**: System MUST use `kessoku.Async` for providers that can run independently:
  - Disk initialization (directory creation)
  - GitHub Actions Cache client setup (HTTP client, URL parsing)
- **FR-005**: System MUST properly handle and propagate errors from provider functions through the injector, returning error to caller
- **FR-006**: System MUST add `//go:generate go tool github.com/mazrean/kessoku/cmd/kessoku $GOFILE` directive to the file containing kessoku.Inject declarations
- **FR-007**: System MUST maintain the same runtime behavior as the current manual initialization, including:
  - Degraded mode (no-cache) when backend initialization fails
  - Warning log on backend failure, not panic
  - DevFlag profiling start/stop remains in main (outside DI scope)
- **FR-008**: System MUST use `kessoku.Bind` for the following interfaces to enable test mocking:
  - `backend.Backend`
  - `backend.LocalBackend`
  - `backend.RemoteBackend`
  - `blob.UploadClient`
  - `blob.DownloadClient`
- **FR-009**: System MUST inject configuration values individually using `kessoku.Value`:
  - Dir (cache directory path)
  - LogLevel (string)
  - GitHub.Token, GitHub.CacheURL, GitHub.RunnerOS, GitHub.Ref, GitHub.Sha
- **FR-010**: Generated `*_band.go` files MUST be committed to the repository (not gitignored) for reproducible builds
- **FR-011**: The following remain OUTSIDE DI scope (manual in main):
  - CLI parsing via Kong
  - Default directory resolution
  - Log level selection and logger creation based on CLI input
  - DevFlag profiling (StartProfiling/StopProfiling)

### Key Entities

- **Injector**: Generated function `InitializeProcess(ctx context.Context)` that wires all dependencies and returns `(*protocol.Process, error)`
- **Provider**: Individual constructor functions wrapped with `kessoku.Provide` or `kessoku.Async(kessoku.Provide(...))`
- **Configuration Values**: Individual CLI struct field values injected via `kessoku.Value`
- **ProcessOptions**: Slice of `protocol.ProcessOption` built from initialized components

## Clarifications

### Session 2025-12-27

- Q: LoggerはDIで管理するか？　→ A: Loggerはmainで作成し、kessoku.Valueで外部依存として注入する（作成はDI外、注入はDI経由）
- Q: 設定値のプロバイダーへの注入方法は？　→ A: 個別の値をkessoku.Valueで注入（Dir、Token等を別々に）
- Q: リモートバックエンドをオプショナルにするか？　→ A: 現状の動作を維持（バックエンド初期化失敗時は警告ログを出してキャッシュなしモードで継続）
- Q: ProcessもDIグラフに含めるか？　→ A: ProcessもDIグラフに含め、インジェクターが直接Processを返す
- Q: blobパッケージのコンポーネントはDI対象か？　→ A: Uploader、Downloader、AzureUploadClient、AzureDownloadClientもDI対象に含める
- Q: CLI解析やDevFlagはDI対象か？　→ A: CLI解析、デフォルトディレクトリ解決、ログレベル選択、DevFlagプロファイリングはDI対象外（mainで手動管理）
- Q: 生成されたファイルの扱いは？　→ A: `*_band.go`ファイルはリポジトリにコミットする（再現可能なビルドのため）

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Application initialization uses kessoku-generated code and produces identical behavior to manual initialization (verified by existing test suite passing)
- **SC-002**: All existing tests pass after migration to DI-managed initialization
- **SC-003**: Application startup time does not regress (measured by benchmark comparing before/after, tolerance: +5%)
- **SC-004**: Test code can compile with mock implementations injected via separate test injector (verified by adding at least one integration test using mocks)
- **SC-005**: Running `go generate ./...` successfully generates `*_band.go` files without errors
- **SC-006**: Missing dependency at compile time produces clear error message from kessoku (verified by intentionally omitting a provider and checking error)
