package blob

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/DataDog/zstd"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	myio "github.com/mazrean/gocica/internal/pkg/io"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote/blob/mock"
	"github.com/mazrean/gocica/log"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestNewUploader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mockSetup        func(*mock.MockUploadClient, *mock.MockBaseBlobProvider)
		expectError      bool
		checkBaseFunc    bool
		wantBlockIDEmpty bool
		wantSize         int64
	}{
		{
			name:             "success without base provider",
			mockSetup:        func(*mock.MockUploadClient, *mock.MockBaseBlobProvider) {},
			wantBlockIDEmpty: true,
			wantSize:         0,
		},
		{
			name: "success with base provider",
			mockSetup: func(client *mock.MockUploadClient, provider *mock.MockBaseBlobProvider) {
				offset := int64(100)
				size := int64(200)
				provider.EXPECT().GetEntries(gomock.Any()).Return(map[string]*v1.IndexEntry{}, nil)
				provider.EXPECT().GetOutputBlockURL(gomock.Any()).Return("test-url", offset, size, nil)
				provider.EXPECT().GetOutputs(gomock.Any()).Return([]*v1.ActionsOutput{}, nil)
				client.EXPECT().UploadBlockFromURL(gomock.Any(), gomock.Any(), "test-url", offset, size).Return(nil)
			},
			checkBaseFunc:    true,
			wantBlockIDEmpty: false,
			wantSize:         200,
		},
		{
			name: "GetOutputBlockURL error",
			mockSetup: func(_ *mock.MockUploadClient, provider *mock.MockBaseBlobProvider) {
				provider.EXPECT().GetEntries(gomock.Any()).Return(map[string]*v1.IndexEntry{}, nil)
				provider.EXPECT().GetOutputBlockURL(gomock.Any()).Return("", int64(0), int64(0), errors.New("get url error"))
				provider.EXPECT().GetOutputs(gomock.Any()).Return([]*v1.ActionsOutput{}, nil)
			},
			checkBaseFunc: true,
			expectError:   true,
			wantSize:      0,
		},
		{
			name: "UploadBlockFromURL error",
			mockSetup: func(client *mock.MockUploadClient, provider *mock.MockBaseBlobProvider) {
				offset := int64(100)
				size := int64(200)
				provider.EXPECT().GetEntries(gomock.Any()).Return(map[string]*v1.IndexEntry{}, nil)
				provider.EXPECT().GetOutputBlockURL(gomock.Any()).Return("test-url", offset, size, nil)
				provider.EXPECT().GetOutputs(gomock.Any()).Return([]*v1.ActionsOutput{}, nil)
				client.EXPECT().UploadBlockFromURL(gomock.Any(), gomock.Any(), "test-url", offset, size).Return(errors.New("upload error"))
			},
			checkBaseFunc: true,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := mock.NewMockUploadClient(gomock.NewController(t))
			provider := mock.NewMockBaseBlobProvider(gomock.NewController(t))
			tt.mockSetup(client, provider)

			var baseProvider BaseBlobProvider
			if tt.checkBaseFunc {
				baseProvider = provider
			}

			uploader, err := NewUploader(t.Context(), log.DefaultLogger, client, baseProvider)
			if err != nil {
				t.Fatalf("failed to create uploader: %v", err)
			}
			if uploader == nil {
				t.Fatal("uploader is nil")
			}

			baseBlockIDs, size, outputs, err := uploader.waitBaseFunc()
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

			if tt.wantBlockIDEmpty && len(baseBlockIDs) > 0 {
				t.Errorf("baseBlockIDs should be empty, got %v", baseBlockIDs)
			}
			if !tt.wantBlockIDEmpty && len(baseBlockIDs) == 0 {
				t.Error("baseBlockIDs should not be empty")
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
		name          string
		actionID      string
		outputID      string
		size          int64
		setupMock     func(*mock.MockUploadClient) (io.ReadSeekCloser, error)
		expectOutputs []*v1.ActionsOutput
		expectHeader  map[string]*v1.IndexEntry
		expectError   bool
	}{
		{
			name:     "success",
			actionID: "test-action",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mock.MockUploadClient) (io.ReadSeekCloser, error) {
				data := make([]byte, 100)
				client.EXPECT().UploadBlock(gomock.Any(), "test-output", gomock.Cond(func(r io.ReadSeekCloser) bool {
					buf := bytes.NewBuffer(nil)
					_, err := io.Copy(buf, r)
					if err != nil {
						t.Fatalf("failed to copy: %v", err)
					}

					return bytes.Equal(buf.Bytes(), data)
				})).Return(int64(len(data)), nil)
				return myio.NopSeekCloser(bytes.NewReader(data)), nil
			},
			expectOutputs: []*v1.ActionsOutput{
				{
					Id:   "test-output",
					Size: 100,
				},
			},
			expectHeader: map[string]*v1.IndexEntry{
				"test-action": {
					OutputId:   "test-output",
					Size:       100,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
		},
		{
			name:     "success with large size",
			actionID: "test-action",
			outputID: "test-output",
			size:     200 * (2 ^ 10),
			setupMock: func(client *mock.MockUploadClient) (io.ReadSeekCloser, error) {
				data := make([]byte, 200*(2^10))
				client.EXPECT().UploadBlock(gomock.Any(), "test-output", gomock.Cond(func(r io.ReadSeekCloser) bool {
					buf := bytes.NewBuffer(nil)
					zw := zstd.NewDecompressWriter(buf)

					_, err := io.Copy(zw, r)
					if err != nil {
						t.Fatalf("failed to copy: %v", err)
					}

					if err := zw.Close(); err != nil {
						t.Fatalf("failed to close: %v", err)
					}

					return bytes.Equal(buf.Bytes(), data)
				})).Return(int64(len(data)), nil)
				return myio.NopSeekCloser(bytes.NewReader(data)), nil
			},
			expectOutputs: []*v1.ActionsOutput{
				{
					Id:          "test-output",
					Size:        200 * (2 ^ 10),
					Compression: v1.Compression_COMPRESSION_ZSTD,
				},
			},
			expectHeader: map[string]*v1.IndexEntry{
				"test-action": {
					OutputId:   "test-output",
					Size:       200 * (2 ^ 10),
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
		},
		{
			name:     "success with empty size",
			actionID: "test-action",
			outputID: "test-output",
			size:     0,
			setupMock: func(*mock.MockUploadClient) (io.ReadSeekCloser, error) {
				return myio.NopSeekCloser(bytes.NewReader(nil)), nil
			},
			expectOutputs: []*v1.ActionsOutput{
				{
					Id:          "test-output",
					Offset:      0,
					Size:        0,
					Compression: v1.Compression_COMPRESSION_UNSPECIFIED,
				},
			},
			expectHeader: map[string]*v1.IndexEntry{
				"test-action": {
					OutputId:   "test-output",
					Size:       0,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
		},
		{
			name:     "upload error",
			actionID: "test-action",
			outputID: "test-output",
			size:     100,
			setupMock: func(client *mock.MockUploadClient) (io.ReadSeekCloser, error) {
				data := make([]byte, 100)
				client.EXPECT().UploadBlock(gomock.Any(), "test-output", gomock.Cond(func(r io.ReadSeekCloser) bool {
					buf := bytes.NewBuffer(nil)
					_, err := io.Copy(buf, r)
					if err != nil {
						t.Fatalf("failed to copy: %v", err)
					}

					return bytes.Equal(buf.Bytes(), data)
				})).Return(int64(len(data)), errors.New("upload error"))
				return myio.NopSeekCloser(bytes.NewReader(data)), nil
			},
			expectError:  true,
			expectHeader: map[string]*v1.IndexEntry{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := mock.NewMockUploadClient(gomock.NewController(t))
			uploader := &Uploader{
				logger:       log.DefaultLogger,
				client:       client,
				nowTimestamp: timestamppb.Now(),
				header:       make(map[string]*v1.IndexEntry),
				outputMap:    make(map[string]int64),
			}

			reader, err := tt.setupMock(client)
			if err != nil {
				t.Fatalf("failed to setup mock: %v", err)
			}
			err = uploader.UploadOutput(t.Context(), tt.actionID, tt.outputID, tt.size, reader)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if diff := cmp.Diff(tt.expectOutputs, uploader.outputs, cmpopts.IgnoreUnexported(v1.ActionsOutput{})); diff != "" {
				t.Errorf("outputs mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.expectHeader, uploader.header,
				cmpopts.IgnoreUnexported(v1.IndexEntry{}),
				cmpopts.IgnoreTypes(timestamppb.Timestamp{}),
				cmpopts.IgnoreFields(v1.IndexEntry{}, "Timenano"),
			); diff != "" {
				t.Errorf("header mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUploader_Commit(t *testing.T) {
	t.Parallel()

	baseOutputsClone := func() []*v1.ActionsOutput {
		return []*v1.ActionsOutput{
			{
				Id:     "base",
				Offset: 0,
				Size:   50,
			},
		}
	}

	tests := []struct {
		name          string
		outputs       []*v1.ActionsOutput
		entries       map[string]*v1.IndexEntry
		setupUploader func(*mock.MockUploadClient, []*v1.ActionsOutput, map[string]*v1.IndexEntry) *Uploader
		expectError   bool
		validateState func(*testing.T, *Uploader)
	}{
		{
			name: "success with no new outputs",
			outputs: []*v1.ActionsOutput{
				{
					Id:     "test",
					Offset: 0,
					Size:   100,
				},
			},
			entries: map[string]*v1.IndexEntry{
				"test": {
					OutputId:   "test",
					Size:       100,
					Timenano:   time.Now().UnixNano(),
					LastUsedAt: timestamppb.Now(),
				},
			},
			setupUploader: func(client *mock.MockUploadClient, outputs []*v1.ActionsOutput, entries map[string]*v1.IndexEntry) *Uploader {
				client.EXPECT().UploadBlock(gomock.Any(), gomock.Any(), gomock.Cond(func(r io.ReadSeekCloser) bool {
					buf := bytes.NewBuffer(nil)
					_, err := io.Copy(buf, r)
					if err != nil {
						t.Fatalf("failed to copy: %v", err)
					}

					var header v1.ActionsCache
					if err := proto.Unmarshal(buf.Bytes()[8:], &header); err != nil {
						t.Fatalf("failed to unmarshal: %v", err)
					}

					return cmp.Equal(entries, header.Entries,
						cmpopts.IgnoreUnexported(v1.IndexEntry{}),
						cmpopts.IgnoreTypes(timestamppb.Timestamp{}),
						cmpopts.IgnoreFields(v1.IndexEntry{}, "Timenano"),
					)
				})).Return(int64(8), nil)
				client.EXPECT().Commit(t.Context(), gomock.Cond(func(ids []string) bool {
					return len(ids) == 2
				})).Return(nil)

				uploader := &Uploader{
					logger:  log.DefaultLogger,
					client:  client,
					header:  entries,
					outputs: outputs,
					waitBaseFunc: func() ([]string, int64, []*v1.ActionsOutput, error) {
						return nil, 0, nil, nil
					},
				}

				return uploader
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
			setupUploader: func(client *mock.MockUploadClient, outputs []*v1.ActionsOutput, entries map[string]*v1.IndexEntry) *Uploader {
				client.EXPECT().UploadBlock(gomock.Any(), gomock.Any(), gomock.Cond(func(r io.ReadSeekCloser) bool {
					buf := bytes.NewBuffer(nil)
					_, err := io.Copy(buf, r)
					if err != nil {
						t.Fatalf("failed to copy: %v", err)
					}

					var header v1.ActionsCache
					if err := proto.Unmarshal(buf.Bytes()[8:], &header); err != nil {
						t.Fatalf("failed to unmarshal: %v", err)
					}

					return cmp.Equal(entries, header.Entries,
						cmpopts.IgnoreUnexported(v1.IndexEntry{}),
						cmpopts.IgnoreTypes(timestamppb.Timestamp{}),
						cmpopts.IgnoreFields(v1.IndexEntry{}, "Timenano"),
					)
				})).Return(int64(8), nil)
				client.EXPECT().Commit(gomock.Any(), gomock.Cond(func(ids []string) bool {
					return len(ids) == 2
				})).Return(errors.New("commit error"))

				uploader := &Uploader{
					logger:  log.DefaultLogger,
					client:  client,
					header:  entries,
					outputs: outputs,
					waitBaseFunc: func() ([]string, int64, []*v1.ActionsOutput, error) {
						return nil, 0, nil, nil
					},
				}

				return uploader
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := mock.NewMockUploadClient(gomock.NewController(t))
			uploader := tt.setupUploader(client, baseOutputsClone(), tt.entries)
			if uploader == nil {
				t.Fatal("uploader is nil")
			}

			uploader.header = tt.entries

			_, err := uploader.Commit(t.Context())

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
