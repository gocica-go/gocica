package json // Package json provides a unified interface for JSON encoding and decoding operations

import (
	"io"

	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
)

// Decoder represents a JSON decoder that utilizes the high-performance Sonic decoder for AMD64 architecture
type Decoder struct {
	reader io.Reader
	dec    *decoder.StreamDecoder
}

// NewDecoder creates a new JSON decoder that wraps the provided io.Reader
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		reader: r,
		dec:    decoder.NewStreamDecoder(r),
	}
}

// Decode decodes JSON data into the provided interface
func (d *Decoder) Decode(v interface{}) error {
	return d.dec.Decode(v)
}

func (d *Decoder) Buffered() io.Reader {
	return d.dec.Buffered()
}

// Encoder represents a JSON encoder that utilizes the high-performance Sonic encoder for AMD64 architecture
type Encoder struct {
	writer io.Writer
	enc    *encoder.StreamEncoder
}

// NewEncoder creates a new JSON encoder that wraps the provided io.Writer
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		writer: w,
		enc:    encoder.NewStreamEncoder(w),
	}
}

// Encode encodes the provided interface into JSON format
// It automatically appends a newline after each encoding for better readability
// and compatibility with streaming protocols that expect line-delimited JSON
func (e *Encoder) Encode(v interface{}) error {
	if err := e.enc.Encode(v); err != nil {
		return err
	}

	// Add a newline after each encoding for better readability
	_, err := e.writer.Write([]byte{'\n'})
	return err
}
