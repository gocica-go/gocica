package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	myio "github.com/mazrean/gocica/internal/pkg/io"
)

func TestProcess_knownCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		options  []ProcessOption
		expected []Cmd
	}{
		{
			name:     "no handlers",
			options:  []ProcessOption{},
			expected: []Cmd{CmdClose},
		},
		{
			name: "get handler only",
			options: []ProcessOption{
				WithGetHandler(func(context.Context, *Request, *Response) error {
					return nil
				}),
			},
			expected: []Cmd{CmdGet, CmdClose},
		},
		{
			name: "push handler only",
			options: []ProcessOption{
				WithPutHandler(func(context.Context, *Request, *Response) error {
					return nil
				}),
			},
			expected: []Cmd{CmdPut, CmdClose},
		},
		{
			name: "close handler only",
			options: []ProcessOption{
				WithCloseHandler(func(context.Context) error {
					return nil
				}),
			},
			expected: []Cmd{CmdClose},
		},
		{
			name: "all handlers",
			options: []ProcessOption{
				WithGetHandler(func(context.Context, *Request, *Response) error {
					return nil
				}),
				WithPutHandler(func(context.Context, *Request, *Response) error {
					return nil
				}),
				WithCloseHandler(func(context.Context) error {
					return nil
				}),
			},
			expected: []Cmd{CmdGet, CmdPut, CmdClose},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProcess(tt.options...)
			commands := p.knownCommands()

			if len(commands) != len(tt.expected) {
				t.Errorf("knownCommands() length mismatch: got %d, want %d", len(commands), len(tt.expected))
				return
			}

			expectMap := make(map[Cmd]struct{}, len(tt.expected))
			for _, cmd := range tt.expected {
				expectMap[cmd] = struct{}{}
			}

			for _, cmd := range commands {
				if _, ok := expectMap[cmd]; !ok {
					t.Errorf("unknown command: %s", cmd)
				}
			}
		})
	}
}

func TestProcess_handle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		options    []ProcessOption
		req        *Request
		wantErr    bool
		wantErrStr string
		wantCalled string
	}{
		{
			name:       "unsupported get command",
			options:    []ProcessOption{},
			req:        &Request{ID: 1, Command: CmdGet},
			wantErr:    true,
			wantErrStr: "get command not supported",
		},
		{
			name:       "unknown command",
			options:    []ProcessOption{},
			req:        &Request{ID: 1, Command: "unknown"},
			wantErr:    true,
			wantErrStr: "unknown command",
		},
		{
			name: "successful get handler",
			options: []ProcessOption{
				WithGetHandler(func(context.Context, *Request, *Response) error {
					return nil
				}),
			},
			req:        &Request{ID: 1, Command: CmdGet},
			wantCalled: "get",
		},
		{
			name: "successful put handler",
			options: []ProcessOption{
				WithPutHandler(func(context.Context, *Request, *Response) error {
					return nil
				}),
			},
			req:        &Request{ID: 1, Command: CmdPut},
			wantCalled: "put",
		},
		{
			name: "successful close handler",
			options: []ProcessOption{
				WithCloseHandler(func(context.Context) error {
					return nil
				}),
			},
			req:        &Request{ID: 1, Command: CmdClose},
			wantCalled: "close",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := ""
			options := make([]ProcessOption, 0, len(tt.options))
			options = append(options, tt.options...)

			switch tt.wantCalled {
			case "get":
				options = append(options, WithGetHandler(func(context.Context, *Request, *Response) error {
					called = "get"
					return nil
				}))
			case "put":
				options = append(options, WithPutHandler(func(context.Context, *Request, *Response) error {
					called = "put"
					return nil
				}))
			case "close":
				options = append(options, WithCloseHandler(func(context.Context) error {
					called = "close"
					return nil
				}))
			}

			p := NewProcess(options...)
			res := &Response{ID: tt.req.ID}
			err := p.handle(t.Context(), tt.req, res)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tt.wantCalled != "" && called != tt.wantCalled {
				t.Errorf("handler call mismatch: got %s, want %s", called, tt.wantCalled)
			}
		})
	}
}

func TestProcess_close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		options  []ProcessOption
		wantErr  bool
		wantCall bool
	}{
		{
			name:     "no close handler",
			options:  []ProcessOption{},
			wantErr:  false,
			wantCall: false,
		},
		{
			name: "with close handler",
			options: []ProcessOption{
				WithCloseHandler(func(context.Context) error {
					return nil
				}),
			},
			wantErr:  false,
			wantCall: true,
		},
		{
			name: "with error in close handler",
			options: []ProcessOption{
				WithCloseHandler(func(context.Context) error {
					return fmt.Errorf("close error")
				}),
			},
			wantErr:  true,
			wantCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			if tt.wantCall {
				tt.options = []ProcessOption{
					WithCloseHandler(func(context.Context) error {
						called = true
						if tt.wantErr {
							return fmt.Errorf("close error")
						}
						return nil
					}),
				}
			}

			p := NewProcess(tt.options...)
			err := p.close(t.Context())

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tt.wantCall && !called {
				t.Error("close handler was not called")
			}
		})
	}
}

type testHandler struct {
	requestsLocker sync.Mutex
	requests       []*Request
	isError        bool
}

func (h *testHandler) handle(ctx context.Context, req *Request) error {
	if h.isError {
		return errors.New("handler error")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	h.requestsLocker.Lock()
	defer h.requestsLocker.Unlock()

	h.requests = append(h.requests, req)
	return nil
}

func TestProcess_decodeWorker(t *testing.T) {
	t.Parallel()

	// base64 encoded string of "gocica"
	const (
		gocicaBase64    = `"Z29jaWNh"`
		oneLineGetReq   = `{"id": 1,"command": "get","actionId": "000a7673899170f3adcac947cabf348c041d32330bb3f6ac6f551128c0c7efa2","outputId": "04464d0c070ce0c1954c4d7846890a40597b70c10f9e7c542c30e6a2659abce4"}` + "\n\n"
		oneLinePutReq   = `{"id": 2,"command": "put","actionId": "000a7673899170f3adcac947cabf348c041d32330bb3f6ac6f551128c0c7efa2","outputId": "0464d0c070ce0c1954c4d7846890a40597b70c10f9e7c542c30e6a2659abce42","bodySize": 6}` + "\n\n" + gocicaBase64 + "\n"
		oneLineCloseReq = `{"id": 3,"command": "close"}` + "\n\n"
	)
	var (
		getReqValue = &Request{
			ID:       1,
			Command:  CmdGet,
			ActionID: "000a7673899170f3adcac947cabf348c041d32330bb3f6ac6f551128c0c7efa2",
			OutputID: "04464d0c070ce0c1954c4d7846890a40597b70c10f9e7c542c30e6a2659abce4",
		}
		putBody     = myio.NewClonableReadSeeker([]byte("gocica"))
		putReqValue = &Request{
			ID:       2,
			Command:  CmdPut,
			ActionID: "000a7673899170f3adcac947cabf348c041d32330bb3f6ac6f551128c0c7efa2",
			OutputID: "0464d0c070ce0c1954c4d7846890a40597b70c10f9e7c542c30e6a2659abce42",
			BodySize: 6,
			Body:     putBody,
		}
		closeReqValue = &Request{
			ID:      3,
			Command: CmdClose,
		}
	)

	tests := []struct {
		name           string
		input          string
		expectRequests []*Request
		wantErr        bool
		handleErr      bool
		ctxCancel      bool
	}{
		{
			name:           "get request with object id in one line",
			input:          oneLineGetReq,
			expectRequests: []*Request{getReqValue},
		},
		{
			name:           "put request with body",
			input:          oneLinePutReq,
			expectRequests: []*Request{putReqValue},
		},
		{
			name:           "close request",
			input:          oneLineCloseReq,
			expectRequests: []*Request{closeReqValue},
		},
		{
			name:           "multiple requests",
			input:          oneLineGetReq + oneLinePutReq,
			expectRequests: []*Request{getReqValue, putReqValue},
		},
		{
			name:           "get request after put request",
			input:          oneLinePutReq + oneLineGetReq,
			expectRequests: []*Request{putReqValue, getReqValue},
		},
		{
			name:    "invalid json",
			input:   `{"id":1,command":"get"}`,
			wantErr: true,
		},
		{
			name:      "handler error",
			input:     oneLineGetReq,
			wantErr:   true,
			handleErr: true,
		},
		{
			name:      "context canceled",
			input:     oneLineGetReq,
			wantErr:   true,
			ctxCancel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			r := bytes.NewBufferString(tt.input)
			p := NewProcess()

			if tt.ctxCancel {
				cancel()
			}

			handler := &testHandler{isError: tt.handleErr}
			err := p.decodeWorker(ctx, r, handler.handle)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
					return
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(handler.requests) != len(tt.expectRequests) {
				t.Errorf("request count mismatch: got %d, want %d", len(handler.requests), len(tt.expectRequests))
				return
			}

			expectRequestMap := make(map[int64]*Request, len(tt.expectRequests))
			for _, req := range tt.expectRequests {
				expectRequestMap[req.ID] = req
			}

			for _, req := range handler.requests {
				expectReq, ok := expectRequestMap[req.ID]
				if !ok {
					t.Errorf("unexpected request: %+v", req)
					continue
				}

				if diff := cmp.Diff(expectReq, req, cmpopts.IgnoreFields(Request{}, "Body")); diff != "" {
					t.Errorf("request mismatch (-want +got):\n%s", diff)
				}

				if req.BodySize > 0 {
					defer func() {
						if _, err := putBody.Seek(0, io.SeekStart); err != nil {
							t.Fatalf("failed to seek request body: %v", err)
						}
					}()
					expectBody, err := io.ReadAll(expectReq.Body)
					if err != nil {
						t.Fatalf("failed to read request body: %v", err)
					}

					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatalf("failed to read request body: %v", err)
					}

					if diff := cmp.Diff(expectBody, body); diff != "" {
						t.Errorf("request body mismatch (-want +got):\n%s", diff)
					}
				} else if req.Body != nil {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatalf("failed to read request body: %v", err)
					}

					if len(body) > 0 {
						t.Errorf("unexpected request body: %s", body)
					}
				}
			}
		})
	}
}

func TestProcess_encodeWorker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		resp    *Response
		want    *Response
		wantErr bool
	}{
		{
			name: "successful encode",
			resp: &Response{
				ID:            1,
				KnownCommands: []Cmd{CmdGet},
			},
			want: &Response{
				ID:            1,
				KnownCommands: []Cmd{CmdGet},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewProcess()
			ch := make(chan *Response, 1)
			ch <- tt.resp
			close(ch)

			err := p.encodeWorker(&buf, ch)
			if (err != nil) != tt.wantErr {
				t.Errorf("encodeWorker() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			var got Response
			if err := json.NewDecoder(&buf).Decode(&got); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}
			if diff := cmp.Diff(tt.want, &got); diff != "" {
				t.Errorf("encodeWorker() response mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
