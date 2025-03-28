package io

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type errorWriter struct {
	err error
}

func (w *errorWriter) Write([]byte) (n int, err error) {
	return 0, w.err
}

func (w *errorWriter) Close() error {
	return nil
}

type bufferCloser struct {
	*bytes.Buffer
	closed bool
}

func newBufferCloser() *bufferCloser {
	return &bufferCloser{
		Buffer: &bytes.Buffer{},
		closed: false,
	}
}

func (b *bufferCloser) Close() error {
	b.closed = true
	return nil
}

func TestJoinedWriter(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		writers []struct {
			size  int64
			isErr bool
		}
		expectedWrites []string
		expectedN      int
		expectedErr    error
	}{
		{
			name: "write to single writer",
			data: []byte("hello"),
			writers: []struct {
				size  int64
				isErr bool
			}{
				{size: 10, isErr: false},
			},
			expectedWrites: []string{"hello"},
			expectedN:      5,
			expectedErr:    nil,
		},
		{
			name: "split write across multiple writers",
			data: []byte("hello"),
			writers: []struct {
				size  int64
				isErr bool
			}{
				{size: 3, isErr: false},
				{size: 3, isErr: false},
			},
			expectedWrites: []string{"hel", "lo"},
			expectedN:      5,
			expectedErr:    nil,
		},
		{
			name: "skip writer with zero size",
			data: []byte("hello"),
			writers: []struct {
				size  int64
				isErr bool
			}{
				{size: 0, isErr: false},
				{size: 5, isErr: false},
			},
			expectedWrites: []string{"", "hello"},
			expectedN:      5,
			expectedErr:    nil,
		},
		{
			name: "handle write error",
			data: []byte("hello"),
			writers: []struct {
				size  int64
				isErr bool
			}{
				{size: 2, isErr: false},
				{size: 3, isErr: true},
			},
			expectedWrites: []string{"he", ""},
			expectedN:      2,
			expectedErr:    errors.New("write failed"),
		},
		{
			name: "write empty byte slice",
			data: []byte{},
			writers: []struct {
				size  int64
				isErr bool
			}{
				{size: 10, isErr: false},
			},
			expectedWrites: []string{""},
			expectedN:      0,
			expectedErr:    nil,
		},
		{
			name: "write exceeding size limit",
			data: []byte("hello"),
			writers: []struct {
				size  int64
				isErr bool
			}{
				{size: 3, isErr: false},
			},
			expectedWrites: []string{"hel"},
			expectedN:      3,
			expectedErr:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test writers and buffers
			var writers []WriterWithSize
			buffers := make([]*bufferCloser, len(tt.writers))

			for i, w := range tt.writers {
				var writer io.WriteCloser
				if w.isErr {
					writer = &errorWriter{err: tt.expectedErr}
				} else {
					buffers[i] = newBufferCloser()
					writer = buffers[i]
				}
				writers = append(writers, WriterWithSize{
					Writer: writer,
					Size:   w.size,
				})
			}

			// Execute test
			jw := NewJoinedWriter(writers...)
			n, err := jw.Write(tt.data)

			// Assert results
			if tt.expectedErr == nil {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err.Error() != tt.expectedErr.Error() {
					t.Errorf("expected error %v, got %v", tt.expectedErr, err)
				}
				return
			}

			if n != tt.expectedN {
				t.Errorf("expected %d bytes written, got %d", tt.expectedN, n)
			}

			for i, expectedWrite := range tt.expectedWrites {
				if i >= len(buffers) || buffers[i] == nil {
					continue
				}
				if got := buffers[i].String(); got != expectedWrite {
					t.Errorf("writer[%d]: expected %q, got %q", i, expectedWrite, got)
				}
			}
		})
	}
}
