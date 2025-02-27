// Package protocol contains the JSON types used for communication
// between processes implementing the cache interface.
package protocol

import "io"

// Cmd is a command that can be issued to a process.
//
// If the interface needs to grow, we can add new commands
// or new versioned commands like "get2".
type Cmd string

const (
	CmdGet   Cmd = "get"   // Get retrieves data from the cache
	CmdPut   Cmd = "put"   // Put stores data in the cache
	CmdClose Cmd = "close" // Close terminates the connection
)

// Request is the JSON-encoded message that's sent to the child process
// over stdin. Each JSON object is on its own line. A Request with
// BodySize > 0 will be followed by the body data.
type Request struct {
	// ID is a unique number per process across all requests.
	// It must be echoed in the Response.
	ID int64

	// Command is the type of request.
	// Only commands that were declared as supported will be processed.
	Command Cmd

	// ActionID is used for identifying specific cache operations.
	ActionID []byte `json:",omitempty"`

	// OutputID specifies the expected format or version of the output.
	OutputID []byte `json:",omitempty"`

	// ObjectID is the target object identifier.
	ObjectID []byte `json:",omitempty"`

	// BodySize is the number of bytes of Body. If zero, the body isn't written.
	BodySize int64 `json:",omitempty"`

	// Body is the request payload for operations like "put".
	// It's sent separately from the JSON object so large values
	// can be streamed efficiently.
	Body io.Reader `json:"-"`
}

// Response is the JSON response from the process.
//
// With the exception of the first protocol message with ID==0
// and KnownCommands populated, these are only sent in response
// to a Request.
//
// Responses can be sent in any order. The ID must match
// the request they're replying to.
type Response struct {
	// ID corresponds to the Request ID they're replying to
	ID int64

	// Err contains the error message if the operation failed
	Err string `json:",omitempty"`

	// KnownCommands is included in the first message on startup (with ID==0).
	// It lists the Request.Command types that are supported.
	// This enables graceful protocol extension over time.
	KnownCommands []Cmd `json:",omitempty"`

	// Miss indicates a cache miss when true
	Miss bool `json:",omitempty"`

	// OutputID identifies the format/version of the response
	OutputID []byte `json:",omitempty"`

	// Size is the total size of the response data in bytes
	Size int64 `json:",omitempty"`

	// TimeNanos is the operation processing time in nanoseconds
	TimeNanos int64 `json:",omitempty"`

	// DiskPath is the absolute path on disk where the data is stored
	DiskPath string `json:",omitempty"`
}
