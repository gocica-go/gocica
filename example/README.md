# Configuration File

gocica supports JSON configuration files. The configuration file can be placed in the following locations:

- Working directory: `./.gocica.json`
- Home directory: `~/.gocica.json`

You can also specify a configuration file using the `-c, --config` flag.

## Configuration Options

```json
{
  "dir": "/path/to/cache",     // Directory to store cache data (default: OS-specific cache directory)
  "log_level": "info",        // Log level: debug, info, warn, error, silent (default: info)
  "remote": "none",           // Remote backend: none, s3, GitHub (default: none)
  "s3": {                     // S3 configuration (required when remote=s3)
    "region": "us-east-1",     // AWS region
    "bucket": "my-bucket",     // S3 bucket name
    "access_key": "xxx",       // AWS access key
    "secret_access_key": "xxx", // AWS secret access key
    "endpoint": "s3.amazonaws.com",  // S3 endpoint (default: s3.amazonaws.com)
    "disable_ssl": false,      // Disable SSL for S3 connection
    "use_path_style": false    // Use path style for S3 connection
  },
  "github": {                 // GitHub configuration (required when remote=GitHub)
    "cache_url": "xxx",       // GitHub Actions Cache URL
    "token": "xxx",           // GitHub token or Actions Runtime token
    "runner_os": "Linux",     // GitHub runner OS
    "ref": "refs/heads/main", // GitHub base ref or target branch
    "sha": "xxx"             // GitHub commit SHA
  }
}
```

## Environment Variables

The following environment variables can be used to configure gocica:

- `GOCICA_DIR`: Directory to store cache data
- `GOCICA_LOG_LEVEL`: Log level (debug, info, warn, error, silent)
- `GOCICA_REMOTE`: Remote backend (none, s3, GitHub)

### S3 Configuration
- `GOCICA_S3_REGION`: AWS region
- `GOCICA_S3_BUCKET`: S3 bucket name
- `GOCICA_S3_ACCESS_KEY`: AWS access key
- `GOCICA_S3_SECRET_ACCESS_KEY`: AWS secret access key
- `GOCICA_S3_ENDPOINT`: S3 endpoint
- `GOCICA_S3_DISABLE_SSL`: Disable SSL for S3 connection
- `GOCICA_S3_USE_PATH_STYLE`: Use path style for S3 connection

### GitHub Configuration
- `GOCICA_GITHUB_CACHE_URL, ACTIONS_RESULTS_URL`: GitHub Actions Cache URL
- `GOCICA_GITHUB_TOKEN, ACTIONS_RUNTIME_TOKEN`: GitHub Actions runtime token
- `GOCICA_GITHUB_RUNNER_OS, RUNNER_OS`: GitHub Actions runner OS
- `GOCICA_GITHUB_REF, GITHUB_REF`: GitHub ref
- `GOCICA_GITHUB_SHA, GITHUB_SHA`: GitHub SHA

## Command Line Flags

- `-v, --version`: Show version and exit
- `-c, --config`: Load configuration from a file
- `-d, --dir`: Directory to store cache files
- `-l, --log-level`: Log level (debug, info, warn, error, silent)
- `-r, --remote`: Remote backend (none, s3, GitHub)
- `--s3.region`: AWS region
- `--s3.bucket`: S3 bucket name
- `--s3.access-key`: AWS access key
- `--s3.secret-access-key`: AWS secret access key
- `--s3.endpoint`: S3 endpoint
- `--s3.disable-ssl`: Disable SSL for S3 connection
- `--s3.use-path-style`: Use path style for S3 connection
- `--github.cache-url`: GitHub Actions Cache URL
- `--github.token`: GitHub token
- `--github.runner-os`: GitHub runner OS
- `--github.ref`: GitHub ref
- `--github.sha`: GitHub SHA

## Priority

1. Command line arguments
2. Environment variables
3. Configuration file
4. Default values