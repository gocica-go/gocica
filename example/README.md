# Configuration File

gocica supports JSON configuration files. The configuration file can be placed in the following locations:

- Working directory: `./.gocica.json`
- Home directory: `~/.gocica.json`

## Configuration Options

```json
{
  "dir": "/path/to/cache",     // Directory to store cache data (default: OS-specific cache directory)
  "logLevel": "info",          // Log level: debug, info, error, none (default: info)
  "memMode": false            // Memory mode: if true, data will be stored only in memory (default: false)
}
```

## Environment Variables

The following environment variables can be used to configure gocica:

- `GOCICA_DIR`: Directory to store cache data
- `GOCICA_LOG_LEVEL`: Log level (debug, info, error, none)
- `GOCICA_MEM_MODE`: Memory mode (true/false)

## Priority

1. Command line arguments
2. Environment variables
3. Configuration file
4. Default values