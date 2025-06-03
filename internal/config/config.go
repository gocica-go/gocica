package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
)

type Config struct {
	Version  kong.VersionFlag `kong:"short='v',help='Show version and exit.'"`
	Dir      string           `kong:"short='d',optional,help='Directory to store cache files',env='GOCICA_DIR'"`
	LogLevel string           `kong:"short='l',default='info',enum='debug,info,warn,error,silent',help='Log level',env='GOCICA_LOG_LEVEL'"`
	Github   struct {
		CacheURL string `kong:"help='GitHub Actions Cache URL',env='GOCICA_GITHUB_CACHE_URL,ACTIONS_RESULTS_URL'"`
		Token    string `kong:"help='GitHub token',env='GOCICA_GITHUB_TOKEN,ACTIONS_RUNTIME_TOKEN'"`
		RunnerOS string `kong:"help='GitHub runner OS',env='GOCICA_GITHUB_RUNNER_OS,RUNNER_OS'"`
		Ref      string `kong:"help='GitHub base ref of the workflow or the target branch of the pull request',env='GOCICA_GITHUB_REF,GITHUB_REF'"`
		Sha      string `kong:"help='GitHub SHA of the commit',env='GOCICA_GITHUB_SHA,GITHUB_SHA'"`
	} `kong:"optional,group='github',embed,prefix='github.'"`
	Dev DevFlag `kong:"group='dev',embed,prefix='dev.'"`
}

type Version struct {
	Version  string
	Revision string
}

func Load(version Version) (*Config, error) {
	config := &Config{}
	// Parse command line arguments
	parser := kong.Must(config,
		kong.Name("gocica"),
		kong.Description("A fast GOCACHEPROG implementation for CI"),
		kong.Vars{"version": fmt.Sprintf("%s (%s)", version.Version, version.Revision)},
		kong.UsageOnError(),
	)
	_, err := parser.Parse(os.Args[1:])
	if err != nil {
		return nil, fmt.Errorf("failed to parse arguments: %w", err)
	}

	// If directory is not specified, use cache directory
	if config.Dir == "" {
		cacheDir, err := os.UserCacheDir()
		if err == nil {
			config.Dir = filepath.Join(cacheDir, "gocica")
		}
	}

	// Validate directory
	if config.Dir == "" {
		return nil, fmt.Errorf("cache directory is not specified. please specify using the -dir flag or config file")
	}

	return config, nil
}
