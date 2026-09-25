module github.com/mazrean/gocica

go 1.27.1

require (
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.8.1
	github.com/DataDog/zstd v1.5.6
	github.com/alecthomas/kong v1.14.0
	github.com/felixge/fgprof v0.9.5
	github.com/google/go-cmp v0.7.0
	github.com/mazrean/kessoku v1.1.0
	github.com/mazrean/odjson v0.2.0
	github.com/prometheus/procfs v0.22.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.23.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.23.1 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.12.0 // indirect
	github.com/google/pprof v0.0.0-20250202011525-fc3143867406 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/DataDog/zstd v1.5.6 => github.com/gocica-go/zstd v1.5.6
