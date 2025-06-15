package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mazrean/gocica/internal/config"
	"github.com/mazrean/gocica/log"
)

func TestNewDisk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		wantErr       bool
		wantObjectMap map[string]<-chan struct{}
		setup         func(t *testing.T) string
	}{
		{
			name: "normal mode initialization",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantObjectMap: map[string]<-chan struct{}{},
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
			disk, err := NewDisk(log.DefaultLogger, &config.Config{Dir: dir})

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

			disk, err := NewDisk(log.DefaultLogger, &config.Config{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()

			if tt.isExist {
				func() {
					_, opener, err := disk.Put(ctx, outputID)
					if err != nil {
						t.Fatal(err)
					}

					f, err := opener.Open()
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()

					if _, err := f.Write(tt.setupData); err != nil {
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
			disk, err := NewDisk(log.DefaultLogger, &config.Config{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}

			var gotPath string
			func() {
				var opener OpenerWithUnlock
				gotPath, opener, err = disk.Put(context.Background(), outputID)
				if err != nil {
					t.Fatal(err)
				}

				f, err := opener.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()

				if _, err := f.Write(tt.data); err != nil {
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
