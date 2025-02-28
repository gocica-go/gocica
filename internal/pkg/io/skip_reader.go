package io

import "io"

// SkipCharReader は、指定された文字を読み飛ばしながら underlying io.Reader から読み込むラッパーです。
// underlying reader から読み込んだデータから、skip で指定された文字を除外したデータのみを返します。
type SkipCharReader struct {
	r    io.Reader
	skip byte
	buf  []byte // フィルタ後のデータを保持する内部バッファ
	err  error  // underlying reader の最終エラー（EOF も含む）
}

// NewSkipCharReader は、与えられた io.Reader とスキップする文字から SkipCharReader を作成します。
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
