package json // Package json provides a unified interface for JSON encoding and decoding operations

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
)

// Decoder represents a streaming JSON decoder built on encoding/json/v2
type Decoder struct {
	dec *jsontext.Decoder
}

// NewDecoder creates a new JSON decoder that wraps the provided io.Reader
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		dec: jsontext.NewDecoder(r),
	}
}

// Decode decodes JSON data into the provided interface
func (d *Decoder) Decode(v any) error {
	return json.UnmarshalDecode(d.dec, v)
}

// Encoder represents a streaming JSON encoder built on encoding/json/v2
type Encoder struct {
	enc *jsontext.Encoder
}

// NewEncoder creates a new JSON encoder that wraps the provided io.Writer
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		enc: jsontext.NewEncoder(w),
	}
}

// Encode encodes the provided interface into JSON format
// jsontext terminates every top-level value with a newline, which is what
// streaming protocols that expect line-delimited JSON need
func (e *Encoder) Encode(v any) error {
	return json.MarshalEncode(e.enc, v)
}
