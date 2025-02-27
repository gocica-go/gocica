package protocol

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"gocica/internal/pkg/json"
	"gocica/internal/pkg/log"
	"io"
	"os"
	"sync"

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
// It handles different types of commands (get, push, close) and manages communication
type Process struct {
	getHandler         func(context.Context, *Request, *Response) error
	pushHandler        func(context.Context, *Request, *Response) error
	closeHandler       func() error
	logger             Logger
	responseBufferSize int
}

// processOption holds the configuration options for a Process instance
type processOption struct {
	getHandler         func(context.Context, *Request, *Response) error
	pushHandler        func(context.Context, *Request, *Response) error
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

// WithPushHandler sets the handler for PUSH commands
// The handler processes PUSH requests and generates appropriate responses
func WithPushHandler(handler func(context.Context, *Request, *Response) error) ProcessOption {
	return func(o *processOption) {
		o.pushHandler = handler
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
		logger:             log.NewLogger(),
		responseBufferSize: 100, // デフォルト値
	}
	for _, option := range options {
		option(o)
	}

	return &Process{
		getHandler:         o.getHandler,
		pushHandler:        o.pushHandler,
		closeHandler:       o.closeHandler,
		logger:             o.logger,
		responseBufferSize: o.responseBufferSize,
	}
}

// Run starts the main processing loop of the Process
// It handles JSON requests from stdin and writes responses to stdout
// The process continues until EOF is received or an error occurs
func (p *Process) Run() (err error) {
	// Create root context and error groups for concurrent operations
	ctx := context.Background()
	eg, ctx := errgroup.WithContext(ctx)
	encodeEg := errgroup.Group{}
	// Create buffered channel for responses with configured size
	resCh := make(chan *Response, p.responseBufferSize)
	defer func() {
		// Perform cleanup and collect any errors that occur
		deferErr := p.close()
		if deferErr != nil {
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}

		// Wait for all request handling goroutines to complete
		deferErr = eg.Wait()
		if deferErr != nil {
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}

		// Close response channel to signal encoder goroutine to exit
		close(resCh)

		// Wait for encoder goroutine to finish and handle any errors
		egErr := encodeEg.Wait()
		if egErr != nil {
			if err == nil {
				err = egErr
			} else {
				err = errors.Join(err, egErr)
			}
		}
	}()

	// Start encoder goroutine to handle response writing
	encodeEg.Go(func() error {
		return p.encodeWorker(resCh)
	})
	// Send initial response with supported commands
	resCh <- &Response{
		ID:            0,
		KnownCommands: p.knownCommands(),
	}

	// Create JSON decoder for reading requests from stdin
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	// Main processing loop for handling requests
	for {
		// Decode incoming JSON request
		var req Request
		err := decoder.Decode(&req)
		if err != nil {
			// Exit normally on EOF, otherwise return error
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode request: %w", err)
		}

		// Handle each request in a separate goroutine
		eg.Go(func() error {
			// Create response with matching ID
			res := Response{ID: req.ID}
			err := p.handle(ctx, &req, &res)
			if err != nil {
				res.Err = err.Error()
			}

			// Send response or handle context cancellation
			select {
			case resCh <- &res:
			case <-ctx.Done():
				return ctx.Err()
			}

			return nil
		})
	}
}

// knownCommands returns a list of commands supported by this Process instance
// The supported commands are determined by the presence of their respective handlers
func (p *Process) knownCommands() []Cmd {
	commands := make([]Cmd, 0)
	if p.getHandler != nil {
		commands = append(commands, CmdGet)
	}
	if p.pushHandler != nil {
		commands = append(commands, CmdPut)
	}
	if p.closeHandler != nil {
		commands = append(commands, CmdClose)
	}

	return commands
}

// encodeWorker handles the encoding and writing of responses to stdout
// It runs in a separate goroutine and processes responses from the response channel
func (p *Process) encodeWorker(ch <-chan *Response) error {
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	encoder := json.NewEncoder(bw)

	for resp := range ch {
		err := encoder.Encode(resp)
		if err != nil {
			return fmt.Errorf("encode response(%+v): %w", resp, err)
		}

		err = bw.Flush()
		if err != nil {
			return fmt.Errorf("flush response(%+v): %w", resp, err)
		}
	}

	return nil
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
		if p.pushHandler == nil {
			return fmt.Errorf("put command not supported")
		}
		return p.pushHandler(ctx, req, res)
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
