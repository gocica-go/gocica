# Task Completion Checklist

When a task is completed, run the following:

1. **Format check** (automatic with gofmt/goimports)
   ```bash
   go fmt ./...
   ```

2. **Lint**
   ```bash
   go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run
   ```

3. **Test**
   ```bash
   go test ./... -v
   ```

4. **Build verification**
   ```bash
   go build -o gocica .
   ```

5. If protobuf changes were made:
   ```bash
   go generate ./...
   ```
