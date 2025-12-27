# Tasks: Kessoku DI Integration

**Input**: Design documents from `/specs/001-kessoku-di/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, quickstart.md

**Tests**: Unit/integration test creation not explicitly requested. Existing test suite validation via `go test` is included to verify behavior preservation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: User story label (US1, US2, US3) - included ONLY in user story phases (Phase 3+), omitted in Setup/Foundational/Polish phases
- Include exact file paths in descriptions

## Path Conventions

- **Single project**: Repository root for injector, `internal/` for backend components
- Paths follow existing project structure per plan.md

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add kessoku tool dependency and prepare project structure

- [ ] T001 Add kessoku dependency to tools/go.mod
- [ ] T002 Run `go mod tidy` in tools/ directory to update tools/go.sum
- [ ] T003 Verify kessoku tool installation by running `go tool github.com/mazrean/kessoku/cmd/kessoku --help` in repository root

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Prepare existing code for DI integration - constructor signatures and interface exposure

**CRITICAL**: No user story work can begin until this phase is complete

- [ ] T004 [P] Export blob.UploadClient interface in internal/backend/blob/upload.go (if not already exported)
- [ ] T005 [P] Export blob.DownloadClient interface in internal/backend/blob/download.go (if not already exported)
- [ ] T006 Create blob.NewAzureUploadClient constructor in internal/backend/blob/azure_blob_storage.go (accepts token, cacheURL string)
- [ ] T007 Create blob.NewAzureDownloadClient constructor in internal/backend/blob/azure_blob_storage.go (depends on T006, same file)
- [ ] T008 [P] Create blob.NewUploader constructor in internal/backend/blob/upload.go (accepts UploadClient)
- [ ] T009 [P] Create blob.NewDownloader constructor in internal/backend/blob/download.go (accepts DownloadClient)
- [ ] T010 Update backend.NewGitHubActionsCache signature in internal/backend/github_actions_cache.go to accept DI parameters: logger, token, cacheURL, runnerOS, ref, sha, localBackend, uploader, downloader
- [ ] T011 Update backend.NewConbinedBackend signature in internal/backend/backend.go to accept (logger, LocalBackend, RemoteBackend) if not already
- [ ] T012 Update internal.NewGocica signature in internal/gocica.go to accept (logger, Backend) if not already
- [ ] T013 Create NewProcessWithOptions wrapper function in injector.go (accepts logger, *Gocica, returns *protocol.Process)

**Checkpoint**: Foundation ready - constructor signatures compatible with DI

---

## Phase 3: User Story 1 - Developer Uses DI for Application Initialization (Priority: P1)

**Goal**: Replace manual component initialization in main.go with kessoku DI framework

**Independent Test**: Run `go generate ./...` successfully, then `go build` and verify application starts correctly

### Implementation for User Story 1

- [ ] T014 [US1] Create injector.go at repository root with `//go:generate go tool github.com/mazrean/kessoku/cmd/kessoku $GOFILE` directive
- [ ] T015 [US1] Add kessoku.Value declaration for logger (log.Logger) in injector.go
- [ ] T016 [US1] Add kessoku.Value declaration for logLevel (string) in injector.go (FR-009)
- [ ] T017 [US1] Add kessoku.Value declaration for dir (string) in injector.go
- [ ] T018 [US1] Add kessoku.Value declaration for token (string) in injector.go
- [ ] T019 [US1] Add kessoku.Value declaration for cacheURL (string) in injector.go
- [ ] T020 [US1] Add kessoku.Value declaration for runnerOS (string) in injector.go
- [ ] T021 [US1] Add kessoku.Value declaration for ref (string) in injector.go
- [ ] T022 [US1] Add kessoku.Value declaration for sha (string) in injector.go
- [ ] T023 [US1] Add kessoku.Provide for Disk provider in injector.go
- [ ] T024 [US1] Add kessoku.Provide for AzureUploadClient provider in injector.go
- [ ] T025 [US1] Add kessoku.Provide for AzureDownloadClient provider in injector.go
- [ ] T026 [US1] Add kessoku.Provide for Uploader provider in injector.go
- [ ] T027 [US1] Add kessoku.Provide for Downloader provider in injector.go
- [ ] T028 [US1] Add kessoku.Provide for GitHubActionsCache provider in injector.go
- [ ] T029 [US1] Add kessoku.Provide for ConbinedBackend provider in injector.go
- [ ] T030 [US1] Add kessoku.Provide for Gocica provider in injector.go
- [ ] T031 [US1] Add kessoku.Provide for Process provider (NewProcessWithOptions) in injector.go
- [ ] T032 [US1] Assemble complete kessoku.Inject[*protocol.Process] declaration with signature `InitializeProcess(ctx context.Context) (*protocol.Process, error)` in injector.go (FR-002)
- [ ] T033 [US1] Run `go generate ./...` to create injector_band.go at repository root
- [ ] T034 [US1] Verify injector_band.go contains InitializeProcess(ctx context.Context) function at repository root
- [ ] T035 [US1] Update main.go to call InitializeProcess(context.Background()) instead of manual initialization
- [ ] T036 [US1] Implement degraded mode handling in main.go (log warning on error, continue with no-cache Process per FR-007)
- [ ] T037 [US1] Verify provider error propagation by testing InitializeProcess with invalid config in main.go (FR-005 validation)
- [ ] T038 [US1] Remove old createBackend function from main.go (if exists)
- [ ] T039 [US1] Run `go build -o gocica .` at repository root to verify compilation succeeds
- [ ] T040 [US1] Run `go test ./... -v` at repository root to verify identical behavior (FR-007)

**Checkpoint**: Application initializes via kessoku DI with same behavior as before

---

## Phase 4: User Story 2 - Parallel Backend Initialization (Priority: P2)

**Goal**: Enable concurrent initialization of independent providers using kessoku.Async

**Independent Test**: Add trace logging and verify overlapping timestamps for Disk/AzureUploadClient/AzureDownloadClient

### Implementation for User Story 2

- [ ] T041 [US2] Wrap Disk provider with kessoku.Async in injector.go
- [ ] T042 [US2] Wrap AzureUploadClient provider with kessoku.Async in injector.go
- [ ] T043 [US2] Wrap AzureDownloadClient provider with kessoku.Async in injector.go
- [ ] T044 [US2] Run `go generate ./...` to regenerate injector_band.go at repository root
- [ ] T045 [US2] Run `go build -o gocica .` at repository root to verify compilation succeeds
- [ ] T046 [US2] Verify parallel initialization by running `./gocica -l debug` and checking log timestamps

**Checkpoint**: Independent providers initialize in parallel, reducing startup time

---

## Phase 5: User Story 3 - Testable Component Wiring (Priority: P3)

**Goal**: Enable test mocking by binding interfaces with kessoku.Bind

**Independent Test**: Verify compilation with mock implementations (or intentionally omit provider to see compile error)

### Implementation for User Story 3

- [ ] T047 [US3] Wrap Disk provider with kessoku.Bind[backend.LocalBackend] in injector.go
- [ ] T048 [US3] Wrap GitHubActionsCache provider with kessoku.Bind[backend.RemoteBackend] in injector.go
- [ ] T049 [US3] Wrap ConbinedBackend provider with kessoku.Bind[backend.Backend] in injector.go
- [ ] T050 [US3] Wrap AzureUploadClient provider with kessoku.Bind[blob.UploadClient] in injector.go
- [ ] T051 [US3] Wrap AzureDownloadClient provider with kessoku.Bind[blob.DownloadClient] in injector.go
- [ ] T052 [US3] Run `go generate ./...` to regenerate injector_band.go at repository root
- [ ] T053 [US3] Run `go build -o gocica .` at repository root to verify compilation succeeds
- [ ] T054 [US3] Verify missing dependency produces compile error by temporarily removing a provider in injector.go (SC-006 validation)
- [ ] T055 [US3] Create test injector example in internal/backend/backend_test.go with mock implementations to verify testability (SC-004 validation)

**Checkpoint**: All 5 interfaces bound for test mocking, compile-time validation works

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Final validation and cleanup

- [ ] T056 Commit injector_band.go to repository per FR-010 (run `git add injector_band.go`)
- [ ] T057 Run `go test ./... -v -race` at repository root to verify full test suite passes
- [ ] T058 Verify startup time does not regress by more than 5% by comparing before/after timing (SC-003)
- [ ] T059 Update CLAUDE.md at repository root with any new development commands if needed
- [ ] T060 Run `go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run` at repository root to verify no linting issues

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion - BLOCKS all user stories
- **User Story 1 (Phase 3)**: Depends on Foundational phase completion
- **User Story 2 (Phase 4)**: Builds on US1 (adds Async wrappers to existing providers)
- **User Story 3 (Phase 5)**: Builds on US1/US2 (adds Bind wrappers to existing providers)
- **Polish (Phase 6)**: Depends on all user stories being complete

### User Story Dependencies

- **User Story 1 (P1)**: Core DI implementation with context.Context signature - required foundation for US2 and US3
- **User Story 2 (P2)**: Adds kessoku.Async - builds on US1 injector (context already available)
- **User Story 3 (P3)**: Adds kessoku.Bind - builds on US1/US2 injector

**Note**: US1 includes the context.Context parameter from the start (per FR-002), so US2 only adds Async wrappers.

### Within Each User Story

- Provider declarations before Inject assembly
- Run go generate after modifying injector.go
- Verify compilation before moving to next task
- Run tests at checkpoint

### Parallel Opportunities

**Phase 2 (Foundational)**:
- T004, T005, T008, T009 can all run in parallel (different files)
- T006 and T007 must be sequential (same file: azure_blob_storage.go)

**Phase 3 (US1 - Value declarations)**:
- T015 through T022 can be done together (all are kessoku.Value additions to injector.go)
- T023 through T031 can be done together (all are kessoku.Provide additions to injector.go)

**Phase 4 (US2)**:
- T041, T042, T043 can be done together (adding Async wrappers to injector.go)

**Phase 5 (US3)**:
- T047 through T051 can be done together (adding Bind wrappers to injector.go)

---

## Parallel Example: Phase 2 Foundational

```bash
# Launch interface/constructor preparations in parallel (different files):
Task: "Export blob.UploadClient interface in internal/backend/blob/upload.go"
Task: "Export blob.DownloadClient interface in internal/backend/blob/download.go"
Task: "Create blob.NewUploader constructor in internal/backend/blob/upload.go"
Task: "Create blob.NewDownloader constructor in internal/backend/blob/download.go"

# Then sequentially (same file):
Task: "Create blob.NewAzureUploadClient constructor in internal/backend/blob/azure_blob_storage.go"
Task: "Create blob.NewAzureDownloadClient constructor in internal/backend/blob/azure_blob_storage.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (add kessoku dependency)
2. Complete Phase 2: Foundational (prepare constructors)
3. Complete Phase 3: User Story 1 (basic DI integration with context.Context signature)
4. **STOP and VALIDATE**: Test that application works identically to before
5. Can deploy/use at this point - DI foundation is complete with proper signature

### Incremental Delivery

1. Complete Setup + Foundational -> Ready for DI
2. Add User Story 1 -> Basic DI with context.Context -> Validate (MVP!)
3. Add User Story 2 -> Parallel initialization -> Validate performance
4. Add User Story 3 -> Interface bindings + test injector -> Validate testability
5. Polish -> Final validation and cleanup

### Single Developer Strategy

Execute phases sequentially:
1. Phase 1: Setup (~5 min)
2. Phase 2: Foundational (~30 min)
3. Phase 3: User Story 1 (~1 hour)
4. Phase 4: User Story 2 (~15 min)
5. Phase 5: User Story 3 (~20 min)
6. Phase 6: Polish (~15 min)

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- Run `go generate ./...` after any injector.go changes
- Generated injector_band.go must be committed (FR-010)
- Verify tests pass at each checkpoint
- Commit after each completed phase
- US1 includes context.Context parameter from the start per FR-002
