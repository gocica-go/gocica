package blob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/DataDog/zstd"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type mockCall struct {
	method string
	args   []any
	result []any
}

type mockUploadClient struct {
	calls []mockCall
}

func (m *mockUploadClient) UploadBlock(_ context.Context, blobID string, _ io.ReadSeekCloser) (int64, error) {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "UploadBlock" {
			if call.args[0] == nil {
				size, ok := call.result[0].(int64)
				if !ok {
					return 0, errors.New("invalid size type")
				}
				var err error
				if e, ok := call.result[1].(error); ok {
					err = e
				}
				return size, err
			}
			if str, ok := call.args[0].(string); ok && str == blobID {
				size, ok := call.result[0].(int64)
				if !ok {
					return 0, errors.New("invalid size type")
				}
				var err error
				if e, ok := call.result[1].(error); ok {
					err = e
				}
				return size, err
			}
		}
	}
	return 0, errors.New("unexpected UploadBlock call")
}

func (m *mockUploadClient) UploadBlockFromURL(_ context.Context, _, url string, offset, size int64) error {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "UploadBlockFromURL" {
			if len(call.args) < 4 {
				continue
			}
			if _, ok := call.args[0].(string); !ok {
				continue
			}
			if u, ok := call.args[1].(string); !ok || u != url {
				continue
			}
			if off, ok := call.args[2].(int64); !ok || off != offset {
				continue
			}
			if sz, ok := call.args[3].(int64); !ok || sz != size {
				continue
			}
			if call.result[0] == nil {
				return nil
			}
			if err, ok := call.result[0].(error); ok {
				return err
			}
		}
	}
	return errors.New("unexpected UploadBlockFromURL call for URL: " + url)
}

func (m *mockUploadClient) Commit(_ context.Context, _ []string) error {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "Commit" {
			if call.result[0] == nil {
				return nil
			}
			if err, ok := call.result[0].(error); ok {
				return err
			}
		}
	}
	return errors.New("unexpected Commit call")
}

func (m *mockUploadClient) expectUploadBlock(blobID string, size int64, err error) {
	m.calls = append(m.calls, mockCall{
		method: "UploadBlock",
		args:   []any{blobID},
		result: []any{size, err},
	})
}

func (m *mockUploadClient) expectAnyUploadBlock(size int64, err error) {
	m.calls = append(m.calls, mockCall{
		method: "UploadBlock",
		args:   []any{nil},
		result: []any{size, err},
	})
}

func (m *mockUploadClient) expectUploadBlockFromURL(offset, size int64, err error) {
	m.calls = append(m.calls, mockCall{
		method: "UploadBlockFromURL",
		args:   []any{"", "test-url", offset, size},
		result: []any{err},
	})
}

func (m *mockUploadClient) expectCommit(err error) {
	m.calls = append(m.calls, mockCall{
		method: "Commit",
		args:   []any{nil},
		result: []any{err},
	})
}

type mockBaseBlobProvider struct {
	calls []mockCall
}

func (m *mockBaseBlobProvider) GetOutputs(_ context.Context) ([]*v1.ActionsOutput, error) {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "DownloadOutputs" {
			outputs := []*v1.ActionsOutput{}
			if call.result[0] != nil {
				if out, ok := call.result[0].([]*v1.ActionsOutput); ok {
					outputs = out
				}
			}
			if call.result[1] == nil {
				return outputs, nil
			}
			if err, ok := call.result[1].(error); ok {
				return outputs, err
			}
		}
	}
	return nil, errors.New("unexpected DownloadOutputs call")
}

func (m *mockBaseBlobProvider) GetOutputBlockURL(_ context.Context) (string, int64, int64, error) {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "GetOutputBlockURL" {
			if call.result[3] == nil {
				url, ok1 := call.result[0].(string)
				offset, ok2 := call.result[1].(int64)
				size, ok3 := call.result[2].(int64)
				if ok1 && ok2 && ok3 {
					return url, offset, size, nil
				}
			}
			if err, ok := call.result[3].(error); ok {
				return "", 0, 0, err
			}
		}
	}
	return "", 0, 0, errors.New("unexpected GetOutputBlockURL call")
}

func (m *mockBaseBlobProvider) expectGetOutputBlockURL(url string, offset, size int64, err error) {
	m.calls = append(m.calls, mockCall{
		method: "GetOutputBlockURL",
		result: []any{url, offset, size, err},
	})
}

func (m *mockBaseBlobProvider) expectDownloadOutputs(outputs []*v1.ActionsOutput, err error) {
	m.calls = append(m.calls, mockCall{
		method: "DownloadOutputs",
		result: []any{outputs, err},
	})
}

func TestNewUploader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mockSetup        func(*mockUploadClient, *mockBaseBlobProvider)
		expectError      bool
		checkBaseFunc    bool
		wantBlockIDEmpty bool
		wantSize         int64
	}{
		{
			name:             "success without base provider",
			mockSetup:        func(*mockUploadClient, *mockBaseBlobProvider) {},
			wantBlockIDEmpty: true,
			wantSize:         0,
		},
		{
			name: "success with base provider",
			mockSetup: func(client *mockUploadClient, provider *mockBaseBlobProvider) {
				offset := int64(100)
				size := int64(200)
				provider.expectGetOutputBlockURL("test-url", offset, size, nil)
				provider.expectDownloadOutputs([]*v1.ActionsOutput{}, nil)
				client.expectUploadBlockFromURL(offset, size, nil)
			},
			checkBaseFunc:    true,
			wantBlockIDEmpty: false,
			wantSize:         200,
		},
		{
			name: "GetOutputBlockURL error",
			mockSetup: func(_ *mockUploadClient, provider *mockBaseBlobProvider) {
				provider.expectGetOutputBlockURL("", 0, 0, errors.New("get url error"))
				provider.expectDownloadOutputs([]*v1.ActionsOutput{}, nil)
			},
			checkBaseFunc: true,
			expectError:   true,
			wantSize:      0,
		},
		{
			name: "DownloadOutputs error",
			mockSetup: func(_ *mockUploadClient, provider *mockBaseBlobProvider) {
				provider.expectGetOutputBlockURL("test-url", 100, 200, nil)
				provider.expectDownloadOutputs(nil, errors.New("download error"))
			},
			checkBaseFunc: true,
			expectError:   true,
			wantSize:      0,
		},
		{
			name: "UploadBlockFromURL error",
			mockSetup: func(client *mockUploadClient, provider *mockBaseBlobProvider) {
				offset := int64(100)
				size := int64(200)
				provider.expectGetOutputBlockURL("test-url", offset, size, nil)
				provider.expectDownloadOutputs([]*v1.ActionsOutput{}, nil)
				client.expectUploadBlockFromURL(offset, size, errors.New("upload error"))
			},
			checkBaseFunc: true,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockUploadClient{}
			provider := &mockBaseBlobProvider{}
			tt.mockSetup(client, provider)

			var baseProvider BaseBlobProvider
			if tt.checkBaseFunc {
				baseProvider = provider
			}

			uploader := NewUploader(t.Context(), log.DefaultLogger, client, baseProvider)
			if uploader == nil {
				t.Fatal("uploader is nil")
			}

			blockID, size, outputs, err := uploader.waitBaseFunc()
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.wantBlockIDEmpty && blockID != "" {
				t.Errorf("blockID should be empty, got %s", blockID)
			}
			if diff := cmp.Diff(tt.wantSize, size); diff != "" {
				t.Errorf("size mismatch (-want +got):\n%s", diff)
			}
			if !tt.checkBaseFunc && len(outputs) != 0 {
				t.Error("outputs should be empty for nil base provider")
			}
		})
	}
}

func TestUploader_UploadOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		outputID    string
		size        int64
		setupMock   func(*mockUploadClient) (io.ReadSeekCloser, error)
		expectError bool
	}{
		{
			name:     "success",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mockUploadClient) (io.ReadSeekCloser, error) {
				data := make([]byte, 100)
				compressedData, err := zstd.Compress(nil, data)
				if err != nil {
					return nil, err
				}
				client.expectUploadBlock("test-output", int64(len(compressedData)), nil)
				return myio.NopSeekCloser(bytes.NewReader(data)), nil
			},
		},
		{
			name:     "size mismatch",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mockUploadClient) (io.ReadSeekCloser, error) {
				data := make([]byte, 50)
				compressedData, err := zstd.Compress(nil, data)
				if err != nil {
					return nil, err
				}
				client.expectUploadBlock("test-output", int64(len(compressedData)), nil)
				return myio.NopSeekCloser(bytes.NewReader(data)), nil
			},
		},
		{
			name:     "upload error",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mockUploadClient) (io.ReadSeekCloser, error) {
				data := make([]byte, 100)
				compressedData, err := zstd.Compress(nil, data)
				if err != nil {
					return nil, err
				}
				client.expectUploadBlock("test-output", int64(len(compressedData)), errors.New("upload error"))
				return myio.NopSeekCloser(bytes.NewReader(data)), nil
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockUploadClient{}
			uploader := NewUploader(t.Context(), log.DefaultLogger, client, &mockBaseBlobProvider{})

			reader, err := tt.setupMock(client)
			if err != nil {
				t.Fatalf("failed to setup mock: %v", err)
			}
			err = uploader.UploadOutput(t.Context(), tt.outputID, tt.size, reader)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestUploader_Commit(t *testing.T) {
	t.Parallel()

	baseOutputs := []*v1.ActionsOutput{
		{
			Id:     "base",
			Offset: 0,
			Size:   50,
		},
	}

	tests := []struct {
		name          string
		entries       map[string]*v1.IndexEntry
		setupUploader func(context.Context, *mockUploadClient, *mockBaseBlobProvider) *Uploader
		expectError   bool
		validateState func(*testing.T, *Uploader)
	}{
		{
			name: "success with no new outputs",
			entries: map[string]*v1.IndexEntry{
				"test": {
					OutputId:   "test",
					Size:       100,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
			setupUploader: func(ctx context.Context, client *mockUploadClient, provider *mockBaseBlobProvider) *Uploader {
				provider.expectGetOutputBlockURL("test-url", 0, 100, nil)
				provider.expectDownloadOutputs(slices.Clone(baseOutputs), nil)
				client.expectUploadBlockFromURL(0, 100, nil)
				client.expectAnyUploadBlock(50, nil)
				client.expectCommit(nil)
				return NewUploader(ctx, log.DefaultLogger, client, provider)
			},
		},
		{
			name: "success with new outputs",
			entries: map[string]*v1.IndexEntry{
				"test": {
					OutputId:   "test",
					Size:       100,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
			setupUploader: func(ctx context.Context, client *mockUploadClient, provider *mockBaseBlobProvider) *Uploader {
				provider.expectGetOutputBlockURL("test-url", 0, 100, nil)
				provider.expectDownloadOutputs(slices.Clone(baseOutputs), nil)
				client.expectUploadBlockFromURL(0, 100, nil)
				client.expectAnyUploadBlock(50, nil)
				client.expectCommit(nil)

				uploader := NewUploader(ctx, log.DefaultLogger, client, provider)
				uploader.outputs = []*v1.ActionsOutput{
					{
						Id:          "new-output",
						Offset:      100,
						Size:        150,
						Compression: v1.Compression_COMPRESSION_ZSTD,
					},
				}
				return uploader
			},
			validateState: func(t *testing.T, u *Uploader) {
				u.outputsLocker.RLock()
				defer u.outputsLocker.RUnlock()
				if diff := cmp.Diff([]*v1.ActionsOutput{
					{
						Id:          "new-output",
						Offset:      100,
						Size:        150,
						Compression: v1.Compression_COMPRESSION_ZSTD,
					},
				}, u.outputs, cmpopts.IgnoreUnexported(v1.ActionsOutput{})); diff != "" {
					t.Errorf("outputs mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "commit error",
			entries: map[string]*v1.IndexEntry{
				"test": {
					OutputId:   "test",
					Size:       100,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
			setupUploader: func(ctx context.Context, client *mockUploadClient, provider *mockBaseBlobProvider) *Uploader {
				provider.expectGetOutputBlockURL("test-url", 0, 100, nil)
				provider.expectDownloadOutputs(slices.Clone(baseOutputs), nil)
				client.expectUploadBlockFromURL(0, 100, nil)
				client.expectAnyUploadBlock(50, nil)
				client.expectCommit(errors.New("commit error"))
				return NewUploader(ctx, log.DefaultLogger, client, provider)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockUploadClient{}
			provider := &mockBaseBlobProvider{}
			uploader := tt.setupUploader(t.Context(), client, provider)

			_, err := uploader.Commit(t.Context(), tt.entries)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tt.validateState != nil {
				tt.validateState(t, uploader)
			}
		})
	}
}

func TestUploader_createHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		entries        map[string]*v1.IndexEntry
		outputs        []*v1.ActionsOutput
		outputSize     int64
		expectError    bool
		validateHeader func(*testing.T, []byte)
	}{
		{
			name: "success",
			entries: map[string]*v1.IndexEntry{
				"test": {
					OutputId:   "test",
					Size:       100,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
			outputs: []*v1.ActionsOutput{
				{
					Id:     "test",
					Offset: 0,
					Size:   100,
				},
			},
			outputSize: 100,
			validateHeader: func(t *testing.T, headerBytes []byte) {
				if len(headerBytes) <= 8 {
					t.Error("header is too short")
					return
				}

				headerLen := binary.BigEndian.Uint64(headerBytes[:8])
				totalLen := uint64(len(headerBytes))
				if totalLen <= 8 || headerLen != totalLen-8 {
					t.Errorf("header size mismatch: headerLen=%d, totalLen=%d", headerLen, totalLen)
					return
				}

				var header v1.ActionsCache
				if err := proto.Unmarshal(headerBytes[8:], &header); err != nil {
					t.Errorf("failed to unmarshal header: %v", err)
					return
				}

				if diff := cmp.Diff("test", header.Entries["test"].OutputId, protocmp.Transform()); diff != "" {
					t.Errorf("entry OutputId mismatch (-want +got):\n%s", diff)
				}

				if diff := cmp.Diff([]*v1.ActionsOutput{
					{
						Id:          "test",
						Offset:      0,
						Size:        100,
						Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
					},
				}, header.Outputs, cmpopts.IgnoreUnexported(v1.ActionsOutput{})); diff != "" {
					t.Errorf("outputs mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(int64(100), header.OutputTotalSize); diff != "" {
					t.Errorf("output total size mismatch (-want +got):\n%s", diff)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uploader := &Uploader{}

			header, err := uploader.createHeader(tt.entries, tt.outputs, tt.outputSize)
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.validateHeader != nil {
				tt.validateHeader(t, header)
			}
		})
	}
}

func TestUploader_constructOutputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		baseOutputSize int64
		baseOutputs    []*v1.ActionsOutput
		outputs        []*v1.ActionsOutput
		wantOutputIDs  []string
		wantOutputs    []*v1.ActionsOutput
		wantOffset     int64
	}{
		{
			name:           "empty base outputs",
			baseOutputSize: 0,
			baseOutputs:    []*v1.ActionsOutput{},
			outputs: []*v1.ActionsOutput{
				{
					Id:   "output1",
					Size: 100,
				},
				{
					Id:   "output2",
					Size: 200,
				},
			},
			wantOutputIDs: []string{"output1", "output2"},
			wantOutputs: []*v1.ActionsOutput{
				{
					Id:     "output1",
					Offset: 0,
					Size:   100,
				},
				{
					Id:     "output2",
					Offset: 100,
					Size:   200,
				},
			},
			wantOffset: 300,
		},
		{
			name:           "empty new outputs",
			baseOutputSize: 100,
			baseOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   50,
				},
				{
					Id:     "base2",
					Offset: 50,
					Size:   50,
				},
			},
			outputs:       []*v1.ActionsOutput{},
			wantOutputIDs: []string{},
			wantOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   50,
				},
				{
					Id:     "base2",
					Offset: 50,
					Size:   50,
				},
			},
			wantOffset: 100,
		},
		{
			name:           "no duplicates",
			baseOutputSize: 150,
			baseOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   50,
				},
				{
					Id:     "base2",
					Offset: 50,
					Size:   100,
				},
			},
			outputs: []*v1.ActionsOutput{
				{
					Id:   "output1",
					Size: 200,
				},
				{
					Id:   "output2",
					Size: 300,
				},
			},
			wantOutputIDs: []string{"output1", "output2"},
			wantOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   50,
				},
				{
					Id:     "base2",
					Offset: 50,
					Size:   100,
				},
				{
					Id:     "output1",
					Offset: 150,
					Size:   200,
				},
				{
					Id:     "output2",
					Offset: 350,
					Size:   300,
				},
			},
			wantOffset: 650,
		},
		{
			name:           "with duplicates",
			baseOutputSize: 200,
			baseOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   50,
				},
				{
					Id:     "duplicate",
					Offset: 50,
					Size:   150,
				},
			},
			outputs: []*v1.ActionsOutput{
				{
					Id:   "duplicate", // この出力はベース出力と重複するため結合結果に追加されない
					Size: 100,
				},
				{
					Id:   "output1",
					Size: 250,
				},
			},
			wantOutputIDs: []string{"output1"},
			wantOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   50,
				},
				{
					Id:     "duplicate",
					Offset: 50,
					Size:   150,
				},
				{
					Id:     "output1",
					Offset: 200,
					Size:   250,
				},
			},
			wantOffset: 450,
		},
		{
			name:           "with zero size outputs",
			baseOutputSize: 100,
			baseOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   100,
				},
			},
			outputs: []*v1.ActionsOutput{
				{
					Id:   "zero",
					Size: 0,
				},
				{
					Id:   "output1",
					Size: 150,
				},
			},
			wantOutputIDs: []string{"output1"}, // サイズが0の出力はwantOutputIDsに含まれない
			wantOutputs: []*v1.ActionsOutput{
				{
					Id:     "base1",
					Offset: 0,
					Size:   100,
				},
				{
					Id:     "zero",
					Offset: 100,
					Size:   0,
				},
				{
					Id:     "output1",
					Offset: 100,
					Size:   150,
				},
			},
			wantOffset: 250,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uploader := &Uploader{
				outputs: tt.outputs,
			}

			gotOutputIDs, gotOutputs, gotOffset := uploader.constructOutputs(tt.baseOutputSize, tt.baseOutputs)

			if diff := cmp.Diff(tt.wantOutputIDs, gotOutputIDs); diff != "" {
				t.Errorf("output IDs mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.wantOutputs, gotOutputs, cmpopts.IgnoreUnexported(v1.ActionsOutput{})); diff != "" {
				t.Errorf("outputs mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.wantOffset, gotOffset); diff != "" {
				t.Errorf("offset mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
