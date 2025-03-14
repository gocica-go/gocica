package io

import "io"

// SkipCharReader is a wrapper that reads from the underlying io.Reader while skipping specified characters.
// It returns only data that excludes the character specified by 'skip' from the data read from the underlying reader.
type SkipCharReader struct {
	r    io.Reader
	skip byte
	buf  []byte // internal buffer that holds filtered data
	err  error  // final error from the underlying reader (including EOF)
}

// NewSkipCharReader creates a new SkipCharReader from the given io.Reader and character to skip.
func NewSkipCharReader(r io.Reader, skip byte) *SkipCharReader {
	return &SkipCharReader{
		r:    r,
		skip: skip,
		buf:  make([]byte, 0),
	}
}

func (scr *SkipCharReader) Read(p []byte) (int, error) {
	for len(scr.buf) == 0 && scr.err == nil {
		tmp := make([]byte, 1024)
		n, err := scr.r.Read(tmp)
		if n > 0 {
			for i := 0; i < n; i++ {
				if tmp[i] == scr.skip {
					continue
				}
				scr.buf = append(scr.buf, tmp[i])
			}
		}
		if err != nil {
			scr.err = err
			break
		}
	}

	if len(scr.buf) == 0 {
		return 0, scr.err
	}

	n := copy(p, scr.buf)
	scr.buf = scr.buf[n:]
	if len(scr.buf) == 0 && scr.err != nil {
		return n, scr.err
	}
	return n, nil
}
