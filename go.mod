module github.com/mazrean/gocica

go 1.24.1

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.19.1
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.6.1
	github.com/alecthomas/kong v1.12.1
	github.com/bytedance/sonic v1.13.2
	github.com/google/go-cmp v0.7.0
	golang.org/x/oauth2 v0.30.0
	golang.org/x/sync v0.16.0
	google.golang.org/protobuf v1.36.9
)

require (
	github.com/DataDog/zstd v1.5.6
	github.com/felixge/fgprof v0.9.5
	github.com/prometheus/procfs v0.17.0
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/bytedance/sonic/loader v0.2.4 // indirect
	github.com/cloudwego/base64x v0.1.5 // indirect
	github.com/google/pprof v0.0.0-20240227163752-401108e1b7e7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	golang.org/x/arch v0.14.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/text v0.28.0 // indirect
)

replace github.com/DataDog/zstd v1.5.6 => github.com/gocica-go/zstd v1.5.6
