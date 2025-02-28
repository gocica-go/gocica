package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/internal/pkg/json"
	"github.com/mazrean/gocica/internal/pkg/log"

	"golang.org/x/sync/errgroup"
)

// Logger defines the interface for logging operations used throughout the protocol
// It provides methods for different log levels: debug, info, and error
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Errorf(format string, args ...any)
}

// Process represents the main protocol handler that manages request/response cycles
// It handles different types of commands (get, put, close) and manages communication
type Process struct {
	getHandler         func(context.Context, *Request, *Response) error
	putHandler         func(context.Context, *Request, *Response) error
	closeHandler       func() error
	logger             Logger
	responseBufferSize int
}

// processOption holds the configuration options for a Process instance
type processOption struct {
	getHandler         func(context.Context, *Request, *Response) error
	putHandler         func(context.Context, *Request, *Response) error
	closeHandler       func() error
	logger             Logger
	responseBufferSize int
}

// ProcessOption defines a function type for configuring Process instances
type ProcessOption func(*processOption)

// WithGetHandler sets the handler for GET commands
// The handler processes GET requests and generates appropriate responses
func WithGetHandler(handler func(context.Context, *Request, *Response) error) ProcessOption {
	return func(o *processOption) {
		o.getHandler = handler
	}
}

// WithPutHandler sets the handler for PUSH commands
// The handler processes PUSH requests and generates appropriate responses
func WithPutHandler(handler func(context.Context, *Request, *Response) error) ProcessOption {
	return func(o *processOption) {
		o.putHandler = handler
	}
}

// WithCloseHandler sets the handler for CLOSE commands
// The handler is wrapped with sync.OnceValue to ensure it's only called once
func WithCloseHandler(handler func() error) ProcessOption {
	return func(o *processOption) {
		o.closeHandler = sync.OnceValue(handler)
	}
}

// WithLogger sets the logger instance for the Process
// If not set, a default logger will be used
func WithLogger(logger Logger) ProcessOption {
	return func(o *processOption) {
		o.logger = logger
	}
}

// WithResponseBufferSize sets the size of the response channel buffer
// The size must be positive, otherwise it will be ignored
func WithResponseBufferSize(size int) ProcessOption {
	return func(o *processOption) {
		if size > 0 {
			o.responseBufferSize = size
		}
	}
}

// NewProcess creates a new Process instance with the given options
// It initializes the process with default values and applies the provided options
func NewProcess(options ...ProcessOption) *Process {
	o := &processOption{
		logger:             log.NewLogger(log.Info),
		responseBufferSize: 100, // デフォルト値
	}
	for _, option := range options {
		option(o)
	}

	return &Process{
		getHandler:         o.getHandler,
		putHandler:         o.putHandler,
		closeHandler:       o.closeHandler,
		logger:             o.logger,
		responseBufferSize: o.responseBufferSize,
	}
}

// Run starts the main processing loop of the Process
// It handles JSON requests from stdin and writes responses to stdout
// The process continues until EOF is received or an error occurs
func (p *Process) Run() error {
	return p.run(os.Stdout, os.Stdin)
}

func (p *Process) run(w io.Writer, r io.Reader) (err error) {
	// Create root context and error groups for concurrent operations
	ctx := context.Background()
	eg, ctx := errgroup.WithContext(ctx)
	// Create buffered channel for responses with configured size
	resCh := make(chan *Response, p.responseBufferSize)
	defer func() {
		// Close response channel to signal encoder goroutine to exit
		close(resCh)

		// Perform cleanup and collect any errors that occur
		deferErr := p.close()
		if deferErr != nil {
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}

		// Wait for encoder goroutine to finish and handle any errors
		deferErr = eg.Wait()
		if deferErr != nil {
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}
	}()

	// Send initial response with supported commands
	resCh <- &Response{
		ID:            0,
		KnownCommands: p.knownCommands(),
	}
	// Start encoder goroutine to handle response writing
	eg.Go(func() error {
		return p.encodeWorker(w, resCh)
	})

	// Start decoder loop to handle request processing
	err = p.decodeWorker(ctx, r, func(ctx context.Context, req *Request) error {
		// Create response with matching ID
		res := Response{}
		err := p.handle(ctx, req, &res)
		if err != nil {
			p.logger.Errorf("handle request(%+v): %v", req, err)
			res.Err = err.Error()
		}
		res.ID = req.ID

		// Send response or handle context cancellation
		select {
		case resCh <- &res:
		case <-ctx.Done():
			return ctx.Err()
		}

		return nil
	})
	if err != nil {
		err = fmt.Errorf("decode worker: %w", err)
		return
	}

	return
}

// knownCommands returns a list of commands supported by this Process instance
// The supported commands are determined by the presence of their respective handlers
func (p *Process) knownCommands() []Cmd {
	commands := make([]Cmd, 0, 3)

	// Always support the close command
	commands = append(commands, CmdClose)

	if p.getHandler != nil {
		commands = append(commands, CmdGet)
	}
	if p.putHandler != nil {
		commands = append(commands, CmdPut)
	}

	return commands
}

// encodeWorker handles the encoding and writing of responses to stdout
// It runs in a separate goroutine and processes responses from the response channel
func (p *Process) encodeWorker(w io.Writer, ch <-chan *Response) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	encoder := json.NewEncoder(bw)

	for resp := range ch {
		err := encoder.Encode(resp)
		if err != nil {
			p.logger.Errorf("encode response(%+v): %v", resp, err)
			continue
		}

		err = bw.Flush()
		if err != nil {
			p.logger.Errorf("flush response(%+v): %v", resp, err)
			continue
		}
	}

	return nil
}

// decodeWorker handles the decoding and processing of requests from stdin
// It reads requests from the provided reader and calls the handler for each request
func (p *Process) decodeWorker(ctx context.Context, r io.Reader, handler func(context.Context, *Request) error) (err error) {
	eg, ctx := errgroup.WithContext(ctx)
	defer func() {
		deferErr := eg.Wait()
		if deferErr != nil {
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}
	}()

	dr := myio.NewDelimReader(bufio.NewReader(r), '\n')
	decoder := json.NewDecoder(dr)

	for {
		err = dr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
				return
			}
			err = fmt.Errorf("next request: %w", err)
			return
		}

		var req Request
		err = decoder.Decode(&req)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
				return
			}

			err = fmt.Errorf("decode request: %w", err)
			return
		}

		err = dr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
				return
			}
			err = fmt.Errorf("next request skip: %w", err)
			return
		}

		_, err = io.Copy(io.Discard, dr)
		if err != nil {
			err = fmt.Errorf("skip request delimiter: %w", err)
			return
		}

		if req.Command == CmdPut && req.BodySize > 0 {
			err = dr.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
					return
				}
				err = fmt.Errorf("next request body: %w", err)
				return
			}

			buf := bytes.NewBuffer(make([]byte, 0, req.BodySize))
			_, err = io.Copy(buf, base64.NewDecoder(base64.StdEncoding, myio.NewSkipCharReader(dr, '"')))
			if err != nil && !errors.Is(err, io.EOF) {
				err = fmt.Errorf("read request body: %w", err)
				return
			}

			if buf.Len() != int(req.BodySize) {
				err = fmt.Errorf("read request body: expected %d bytes, got %d", req.BodySize, buf.Len())
				return
			}

			// Wrap the request body reader with a limited reader to prevent reading more than expected
			req.Body = buf
		}

		eg.Go(func() error {
			return handler(ctx, &req)
		})
	}
}

// handle processes individual requests based on their command type
// It routes requests to the appropriate handler (get, push, or close)
func (p *Process) handle(ctx context.Context, req *Request, res *Response) error {
	switch req.Command {
	case CmdGet:
		if p.getHandler == nil {
			return fmt.Errorf("get command not supported")
		}
		return p.getHandler(ctx, req, res)
	case CmdPut:
		if p.putHandler == nil {
			return fmt.Errorf("put command not supported")
		}
		return p.putHandler(ctx, req, res)
	case CmdClose:
		return p.close()
	default:
		return fmt.Errorf("unknown command: %s", req.Command)
	}
}

// close handles the cleanup when the Process is being shut down
// It calls the closeHandler if one is set
func (p *Process) close() error {
	if p.closeHandler == nil {
		return nil
	}

	return p.closeHandler()
}
