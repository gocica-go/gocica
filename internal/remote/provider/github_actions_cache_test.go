package provider

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mazrean/gocica/log"
)

func TestGHACacheClient_blobKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		config          *GHACacheConfig
		wantKey         string
		wantRestoreKeys []string
		wantVersion     string
	}{
		{
			// Golden values for the build cache namespace. These keys are shared with
			// every gocica release already in the wild: changing them silently
			// invalidates every existing cache entry.
			name: "build cache namespace is unchanged",
			config: &GHACacheConfig{
				CacheURL: "https://example.com/",
				RunnerOS: "Linux",
				Ref:      "refs/heads/main",
				Sha:      "0123456789abcdef",
			},
			wantKey: "gocica-cache-Linux-refs/heads/main-0123456789abcdef",
			wantRestoreKeys: []string{
				"gocica-cache-Linux-refs/heads/main-",
				"gocica-cache-Linux-",
			},
			wantVersion: actionsCacheVersion,
		},
		{
			name: "module namespace is isolated",
			config: &GHACacheConfig{
				CacheURL:   "https://example.com/",
				RunnerOS:   "Linux",
				Ref:        "refs/heads/main",
				Sha:        "0123456789abcdef",
				Prefix:     ModuleCachePrefix,
				KeyVersion: ModuleCacheVersion,
			},
			wantKey: "gocica-mod-Linux-refs/heads/main-0123456789abcdef",
			wantRestoreKeys: []string{
				"gocica-mod-Linux-refs/heads/main-",
				"gocica-mod-Linux-",
			},
			wantVersion: ModuleCacheVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := newGitHubCacheClient(t.Context(), log.DefaultLogger, tt.config)
			if err != nil {
				t.Fatalf("new github cache client: %v", err)
			}

			key, restoreKeys := client.blobKey()
			if diff := cmp.Diff(tt.wantKey, key); diff != "" {
				t.Errorf("key mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantRestoreKeys, restoreKeys); diff != "" {
				t.Errorf("restore keys mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantVersion, client.version); diff != "" {
				t.Errorf("version mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestModuleCacheVersionIsDistinct(t *testing.T) {
	t.Parallel()

	if ModuleCacheVersion == actionsCacheVersion {
		t.Error("module cache version must differ from the build cache version")
	}
}
