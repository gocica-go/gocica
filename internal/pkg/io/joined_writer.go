package io

import "io"

type WriterWithSize struct {
	Writer io.WriteCloser // Changed from io.Writer to io.WriteCloser
	Size   int64
}

type JoinedWriter struct {
	writers   []WriterWithSize
	curWriter int // current writer index
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
		if writer.Size <= 0 {
			// Close writers with size <= 0 and move to the next writer
			if closeErr := writer.Writer.Close(); closeErr != nil {
				return totalWritten, closeErr
			}
			j.curWriter++
			continue
		}

		// determine the size to write
		writeSize := int64(len(remaining))
		if writeSize > writer.Size {
			writeSize = writer.Size
		}

		// execute the actual write
		written, writeErr := writer.Writer.Write(remaining[:writeSize])
		totalWritten += written
		writer.Size -= int64(written)

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
