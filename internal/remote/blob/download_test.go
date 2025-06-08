package blob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/DataDog/zstd"
	"github.com/google/go-cmp/cmp"
	"github.com/mazrean/gocica/internal/local"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote/blob/mock"
	"github.com/mazrean/gocica/log"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestDownloader_readHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		header      *v1.ActionsCache
		setupMock   func(*mock.MockDownloadClient, []byte)
		expectError bool
	}{
		{
			name: "success",
			setupMock: func(client *mock.MockDownloadClient, headerBytes []byte) {
				sizeBuf := make([]byte, 8)
				binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))

				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(0), int64(8), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, buf []byte) error {
						copy(buf, sizeBuf)
						return nil
					})
				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(8), int64(len(headerBytes)), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, buf []byte) error {
						copy(buf, headerBytes)
						return nil
					})
			},
			header: &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{
					"test": {
						OutputId: "test",
						Size:     100,
					},
				},
				Outputs: []*v1.ActionsOutput{
					{
						Id:     "test",
						Offset: 0,
						Size:   100,
					},
				},
			},
		},
		{
			name: "size download error",
			setupMock: func(client *mock.MockDownloadClient, _ []byte) {
				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(0), int64(8), gomock.Any()).
					Return(errors.New("download error"))
			},
			expectError: true,
		},
		{
			name: "header download error",
			setupMock: func(client *mock.MockDownloadClient, headerBytes []byte) {
				sizeBuf := make([]byte, 8)
				binary.BigEndian.PutUint64(sizeBuf, uint64(len(headerBytes)))

				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(0), int64(8), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, buf []byte) error {
						copy(buf, sizeBuf)
						return nil
					})
				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(8), int64(len(headerBytes)), gomock.Any()).
					Return(errors.New("download error"))
			},
			header: &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{
					"test": {
						OutputId: "test",
						Size:     100,
					},
				},
			},
			expectError: true,
		},
		{
			name: "zero size header",
			setupMock: func(client *mock.MockDownloadClient, _ []byte) {
				sizeBuf := make([]byte, 8)
				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(0), int64(8), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, buf []byte) error {
						copy(buf, sizeBuf)
						return nil
					})
				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(8), int64(0), gomock.Any()).Return(nil)
			},
			header: &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{},
				Outputs: []*v1.ActionsOutput{},
			},
		},
		{
			name: "invalid protobuf",
			setupMock: func(client *mock.MockDownloadClient, _ []byte) {
				invalidProto := []byte("invalid protobuf")
				sizeBuf := make([]byte, 8)
				binary.BigEndian.PutUint64(sizeBuf, uint64(len(invalidProto)))

				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(0), int64(8), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, buf []byte) error {
						copy(buf, sizeBuf)
						return nil
					})
				client.EXPECT().DownloadBlockBuffer(gomock.Any(), int64(8), int64(len(invalidProto)), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, buf []byte) error {
						copy(buf, invalidProto)
						return nil
					})
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			headerBytes, err := proto.Marshal(tt.header)
			if err != nil {
				t.Fatal(err)
			}

			client := mock.NewMockDownloadClient(gomock.NewController(t))

			tt.setupMock(client, headerBytes)

			downloader := &Downloader{
				logger: log.DefaultLogger,
				client: client,
			}

			header, headerSize, err := downloader.readHeader(t.Context())

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tt.header, header, protocmp.Transform()); diff != "" {
				t.Errorf("header mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(int64(8+len(headerBytes)), headerSize); diff != "" {
				t.Errorf("header size mismatch (-want +got):\n%s", diff)
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

			downloader := &Downloader{
				logger: log.DefaultLogger,
				client: mock.NewMockDownloadClient(gomock.NewController(t)),
				header: tt.header,
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

func TestDownloader_GetEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		header      *v1.ActionsCache
		expectEntry *v1.IndexEntry
		expectOK    bool
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
			expectEntry: &v1.IndexEntry{
				OutputId: "test",
				Size:     100,
			},
			expectOK: true,
		},
		{
			name: "success with multiple entries",
			header: &v1.ActionsCache{
				Entries: map[string]*v1.IndexEntry{
					"test": {
						OutputId: "test1",
						Size:     100,
					},
					"test2": {
						OutputId: "test2",
						Size:     200,
					},
				},
			},
			expectEntry: &v1.IndexEntry{
				OutputId: "test1",
				Size:     100,
			},
			expectOK: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			downloader := &Downloader{
				logger: log.DefaultLogger,
				client: mock.NewMockDownloadClient(gomock.NewController(t)),
				header: tt.header,
			}

			entry, ok, err := downloader.GetEntry(t.Context(), "test")
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tt.expectEntry, entry, protocmp.Transform()); diff != "" {
				t.Errorf("entry mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.expectOK, ok); diff != "" {
				t.Errorf("ok mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDownloader_GetOutputBlockURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		header      *v1.ActionsCache
		setupMock   func(*mock.MockDownloadClient)
		expectURL   string
		expectSize  int64
		expectError bool
	}{
		{
			name: "success",
			header: &v1.ActionsCache{
				OutputTotalSize: 150,
			},
			setupMock: func(client *mock.MockDownloadClient) {
				client.EXPECT().GetURL(gomock.Any()).Return("test-url")
			},
			expectURL:  "test-url",
			expectSize: 150,
		},
		{
			name: "empty URL",
			header: &v1.ActionsCache{
				OutputTotalSize: 150,
			},
			setupMock: func(client *mock.MockDownloadClient) {
				client.EXPECT().GetURL(gomock.Any()).Return("")
			},
			expectURL:  "",
			expectSize: 150,
		},
		{
			name: "zero size",
			header: &v1.ActionsCache{
				OutputTotalSize: 0,
			},
			setupMock: func(client *mock.MockDownloadClient) {
				client.EXPECT().GetURL(gomock.Any()).Return("test-url")
			},
			expectURL:  "test-url",
			expectSize: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := mock.NewMockDownloadClient(gomock.NewController(t))

			if tt.setupMock != nil {
				tt.setupMock(client)
			}

			downloader := &Downloader{
				logger: log.DefaultLogger,
				client: client,
				header: tt.header,
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
			if diff := cmp.Diff(int64(0), offset); diff != "" {
				t.Errorf("offset mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.expectSize, size); diff != "" {
				t.Errorf("size mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type mockWriteCloser struct {
	*bytes.Buffer
	closed bool
}

func (m *mockWriteCloser) Close() error {
	m.closed = true
	return nil
}

func TestDownloader_DownloadAllOutputBlocks(t *testing.T) {
	t.Parallel()

	data := []byte("testdata12")
	compressedData, err := zstd.Compress(nil, data)
	if err != nil {
		t.Fatalf("compress data: %v", err)
	}

	tests := []struct {
		name        string
		header      *v1.ActionsCache
		setupMock   func(*mock.MockDownloadClient, *local.MockBackend, *map[string]*mockWriteCloser, int64)
		writerError bool
		expectData  map[string][]byte
		expectError bool
	}{
		{
			name: "success with single output",
			header: &v1.ActionsCache{
				Outputs: []*v1.ActionsOutput{
					{
						Id:          "test",
						Offset:      0,
						Size:        10,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				},
				OutputTotalSize: 10,
			},
			setupMock: func(client *mock.MockDownloadClient, localBackend *local.MockBackend, writers *map[string]*mockWriteCloser, headerSize int64) {
				data := []byte("testdata12")
				client.EXPECT().DownloadBlock(gomock.Any(), headerSize, int64(10), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ int64, _ int64, w io.Writer) error {
						_, _ = w.Write(data)
						return nil
					})
				localBackend.EXPECT().Put(gomock.Any(), "test", int64(10)).DoAndReturn(func(context.Context, string, int64) (string, io.WriteCloser, error) {
					(*writers)["test"] = &mockWriteCloser{
						Buffer: bytes.NewBuffer(nil),
					}
					return "test", (*writers)["test"], nil
				})
			},
			expectData: map[string][]byte{
				"test": []byte("testdata12"),
			},
		},
		{
			name: "success with multiple outputs",
			header: &v1.ActionsCache{
				Outputs: []*v1.ActionsOutput{
					{
						Id:          "test1",
						Offset:      0,
						Size:        10,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
					{
						Id:          "test2",
						Offset:      10,
						Size:        10,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				},
				OutputTotalSize: 20,
			},
			setupMock: func(client *mock.MockDownloadClient, localBackend *local.MockBackend, writers *map[string]*mockWriteCloser, headerSize int64) {
				data := []byte("testdata12testdata34")
				client.EXPECT().DownloadBlock(gomock.Any(), headerSize, int64(20), gomock.Any()).
					Do(func(_ context.Context, _ int64, _ int64, w io.Writer) error {
						_, _ = w.Write(data)
						return nil
					})
				localBackend.EXPECT().Put(gomock.Any(), "test1", int64(10)).DoAndReturn(func(context.Context, string, int64) (string, io.WriteCloser, error) {
					(*writers)["test1"] = &mockWriteCloser{
						Buffer: bytes.NewBuffer(nil),
					}
					return "test1", (*writers)["test1"], nil
				})
				localBackend.EXPECT().Put(gomock.Any(), "test2", int64(10)).DoAndReturn(func(context.Context, string, int64) (string, io.WriteCloser, error) {
					(*writers)["test2"] = &mockWriteCloser{
						Buffer: bytes.NewBuffer(nil),
					}
					return "test2", (*writers)["test2"], nil
				})
			},
			expectData: map[string][]byte{
				"test1": []byte("testdata12"),
				"test2": []byte("testdata34"),
			},
		},
		{
			name: "success with zstd compression",
			header: &v1.ActionsCache{
				Outputs: []*v1.ActionsOutput{
					{
						Id:          "test",
						Offset:      0,
						Size:        int64(len(compressedData)),
						Compression: v1.Compression_COMPRESSION_ZSTD,
					},
				},
				OutputTotalSize: 10,
			},
			setupMock: func(client *mock.MockDownloadClient, localBackend *local.MockBackend, writers *map[string]*mockWriteCloser, headerSize int64) {
				client.EXPECT().DownloadBlock(gomock.Any(), headerSize, int64(len(compressedData)), gomock.Any()).
					Do(func(_ context.Context, _ int64, _ int64, w io.Writer) error {
						_, _ = w.Write(compressedData)
						return nil
					})
				localBackend.EXPECT().Put(gomock.Any(), "test", int64(len(compressedData))).DoAndReturn(func(context.Context, string, int64) (string, io.WriteCloser, error) {
					(*writers)["test"] = &mockWriteCloser{
						Buffer: bytes.NewBuffer(nil),
					}
					return "test", (*writers)["test"], nil
				})
			},
			expectData: map[string][]byte{
				"test": []byte("testdata12"),
			},
		},
		{
			name: "unsupported compression",
			header: &v1.ActionsCache{
				Outputs: []*v1.ActionsOutput{
					{
						Id:          "test",
						Offset:      0,
						Size:        10,
						Compression: v1.Compression(100),
					},
				},
				OutputTotalSize: 10,
			},
			expectData: map[string][]byte{
				"test": []byte("testdata12"),
			},
			setupMock: func(client *mock.MockDownloadClient, localBackend *local.MockBackend, writers *map[string]*mockWriteCloser, headerSize int64) {
				data := []byte("testdata12")
				client.EXPECT().DownloadBlock(gomock.Any(), headerSize, int64(10), gomock.Any()).
					Do(func(_ context.Context, _ int64, _ int64, w io.Writer) error {
						_, _ = w.Write(data)
						return nil
					})
				localBackend.EXPECT().Put(gomock.Any(), "test", int64(10)).DoAndReturn(func(context.Context, string, int64) (string, io.WriteCloser, error) {
					(*writers)["test"] = &mockWriteCloser{
						Buffer: bytes.NewBuffer(nil),
					}
					return "test", (*writers)["test"], nil
				})
			},
		},
		{
			name: "download error",
			header: &v1.ActionsCache{
				Outputs: []*v1.ActionsOutput{
					{
						Id:          "test",
						Offset:      0,
						Size:        10,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				},
				OutputTotalSize: 10,
			},
			setupMock: func(client *mock.MockDownloadClient, localBackend *local.MockBackend, writers *map[string]*mockWriteCloser, headerSize int64) {
				client.EXPECT().DownloadBlock(gomock.Any(), headerSize, int64(10), gomock.Any()).
					DoAndReturn(func(context.Context, int64, int64, io.Writer) error {
						return errors.New("download error")
					})
				localBackend.EXPECT().Put(gomock.Any(), "test", int64(10)).DoAndReturn(func(context.Context, string, int64) (string, io.WriteCloser, error) {
					(*writers)["test"] = &mockWriteCloser{
						Buffer: bytes.NewBuffer(nil),
					}
					return "test", (*writers)["test"], nil
				})
			},
			expectError: true,
		},
		{
			name: "empty outputs",
			header: &v1.ActionsCache{
				Outputs:         []*v1.ActionsOutput{},
				OutputTotalSize: 0,
			},
			expectData: map[string][]byte{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := mock.NewMockDownloadClient(gomock.NewController(t))
			localBackend := local.NewMockBackend(gomock.NewController(t))
			writers := make(map[string]*mockWriteCloser)

			if tt.setupMock != nil {
				tt.setupMock(client, localBackend, &writers, 0)
			}

			downloader := &Downloader{
				logger: log.DefaultLogger,
				client: client,
				header: tt.header,
			}

			err := downloader.DownloadAllOutputBlocks(t.Context(), localBackend)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check that all writers were closed
			for id, w := range writers {
				if !w.closed {
					t.Errorf("writer for %s not closed", id)
				}
			}

			// Check that expected data was received
			for id, expected := range tt.expectData {
				w, ok := writers[id]
				if !ok {
					t.Errorf("missing writer for %s", id)
					continue
				}
				if diff := cmp.Diff(expected, w.Bytes()); diff != "" {
					t.Errorf("content mismatch for %s (-want +got):\n%s", id, diff)
				}
			}

			// Check that there are no unexpected writers
			for id := range writers {
				if _, ok := tt.expectData[id]; !ok {
					t.Errorf("unexpected writer for %s", id)
				}
			}
		})
	}
}
