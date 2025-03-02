# Configuration File

gocica supports JSON configuration files. The configuration file can be placed in the following locations:

- Working directory: `./.gocica.json`
- Home directory: `~/.gocica.json`

You can also specify a configuration file using the `--config` flag.

## Configuration Options

```json
{
  "dir": "/path/to/cache",     // Directory to store cache data (default: OS-specific cache directory)
  "log_level": "info",        // Log level: debug, info, error, none (default: info)
  "remote": "none",           // Remote backend: none, s3, GitHub (default: none)
  "s3": {                     // S3 configuration (required when remote=s3)
    "region": "us-east-1",     // AWS region
    "bucket": "my-bucket",     // S3 bucket name
    "access_key": "xxx",       // AWS access key
    "secret_access_key": "xxx", // AWS secret access key
    "endpoint": "https://s3.amazonaws.com",  // S3 endpoint (default: https://s3.amazonaws.com)
    "disable_ssl": false,      // Disable SSL for S3 connection
    "use_path_style": false    // Use path style for S3 connection
  },
  "github": {                 // GitHub configuration (required when remote=GitHub)
    "token": "xxx"            // GitHub token or Actions Runtime token
  }
}
```

## Environment Variables

The following environment variables can be used to configure gocica:

- `GOCICA_DIR`: Directory to store cache data
- `GOCICA_LOG_LEVEL`: Log level (debug, info, error, none)
- `GOCICA_REMOTE`: Remote backend (none, s3, GitHub)

### S3 Configuration
- `GOCICA_S3_REGION`: AWS region
- `GOCICA_S3_BUCKET`: S3 bucket name
- `GOCICA_S3_ACCESS_KEY`: AWS access key
- `GOCIAC_S3_SECRET_ACCESS_KEY`: AWS secret access key
- `GOCICA_S3_ENDPOINT`: S3 endpoint
- `GOCICA_S3_DISABLE_SSL`: Disable SSL for S3 connection
- `GOCICA_S3_USE_PATH_STYLE`: Use path style for S3 connection

### GitHub Configuration
- `GOCICA_GITHUB_TOKEN`,`ACTIONS_RUNTIME_TOKEN`: GitHub token

## Command Line Flags

- `-v`, `--version`: Show version and exit
- `-c`, `--config`: Load configuration from a file
- `-d`, `--dir`: Directory to store cache files
- `-l`, `--log-level`: Log level (debug, info, error, none)
- `-r`, `--remote`: Remote backend (none, s3, GitHub)
- `--s3.region`: AWS region
- `--s3.bucket`: S3 bucket name
- `--s3.access-key`: AWS access key
- `--s3.secret-access-key`: AWS secret access key
- `--s3.endpoint`: S3 endpoint
- `--s3.disable-ssl`: Disable SSL for S3 connection
- `--s3.use-path-style`: Use path style for S3 connection
- `--github.token`: GitHub token

## Priority

1. Command line arguments
2. Environment variables
3. Configuration file
4. Default values