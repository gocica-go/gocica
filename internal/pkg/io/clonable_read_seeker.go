package io

import (
	"bytes"
	"io"
)

type ClonableReadSeeker interface {
	io.ReadSeeker
	Clone() ClonableReadSeeker
}

type clonableReadSeeker struct {
	br  *bytes.Reader
	buf []byte
}

func NewClonableReadSeeker(buf []byte) ClonableReadSeeker {
	return &clonableReadSeeker{
		br:  bytes.NewReader(buf),
		buf: buf,
	}
}

func (c *clonableReadSeeker) Read(p []byte) (n int, err error) {
	return c.br.Read(p)
}

func (c *clonableReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.br.Seek(offset, whence)
}

func (c *clonableReadSeeker) Clone() ClonableReadSeeker {
	return &clonableReadSeeker{
		br:  bytes.NewReader(c.buf),
		buf: c.buf,
	}
}
