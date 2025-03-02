package io

import "io"

var EmptyReader io.Reader = &emptyReader{}

type emptyReader struct{}

func (e *emptyReader) Read([]byte) (n int, err error) {
	return 0, io.EOF
}
