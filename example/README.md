# Configuration File

gocica supports JSON configuration files. The configuration file can be placed in the following locations:

- Working directory: `./.gocica.json`
- Home directory: `~/.gocica.json`

## Configuration Options

```json
{
  "dir": "/path/to/cache",     // Directory to store cache data (default: OS-specific cache directory)
  "logLevel": "info",          // Log level: debug, info, error, none (default: info)
  "s3": {                      // S3 configuration (optional)
    "region": "us-east-1",     // AWS region
    "bucket": "my-bucket",     // S3 bucket name
    "accessKeyId": "xxx",      // AWS access key ID
    "secretAccessKey": "xxx",  // AWS secret access key
    "endpoint": "https://s3.amazonaws.com",  // S3 endpoint (default: https://s3.amazonaws.com)
    "disableSSL": false,       // Disable SSL for S3 connection
    "usePathStyle": false      // Use path style for S3 connection
  }
}
```

## Environment Variables

The following environment variables can be used to configure gocica:

- `GOCICA_DIR`: Directory to store cache data
- `GOCICA_LOG_LEVEL`: Log level (debug, info, error, none)
- `GOCICA_S3_REGION`: AWS region
- `GOCICA_S3_BUCKET`: S3 bucket name
- `GOCICA_S3_ACCESS_KEY_ID`: AWS access key ID
- `GOCIAC_S3_SECRET_ACCESS_KEY`: AWS secret access key
- `GOCICA_S3_ENDPOINT`: S3 endpoint
- `GOCICA_S3_DISABLE_SSL`: Disable SSL for S3 connection
- `GOCICA_S3_USE_PATH_STYLE`: Use path style for S3 connection

## Priority

1. Command line arguments
2. Environment variables
3. Configuration file
4. Default values