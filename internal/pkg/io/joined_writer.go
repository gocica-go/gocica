package io

import "io"

type WriterWithSize struct {
	writer io.Writer
	size   int64
}

type JoinedWriter struct {
	writers   []WriterWithSize
	curWriter int // 現在のwriterのインデックス
}

func NewJoinedWriter(writers ...WriterWithSize) *JoinedWriter {
	return &JoinedWriter{
		writers:   writers,
		curWriter: 0,
	}
}

func (j *JoinedWriter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	remaining := p
	totalWritten := 0

	for j.curWriter < len(j.writers) {
		writer := &j.writers[j.curWriter]
		if writer.size <= 0 {
			j.curWriter++
			continue
		}

		// 書き込むサイズを決定
		writeSize := int64(len(remaining))
		if writeSize > writer.size {
			writeSize = writer.size
		}

		// 実際の書き込みを実行
		written, writeErr := writer.writer.Write(remaining[:writeSize])
		totalWritten += written
		writer.size -= int64(written)

		if writeErr != nil {
			return totalWritten, writeErr
		}

		if written < len(remaining) {
			remaining = remaining[written:]
		} else {
			return totalWritten, nil
		}
	}

	return totalWritten, nil
}
