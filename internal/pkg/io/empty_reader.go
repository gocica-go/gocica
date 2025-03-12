package io

import "io"

var EmptyReader io.ReadSeeker = &emptyReader{}

type emptyReader struct{}

func (e *emptyReader) Read([]byte) (n int, err error) {
	return 0, io.EOF
}

func (e *emptyReader) Seek(int64, int) (int64, error) {
	return 0, nil
}
