package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Ignore Timenano field in comparisons
var ignoreTimenanoCmp = cmpopts.IgnoreFields(MetaData{}, "Timenano")

func TestNewDisk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		isMemory bool
		wantErr  bool
		setup    func(t *testing.T) string
	}{
		{
			name:     "normal mode initialization",
			isMemory: false,
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name:     "memory mode initialization",
			isMemory: true,
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name:     "error on directory creation",
			isMemory: false,
			wantErr:  true,
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Chmod(dir, 0500); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(dir, "subdir")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			disk, err := NewDisk(dir, tt.isMemory)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if disk == nil {
				t.Fatal("disk is nil")
			}

			if tt.isMemory && disk.actionMap == nil {
				t.Error("actionMap should not be nil in memory mode")
			} else if !tt.isMemory && disk.actionMap != nil {
				t.Error("actionMap should be nil in normal mode")
			}

			if !tt.isMemory {
				if _, err := os.Stat(filepath.Join(dir, "r-empty-file")); err != nil {
					t.Error("empty file should exist:", err)
				}
			}
		})
	}
}

func TestDisk_Get(t *testing.T) {
	t.Parallel()

	const (
		actionID      = "eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc="
		outputID      = "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0="
		path          = "o-mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0="
		emptyFilePath = "r-empty-file"
	)
	var (
		emptyData    = []byte{}
		nonEmptyData = []byte("test data")
	)

	tests := []struct {
		name      string
		isMemory  bool
		isExist   bool
		setupData []byte
		want      struct {
			path string
			meta *MetaData
			err  error
		}
	}{
		{
			name:      "normal mode - existing file",
			isMemory:  false,
			isExist:   true,
			setupData: nonEmptyData,
			want: struct {
				path string
				meta *MetaData
				err  error
			}{
				path: path,
				meta: &MetaData{
					OutputID: outputID,
					Size:     int64(len(nonEmptyData)),
				},
			},
		},
		{
			name:      "normal mode - empty file",
			isMemory:  false,
			isExist:   true,
			setupData: emptyData,
			want: struct {
				path string
				meta *MetaData
				err  error
			}{
				path: emptyFilePath,
				meta: &MetaData{
					OutputID: outputID,
					Size:     0,
				},
			},
		},
		{
			name:     "normal mode - non-existent file",
			isMemory: false,
			isExist:  false,
			want: struct {
				path string
				meta *MetaData
				err  error
			}{},
		},
		{
			name:      "memory mode - existing file",
			isMemory:  true,
			isExist:   true,
			setupData: nonEmptyData,
			want: struct {
				path string
				meta *MetaData
				err  error
			}{
				path: path,
				meta: &MetaData{
					OutputID: outputID,
					Size:     int64(len(nonEmptyData)),
				},
			},
		},
		{
			name:     "memory mode - non-existent file",
			isMemory: true,
			isExist:  false,
			want: struct {
				path string
				meta *MetaData
				err  error
			}{},
		},
		{
			name:      "memory mode - empty file",
			isMemory:  true,
			isExist:   true,
			setupData: emptyData,
			want: struct {
				path string
				meta *MetaData
				err  error
			}{
				path: emptyFilePath,
				meta: &MetaData{
					OutputID: outputID,
					Size:     0,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			disk, err := NewDisk(dir, tt.isMemory)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()

			if tt.isExist {
				_, err = disk.Put(ctx, actionID, outputID, int64(len(tt.setupData)), bytes.NewReader(tt.setupData))
				if err != nil {
					t.Fatal(err)
				}
			}

			gotPath, gotMeta, err := disk.Get(ctx, actionID)

			if diff := cmp.Diff(tt.want.err, err); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}

			if tt.want.meta == nil {
				if gotMeta != nil {
					t.Error("expected nil metadata but got non-nil")
				}
			} else {
				if diff := cmp.Diff(tt.want.meta, gotMeta, ignoreTimenanoCmp); diff != "" {
					t.Errorf("metadata mismatch (-want +got):\n%s", diff)
				}
			}

			if tt.want.path == "" {
				if diff := cmp.Diff("", gotPath); diff != "" {
					t.Errorf("path mismatch (-want +got):\n%s", diff)
				}
			} else {
				if diff := cmp.Diff(filepath.Join(dir, tt.want.path), gotPath); diff != "" {
					t.Errorf("path mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestDisk_Put(t *testing.T) {
	t.Parallel()

	const (
		actionID      = "eqWF6jnj8u+hl4RcMhv+53OR/32mkxg1mypRdiSXUzc="
		outputID      = "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0="
		path          = "o-mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0="
		emptyFilePath = "r-empty-file"
	)
	var (
		emptyData    = []byte{}
		nonEmptyData = []byte("test data")
	)

	tests := []struct {
		name     string
		isMemory bool
		data     []byte
		want     struct {
			path string
			err  error
		}
	}{
		{
			name:     "normal mode - non-empty data",
			isMemory: false,
			data:     nonEmptyData,
			want: struct {
				path string
				err  error
			}{
				path: path,
			},
		},
		{
			name:     "normal mode - empty data",
			isMemory: false,
			data:     emptyData,
			want: struct {
				path string
				err  error
			}{
				path: emptyFilePath,
			},
		},
		{
			name:     "memory mode - non-empty data",
			isMemory: true,
			data:     nonEmptyData,
			want: struct {
				path string
				err  error
			}{
				path: path,
			},
		},
		{
			name:     "memory mode - empty data",
			isMemory: true,
			data:     emptyData,
			want: struct {
				path string
				err  error
			}{
				path: emptyFilePath,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			disk, err := NewDisk(dir, tt.isMemory)
			if err != nil {
				t.Fatal(err)
			}

			gotPath, err := disk.Put(context.Background(), actionID, outputID, int64(len(tt.data)), bytes.NewReader(tt.data))

			if diff := cmp.Diff(tt.want.err, err); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}

			if err != nil {
				return
			}

			if diff := cmp.Diff(filepath.Join(dir, tt.want.path), gotPath); diff != "" {
				t.Errorf("path mismatch (-want +got):\n%s", diff)
			}

			content, err := os.ReadFile(gotPath)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tt.data, content); diff != "" {
				t.Errorf("content mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDisk_encodeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want struct {
			result string
			err    error
		}
	}{
		{
			name: "base64 without slash",
			id:   "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0=",
			want: struct {
				result string
				err    error
			}{
				result: "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0=",
			},
		},
		{
			name: "base64 with one slash",
			id:   "eqWF/jnj8u+hl4RcMhv+53OR",
			want: struct {
				result string
				err    error
			}{
				result: "eqWF-jnj8u+hl4RcMhv+53OR",
			},
		},
		{
			name: "base64 with multiple slashes",
			id:   "eq/WF/jn/j8u+hl4RcMhv+53OR",
			want: struct {
				result string
				err    error
			}{
				result: "eq-WF-jn-j8u+hl4RcMhv+53OR",
			},
		},
		{
			name: "base64 with padding",
			id:   "YWJjZA==",
			want: struct {
				result string
				err    error
			}{
				result: "YWJjZA==",
			},
		},
		{
			name: "empty string",
			id:   "",
			want: struct {
				result string
				err    error
			}{
				result: "",
			},
		},
	}

	disk := &Disk{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := disk.encodeID(tt.id)
			if diff := cmp.Diff(tt.want.result, got); diff != "" {
				t.Errorf("encodeID result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDisk_writeAtomic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		data     []byte
		wantSize int64
		isErr    bool
	}{
		{
			name:     "successful write",
			data:     []byte("test data"),
			wantSize: 9,
		},
		{
			name:     "empty data",
			data:     []byte{},
			wantSize: 0,
		},
		{
			name:  "invalid path",
			path:  strings.Repeat("a", 1000),
			data:  []byte("test"),
			isErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			disk, err := NewDisk(dir, false)
			if err != nil {
				t.Fatal(err)
			}

			destPath := tt.path
			if destPath == "" {
				destPath = filepath.Join(dir, "test-file")
			} else {
				destPath = filepath.Join(dir, tt.path)
			}

			gotSize, err := disk.writeAtomic(destPath, bytes.NewReader(tt.data))

			if tt.isErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				return
			}

			if diff := cmp.Diff(tt.wantSize, gotSize); diff != "" {
				t.Errorf("size mismatch (-want +got):\n%s", diff)
			}

			content, err := os.ReadFile(destPath)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tt.data, content); diff != "" {
				t.Errorf("content mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
