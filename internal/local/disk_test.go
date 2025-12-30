package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mazrean/gocica/log"
)

func TestNewDisk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		wantErr       bool
		wantObjectMap map[string]*objectLocker
		setup         func(t *testing.T) string
	}{
		{
			name: "normal mode initialization",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantObjectMap: map[string]*objectLocker{},
		},
		{
			name:    "error on directory creation",
			wantErr: true,
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
			disk, err := NewDisk(log.DefaultLogger, dir)

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

			if diff := cmp.Diff(tt.wantObjectMap, disk.objectMap); diff != "" {
				t.Errorf("object map mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDisk_Get(t *testing.T) {
	t.Parallel()

	const (
		outputID = "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2/QO3Br5W5e3U0="
		path     = "o-mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2-QO3Br5W5e3U0="
	)
	testData := []byte("test data")

	tests := []struct {
		name      string
		isExist   bool
		isBefore  bool
		setupData []byte
		want      struct {
			path string
			err  error
		}
	}{
		{
			name:      "put file",
			isExist:   true,
			isBefore:  false,
			setupData: testData,
			want: struct {
				path string
				err  error
			}{
				path: path,
			},
		},
		{
			name:      "existing file",
			isExist:   true,
			isBefore:  true,
			setupData: testData,
			want: struct {
				path string
				err  error
			}{
				path: path,
			},
		},
		{
			name:    "normal mode - non-existent file",
			isExist: false,
			want: struct {
				path string
				err  error
			}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.isBefore {
				if _, err := os.Create(filepath.Join(dir, path)); err != nil {
					t.Fatal(err)
				}
			}

			disk, err := NewDisk(log.DefaultLogger, dir)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()

			if tt.isExist {
				func() {
					_, w, err := disk.Put(ctx, outputID, int64(len(tt.setupData)))
					if err != nil {
						t.Fatal(err)
					}
					defer w.Close()

					if _, err := w.Write(tt.setupData); err != nil {
						t.Fatal(err)
					}
				}()
			}

			gotPath, err := disk.Get(ctx, outputID)

			if diff := cmp.Diff(tt.want.err, err); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
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
		outputID = "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0="
		path     = "o-mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0="
	)
	var (
		emptyData    = []byte{}
		nonEmptyData = []byte("test data")
	)

	tests := []struct {
		name string
		data []byte
		want struct {
			path string
			err  error
		}
	}{
		{
			name: "normal mode - non-empty data",
			data: nonEmptyData,
			want: struct {
				path string
				err  error
			}{
				path: path,
			},
		},
		{
			name: "normal mode - empty data",
			data: emptyData,
			want: struct {
				path string
				err  error
			}{
				path: path,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			disk, err := NewDisk(log.DefaultLogger, dir)
			if err != nil {
				t.Fatal(err)
			}

			var gotPath string
			func() {
				var w io.WriteCloser
				gotPath, w, err = disk.Put(context.Background(), outputID, int64(len(tt.data)))
				if err != nil {
					t.Fatal(err)
				}
				defer w.Close()

				if _, err := w.Write(tt.data); err != nil {
					t.Fatal(err)
				}
			}()

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

func TestEncodeID(t *testing.T) {
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeID(tt.id)
			if diff := cmp.Diff(tt.want.result, got); diff != "" {
				t.Errorf("encodeID result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
