//go:build !amd64 && !arm64

// This file is used when building for non-AMD64 architectures, utilizing the go-json library for JSON operations

package json // Package json provides a unified interface for JSON encoding and decoding operations

import (
	"io"

	"github.com/goccy/go-json"
)

const Library = "github.com/goccy/go-json"

// Decoder represents a JSON decoder that uses go-json library for non-AMD64 architectures
type Decoder struct {
	dec *json.Decoder
}

// NewDecoder creates a new JSON decoder that wraps the provided io.Reader
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		dec: json.NewDecoder(r),
	}
}

// Decode decodes JSON data into the provided interface
func (d *Decoder) Decode(v interface{}) error {
	return d.dec.Decode(v)
}

func (d *Decoder) Buffered() io.Reader {
	return d.dec.Buffered()
}

// Encoder represents a JSON encoder that uses go-json library for non-AMD64 architectures
type Encoder struct {
	writer io.Writer
	enc    *json.Encoder
}

// NewEncoder creates a new JSON encoder that wraps the provided io.Writer
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		writer: w,
		enc:    json.NewEncoder(w),
	}
}

// Encode encodes the provided interface into JSON format
// Note: go-json encoder automatically adds a newline after each encoding
func (e *Encoder) Encode(v interface{}) error {
	return e.enc.Encode(v)
}
