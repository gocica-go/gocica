package io

import (
	"bytes"
	"errors"
	"io"
)

type DelimReader struct {
	r         io.Reader
	delim     byte
	buf       []byte
	reached   bool
	firstRead bool
	eof       bool
}

func NewDelimReader(r io.Reader, delim byte) *DelimReader {
	return &DelimReader{r: r, delim: delim, buf: make([]byte, 0, 1024), firstRead: true}
}

func (d *DelimReader) Read(p []byte) (int, error) {
	if d.reached || (d.eof && len(d.buf) == 0) {
		return 0, io.EOF
	}

	var (
		n         int
		tmp       []byte
		firstLoop = true
	)
	for firstLoop || (n == 0 && d.firstRead) {
		firstLoop = false

		d.reached = false
		if d.eof && len(d.buf) == 0 {
			return 0, io.EOF
		}

		useBuf := len(d.buf) > 0
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
	}
	d.firstRead = false

	if n == 0 {
		return 0, io.EOF
	}

	return n, nil
}

func (d *DelimReader) Next() error {
	if d.eof && len(d.buf) == 0 {
		return io.EOF
	}

	d.reached = false
	d.firstRead = true

	return nil
}
