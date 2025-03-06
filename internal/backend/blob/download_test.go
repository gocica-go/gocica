package blob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/DataDog/zstd"
	"github.com/google/go-cmp/cmp"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

type mockDownloadClient struct {
	calls []mockCall
}

func (m *mockDownloadClient) GetURL(context.Context) string {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "GetURL" {
			if url, ok := call.result[0].(string); ok {
				return url
			}
		}
	}
	return ""
}

func (m *mockDownloadClient) DownloadBlock(_ context.Context, offset int64, size int64, w io.Writer) error {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "DownloadBlock" {
			expectedOffset, ok1 := call.args[1].(int64)
			expectedSize, ok2 := call.args[2].(int64)

			if ok1 && ok2 && expectedOffset == offset && expectedSize == size {
				if call.result[1] != nil {
					if err, ok := call.result[1].(error); ok {
						return err
					}
				}
				if data, ok := call.result[0].([]byte); ok {
					_, err := w.Write(data)
					return err
				}
				return nil
			}
		}
	}
	return errors.New("unexpected DownloadBlock call")
}

func (m *mockDownloadClient) DownloadBlockBuffer(_ context.Context, offset int64, size int64, buf []byte) error {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "DownloadBlockBuffer" {
			expectedOffset, ok1 := call.args[1].(int64)
			expectedSize, ok2 := call.args[2].(int64)

			if ok1 && ok2 && expectedOffset == offset && expectedSize == size {
				if call.result[1] != nil {
					if err, ok := call.result[1].(error); ok {
						return err
					}
				}

				if data, ok := call.result[0].([]byte); ok {
					copy(buf, data)
				}
				return nil
			}
		}
	}
	return errors.New("unexpected DownloadBlockBuffer call")
}

func (m *mockDownloadClient) expectGetURL(url string) {
	m.calls = append(m.calls, mockCall{
		method: "GetURL",
		args:   []any{nil}, // Add context placeholder as nil
		result: []any{url},
	})
}

func (m *mockDownloadClient) expectDownloadBlockBuffer(offset, size int64, data []byte, err error) {
	m.calls = append(m.calls, mockCall{
		method: "DownloadBlockBuffer",
		args:   []any{nil, offset, size, nil}, // Add context placeholder as nil
		result: []any{data, err},
	})
}

func TestNewDownloader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupMock   func(*mockDownloadClient, *v1.ActionsCache) []byte
		expectError bool
	}{
		{
			name: "success",
			setupMock: func(client *mockDownloadClient, header *v1.ActionsCache) []byte {
				headerBytes, err := proto.Marshal(header)
				if err != nil {
					t.Fatal(err)
				}

				sizeBuf := make([]byte, 8)
				binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))

				client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
				client.expectDownloadBlockBuffer(8, int64(len(headerBytes)), headerBytes, nil)

				return append(sizeBuf, headerBytes...)
			},
		},
		{
			name: "size download error",
			setupMock: func(client *mockDownloadClient, _ *v1.ActionsCache) []byte {
				client.expectDownloadBlockBuffer(0, 8, nil, errors.New("download error"))
				return nil
			},
			expectError: true,
		},
		{
			name: "header download error",
			setupMock: func(client *mockDownloadClient, header *v1.ActionsCache) []byte {
				headerBytes, err := proto.Marshal(header)
				if err != nil {
					t.Fatal(err)
				}

				sizeBuf := make([]byte, 8)
				binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))

				client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
				client.expectDownloadBlockBuffer(8, int64(len(headerBytes)), nil, errors.New("download error"))

				return nil
			},
			expectError: true,
		},
		{
			name: "zero size header",
			setupMock: func(client *mockDownloadClient, _ *v1.ActionsCache) []byte {
				sizeBuf := make([]byte, 8)
				client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
				return sizeBuf
			},
			expectError: true,
		},
		{
			name: "invalid protobuf",
			setupMock: func(client *mockDownloadClient, _ *v1.ActionsCache) []byte {
				invalidProto := []byte("invalid protobuf")
				sizeBuf := make([]byte, 8)
				binary.BigEndian.PutUint64(sizeBuf, uint64(len(invalidProto)))

				client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
				client.expectDownloadBlockBuffer(8, int64(len(invalidProto)), invalidProto, nil)

				return append(sizeBuf, invalidProto...)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockDownloadClient{}
			header := &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{
					"test": {
						OutputId: "test",
						Size:     100,
					},
				},
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset: 0,
						Size:   100,
					},
				},
			}

			_ = tt.setupMock(client, header)

			downloader, err := NewDownloader(t.Context(), client)
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if downloader == nil {
				t.Fatal("downloader is nil")
			}
		})
	}
}

func TestDownloader_GetEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		header        *v1.ActionsCache
		expectEntries map[string]*v1.IndexEntry
	}{
		{
			name: "success with single entry",
			header: &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{
					"test": {
						OutputId: "test",
						Size:     100,
					},
				},
			},
			expectEntries: map[string]*v1.IndexEntry{
				"test": {
					OutputId: "test",
					Size:     100,
				},
			},
		},
		{
			name: "success with multiple entries",
			header: &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{
					"test1": {
						OutputId: "test1",
						Size:     100,
					},
					"test2": {
						OutputId: "test2",
						Size:     200,
					},
				},
			},
			expectEntries: map[string]*v1.IndexEntry{
				"test1": {
					OutputId: "test1",
					Size:     100,
				},
				"test2": {
					OutputId: "test2",
					Size:     200,
				},
			},
		},
		{
			name: "success with empty entries",
			header: &v1.ActionsCache{
				Entries: nil,
			},
			expectEntries: nil,
		},
		{
			name:          "success with nil entries",
			header:        &v1.ActionsCache{},
			expectEntries: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockDownloadClient{}
			headerBytes, err := proto.Marshal(tt.header)
			if err != nil {
				t.Fatal(err)
			}

			sizeBuf := make([]byte, 8)
			binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))

			client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
			client.expectDownloadBlockBuffer(8, int64(len(headerBytes)), headerBytes, nil)

			downloader, err := NewDownloader(t.Context(), client)
			if err != nil {
				t.Fatal(err)
			}

			entries, err := downloader.GetEntries(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tt.expectEntries, entries, protocmp.Transform()); diff != "" {
				t.Errorf("entries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDownloader_DownloadOutputBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		header        *v1.ActionsCache
		blobID        string
		setupMock     func(*mockDownloadClient, int64) error
		expectContent []byte
		expectError   bool
	}{
		{
			name: "success with zstd compression",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset:      100,
						Size:        50,
						Compression: v1.Compression_COMPRESSION_ZSTD,
					},
				},
				OutputTotalSize: 150,
			},
			blobID: "test",
			setupMock: func(client *mockDownloadClient, headerSize int64) error {
				data := bytes.Repeat([]byte{0xAA}, 50)
				compressedData, err := zstd.Compress(nil, data)
				if err != nil {
					return fmt.Errorf("compress data: %w", err)
				}
				client.calls = append(client.calls, mockCall{
					method: "DownloadBlock",
					args:   []any{nil, headerSize + 100, int64(50)}, // サイズは圧縮前のサイズを使用
					result: []any{compressedData, nil},
				})
				return nil
			},
			expectContent: bytes.Repeat([]byte{0xAA}, 50),
		},
		{
			name: "success without compression",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset:      100,
						Size:        50,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				},
				OutputTotalSize: 150,
			},
			blobID: "test",
			setupMock: func(client *mockDownloadClient, headerSize int64) error {
				data := bytes.Repeat([]byte{0xAA}, 50)
				client.calls = append(client.calls, mockCall{
					method: "DownloadBlock",
					args:   []any{nil, headerSize + 100, int64(50)},
					result: []any{data, nil},
				})
				return nil
			},
			expectContent: bytes.Repeat([]byte{0xAA}, 50),
		},
		{
			name: "unsupported compression",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset:      100,
						Size:        50,
						Compression: v1.Compression(100),
					},
				},
				OutputTotalSize: 150,
			},
			blobID:      "test",
			expectError: true,
		},
		{
			name: "output not found",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{},
			},
			blobID:      "test",
			expectError: true,
		},
		{
			name: "download error with zstd compression",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset:      100,
						Size:        50,
						Compression: v1.Compression_COMPRESSION_ZSTD,
					},
				},
			},
			blobID: "test",
			setupMock: func(client *mockDownloadClient, headerSize int64) error {
				client.calls = append(client.calls, mockCall{
					method: "DownloadBlock",
					args:   []any{nil, headerSize + 100, int64(50)},
					result: []any{nil, errors.New("download error")},
				})
				return nil
			},
			expectError: true,
		},
		{
			name: "zero size output",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset:      100,
						Size:        0,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				},
			},
			blobID:      "test",
			expectError: true,
		},
		{
			name: "negative size output",
			header: &v1.ActionsCache{
				Outputs: map[string]*v1.ActionsOutput{
					"test": {
						Offset:      100,
						Size:        -1,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				},
			},
			blobID:      "test",
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockDownloadClient{}
			headerBytes, err := proto.Marshal(tt.header)
			if err != nil {
				t.Fatal(err)
			}

			sizeBuf := make([]byte, 8)
			binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))
			headerSize := int64(8 + len(headerBytes))

			client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
			client.expectDownloadBlockBuffer(8, int64(len(headerBytes)), headerBytes, nil)

			if tt.setupMock != nil {
				err := tt.setupMock(client, headerSize)
				if err != nil {
					t.Fatal(err)
				}
			}

			downloader, err := NewDownloader(t.Context(), client)
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			err = downloader.DownloadOutputBlock(t.Context(), tt.blobID, &buf)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tt.expectContent, buf.Bytes()); diff != "" {
				t.Errorf("content mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDownloader_GetOutputBlockURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		header      *v1.ActionsCache
		setupMock   func(*mockDownloadClient)
		expectURL   string
		expectSize  int64
		expectError bool
	}{
		{
			name: "success",
			header: &v1.ActionsCache{
				OutputTotalSize: 150,
			},
			setupMock: func(client *mockDownloadClient) {
				client.expectGetURL("test-url")
			},
			expectURL:  "test-url",
			expectSize: 150,
		},
		{
			name: "empty URL",
			header: &v1.ActionsCache{
				OutputTotalSize: 150,
			},
			setupMock: func(client *mockDownloadClient) {
				client.expectGetURL("")
			},
			expectURL:  "",
			expectSize: 150,
		},
		{
			name: "zero size",
			header: &v1.ActionsCache{
				OutputTotalSize: 0,
			},
			setupMock: func(client *mockDownloadClient) {
				client.expectGetURL("test-url")
			},
			expectURL:  "test-url",
			expectSize: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockDownloadClient{}
			headerBytes, err := proto.Marshal(tt.header)
			if err != nil {
				t.Fatal(err)
			}

			sizeBuf := make([]byte, 8)
			binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))

			client.expectDownloadBlockBuffer(0, 8, sizeBuf, nil)
			client.expectDownloadBlockBuffer(8, int64(len(headerBytes)), headerBytes, nil)

			if tt.setupMock != nil {
				tt.setupMock(client)
			}

			downloader, err := NewDownloader(t.Context(), client)
			if err != nil {
				t.Fatal(err)
			}

			url, offset, size, err := downloader.GetOutputBlockURL(t.Context())
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tt.expectURL, url); diff != "" {
				t.Errorf("url mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(int64(8+len(headerBytes)), offset); diff != "" {
				t.Errorf("offset mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.expectSize, size); diff != "" {
				t.Errorf("size mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
