package blob

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
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

func (m *mockUploadClient) UploadBlock(blobID string, _ io.ReadSeekCloser) (int64, error) {
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

func (m *mockUploadClient) UploadBlockFromURL(_, url string, offset, size int64) error {
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

func (m *mockUploadClient) Commit(_ []string) error {
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

func (m *mockBaseBlobProvider) GetOutputs() (map[string]*v1.ActionsOutput, error) {
	for i := len(m.calls) - 1; i >= 0; i-- {
		call := m.calls[i]
		if call.method == "DownloadOutputs" {
			outputs := make(map[string]*v1.ActionsOutput)
			if call.result[0] != nil {
				if out, ok := call.result[0].(map[string]*v1.ActionsOutput); ok {
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

func (m *mockBaseBlobProvider) GetOutputBlockURL() (string, int64, int64, error) {
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

func (m *mockBaseBlobProvider) expectDownloadOutputs(outputs map[string]*v1.ActionsOutput, err error) {
	m.calls = append(m.calls, mockCall{
		method: "DownloadOutputs",
		result: []any{outputs, err},
	})
}

func TestNewUploader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mockSetup   func(*mockUploadClient, *mockBaseBlobProvider)
		expectError bool
	}{
		{
			name: "success",
			mockSetup: func(client *mockUploadClient, provider *mockBaseBlobProvider) {
				offset := int64(100)
				size := int64(200)
				provider.expectGetOutputBlockURL("test-url", offset, size, nil)
				provider.expectDownloadOutputs(map[string]*v1.ActionsOutput{}, nil)
				client.expectUploadBlockFromURL(offset, size, nil)
			},
		},
		{
			name: "GetOutputBlockURL error",
			mockSetup: func(_ *mockUploadClient, provider *mockBaseBlobProvider) {
				provider.expectGetOutputBlockURL("", 0, 0, errors.New("get url error"))
				provider.expectDownloadOutputs(map[string]*v1.ActionsOutput{}, nil)
			},
			expectError: true,
		},
		{
			name: "DownloadOutputs error",
			mockSetup: func(_ *mockUploadClient, provider *mockBaseBlobProvider) {
				provider.expectGetOutputBlockURL("test-url", 100, 200, nil)
				provider.expectDownloadOutputs(nil, errors.New("download error"))
			},
			expectError: true,
		},
		{
			name: "UploadBlockFromURL error",
			mockSetup: func(client *mockUploadClient, provider *mockBaseBlobProvider) {
				offset := int64(100)
				size := int64(200)
				provider.expectGetOutputBlockURL("test-url", offset, size, nil)
				provider.expectDownloadOutputs(map[string]*v1.ActionsOutput{}, nil)
				client.expectUploadBlockFromURL(offset, size, errors.New("upload error"))
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
			tt.mockSetup(client, provider)

			uploader := NewUploader(client, provider)
			if uploader == nil {
				t.Fatal("uploader is nil")
			}

			_, _, _, err := uploader.waitBaseFunc()
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

func TestUploader_UploadOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		outputID    string
		size        int64
		setupMock   func(*mockUploadClient) io.ReadSeekCloser
		expectError bool
	}{
		{
			name:     "success",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mockUploadClient) io.ReadSeekCloser {
				data := bytes.NewReader(make([]byte, 100))
				client.expectUploadBlock("test-output", 100, nil)
				return myio.NopSeekCloser(data)
			},
		},
		{
			name:     "size mismatch",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mockUploadClient) io.ReadSeekCloser {
				data := bytes.NewReader(make([]byte, 50))
				client.expectUploadBlock("test-output", 50, nil)
				return myio.NopSeekCloser(data)
			},
			expectError: true,
		},
		{
			name:     "upload error",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mockUploadClient) io.ReadSeekCloser {
				data := bytes.NewReader(make([]byte, 100))
				client.expectUploadBlock("test-output", 0, errors.New("upload error"))
				return myio.NopSeekCloser(data)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockUploadClient{}
			uploader := NewUploader(client, &mockBaseBlobProvider{})

			reader := tt.setupMock(client)
			err := uploader.UploadOutput(tt.outputID, tt.size, reader)

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

	baseOutputs := map[string]*v1.ActionsOutput{
		"base": {
			Offset: 0,
			Size:   50,
		},
	}

	tests := []struct {
		name          string
		entries       map[string]*v1.IndexEntry
		setupUploader func(*mockUploadClient, *mockBaseBlobProvider) *Uploader
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
			setupUploader: func(client *mockUploadClient, provider *mockBaseBlobProvider) *Uploader {
				provider.expectGetOutputBlockURL("test-url", 0, 100, nil)
				provider.expectDownloadOutputs(baseOutputs, nil)
				client.expectUploadBlockFromURL(0, 100, nil)
				client.expectAnyUploadBlock(50, nil)
				client.expectCommit(nil)
				return NewUploader(client, provider)
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
			setupUploader: func(client *mockUploadClient, provider *mockBaseBlobProvider) *Uploader {
				provider.expectGetOutputBlockURL("test-url", 0, 100, nil)
				provider.expectDownloadOutputs(baseOutputs, nil)
				client.expectUploadBlockFromURL(0, 100, nil)
				client.expectAnyUploadBlock(50, nil)
				client.expectCommit(nil)

				uploader := NewUploader(client, provider)
				uploader.outputSizeMap["new-output"] = 200
				return uploader
			},
			validateState: func(t *testing.T, u *Uploader) {
				u.outputSizeMapLocker.RLock()
				defer u.outputSizeMapLocker.RUnlock()
				if diff := cmp.Diff(int64(200), u.outputSizeMap["new-output"]); diff != "" {
					t.Errorf("outputSizeMap mismatch (-want +got):\n%s", diff)
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
			setupUploader: func(client *mockUploadClient, provider *mockBaseBlobProvider) *Uploader {
				provider.expectGetOutputBlockURL("test-url", 0, 100, nil)
				provider.expectDownloadOutputs(baseOutputs, nil)
				client.expectUploadBlockFromURL(0, 100, nil)
				client.expectAnyUploadBlock(50, nil)
				client.expectCommit(errors.New("commit error"))
				return NewUploader(client, provider)
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
			uploader := tt.setupUploader(client, provider)

			err := uploader.Commit(tt.entries)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else if err != nil {
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
		outputs        map[string]*v1.ActionsOutput
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
			outputs: map[string]*v1.ActionsOutput{
				"test": {
					Offset: 0,
					Size:   100,
				},
			},
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
				if diff := cmp.Diff(int64(0), header.Outputs["test"].Offset); diff != "" {
					t.Errorf("output Offset mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(int64(100), header.Outputs["test"].Size); diff != "" {
					t.Errorf("output Size mismatch (-want +got):\n%s", diff)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uploader := &Uploader{}

			header, err := uploader.createHeader(tt.entries, tt.outputs)
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
