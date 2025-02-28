package io

import (
	"bytes"
	"errors"
	"io"
)

type DelimReader struct {
	r       io.Reader
	delim   byte
	buf     []byte
	reached bool
	eof     bool
}

func NewDelimReader(r io.Reader, delim byte) *DelimReader {
	return &DelimReader{r: r, delim: delim, buf: make([]byte, 0, 1024)}
}

func (d *DelimReader) Read(p []byte) (int, error) {
	var (
		useBuf = len(d.buf) > 0
		n      int
		tmp    []byte
	)
	if useBuf {
		n = min(len(d.buf), len(p))
		tmp = d.buf
	} else {
		tmp = p

		var err error
		n, err = d.r.Read(tmp)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return 0, err
			}
			d.eof = true
		}
		tmp = tmp[:n]
	}

	if d.reached || d.eof {
		return 0, io.EOF
	}

	nextStart := n
	if idx := bytes.IndexByte(tmp[:n], d.delim); idx >= 0 {
		d.reached = true

		n = idx
		nextStart = idx + 1
	}

	if useBuf {
		copy(p[:n], tmp[:n])
	}
	d.buf = append(d.buf[:0], tmp[nextStart:]...)

	if n == 0 {
		return 0, io.EOF
	}

	return n, nil
}

func (d *DelimReader) Next() error {
	if d.eof {
		return io.EOF
	}

	d.reached = false

	return nil
}
