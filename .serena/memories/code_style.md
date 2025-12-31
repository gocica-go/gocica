# Code Style and Conventions

## Language
- Go (latest)
- Go modules with workspace (go.work)

## Style
- Standard Go code style (gofmt)
- golangci-lint v2 for linting
- Minimal comments, self-documenting code
- Error wrapping with fmt.Errorf and %w

## Patterns
- Interfaces defined close to usage
- Constructor functions: NewXxx pattern
- Context passing for cancellation
- errgroup for concurrent operations

## Naming
- Package names: lowercase, short
- Interfaces: Xxx (Backend, LocalBackend, RemoteBackend)
- Structs: PascalCase
- Private: camelCase

## Build Tags
- `dev` tag for profiling features
