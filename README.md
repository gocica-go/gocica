# GoCICa

GoCICa is a build and module caching tool for Go in CI environments.
The Go compiler's GOCACHEPROG feature is used to provide a cache optimized for GitHub Actions.
The following cache locations are supported:
- GitHub Actions cache(GitHub-hosted runners only)
- S3-compatible storage

Especially when using GitHub Actions cache mechanism, you can achieve 5~10 times build speedup by adding one line.
