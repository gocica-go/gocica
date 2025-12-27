# Suggested Commands

## Build
```bash
# Build the binary
go build -o gocica .

# Build with dev features (profiling support)
go build -tags=dev -o gocica .
```

## Test
```bash
# Run all tests
go test ./... -v

# Run a single test
go test -v -run TestName ./path/to/package

# Run tests with coverage
go test ./... -v -coverprofile=coverage.txt -race -vet=off
```

## Lint
```bash
# Run linter (uses golangci-lint v2)
go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run
```

## Generate
```bash
# Generate protobuf code
go generate ./...
# or directly:
go tool buf generate
```

## System Commands
- git, ls, cd, grep, find - standard Linux commands
