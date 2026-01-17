module github.com/mazrean/gocica

go 1.24.1

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.21.0
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.6.3
	github.com/alecthomas/kong v1.13.0
	github.com/bytedance/sonic v1.14.2
	github.com/google/go-cmp v0.7.0
	golang.org/x/oauth2 v0.34.0
	golang.org/x/sync v0.19.0
	google.golang.org/protobuf v1.36.10
)

require (
	github.com/DataDog/zstd v1.5.6
	github.com/felixge/fgprof v0.9.5
	github.com/mazrean/kessoku v1.0.1-0.20251228025041-cb56d3314c27
	github.com/prometheus/procfs v0.19.2
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic/loader v0.4.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/google/pprof v0.0.0-20240227163752-401108e1b7e7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	golang.org/x/arch v0.14.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/text v0.33.0 // indirect
)

replace github.com/DataDog/zstd v1.5.6 => github.com/gocica-go/zstd v1.5.6
