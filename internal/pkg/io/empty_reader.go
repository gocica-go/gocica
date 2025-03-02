package io

import "io"

var EmptyReader io.Reader = &emptyReader{}

type emptyReader struct{}

func (e *emptyReader) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}
