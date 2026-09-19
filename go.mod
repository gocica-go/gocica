module github.com/mazrean/gocica

go 1.27.1

require (
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.6.4
	github.com/DataDog/zstd v1.5.6
	github.com/alecthomas/kong v1.14.0
	github.com/felixge/fgprof v0.9.5
	github.com/google/go-cmp v0.7.0
	github.com/mazrean/kessoku v1.1.0
	github.com/mazrean/odjson v0.2.0
	github.com/prometheus/procfs v0.19.2
	golang.org/x/oauth2 v0.34.0
	golang.org/x/sync v0.23.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.21.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/google/pprof v0.0.0-20250202011525-fc3143867406 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/DataDog/zstd v1.5.6 => github.com/gocica-go/zstd v1.5.6
