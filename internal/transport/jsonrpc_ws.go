// Package transport carries a resolved request over the wire. This file
// implements the jsonrpc-ws transport: JSON-RPC 2.0 over WebSocket.
// Auth is connection-scoped: one socket is opened, login is sent (id=1) and
// awaited, then the method is sent (id=2) on the same connection.
package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// JSONRPCWSRequest is a fully-resolved JSON-RPC 2.0 over WebSocket call.
// All template expansion has already happened; no templates remain.
type JSONRPCWSRequest struct {
	Ctx         context.Context
	URL         string // wss://... endpoint
	TLSInsecure bool
	Timeout     time.Duration // per-message deadline
	// Auth
	AuthMethod string   // ws-login jsonrpc method (e.g. "auth.login_with_api_key")
	AuthParams []string // resolved auth params (already expanded, NOT templates)
	NoAuth     bool
	// Command
	Method  string // jsonrpc method name
	Params  []byte // resolved params as raw JSON (nil = [])
	Verbose io.Writer
}

// rpcMessage is a JSON-RPC 2.0 request frame.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is a JSON-RPC 2.0 response frame (result or error).
type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// RPCError is a JSON-RPC error response (exit code 4, same as HTTPError).
type RPCError struct {
	Code    int
	Message string
	Method  string
}

func (e *RPCError) Error() string {
	if e.Method != "" {
		return fmt.Sprintf("JSON-RPC error %d on %s: %s", e.Code, e.Method, e.Message)
	}
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// DoJSONRPCWS opens one WebSocket, optionally authenticates (connection-scoped),
// sends the method, reads the result, and returns the result bytes.
func DoJSONRPCWS(r JSONRPCWSRequest) ([]byte, error) {
	ctx := r.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// Build a custom HTTP client for the WebSocket handshake with optional TLS skip.
	dialOpts := &websocket.DialOptions{}
	if r.TLSInsecure {
		dialOpts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in per manifest tls_insecure
			},
		}
	}

	if r.Verbose != nil {
		_, _ = fmt.Fprintf(r.Verbose, "> WS CONNECT %s\n", r.URL)
	}

	conn, _, err := websocket.Dial(ctx, r.URL, dialOpts)
	if err != nil {
		return nil, &NetworkError{fmt.Errorf("websocket dial %s: %w", r.URL, err)}
	}
	conn.SetReadLimit(16 << 20) // allow up to 16 MiB per frame (TrueNAS dataset responses can be large)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Derive a wall-clock deadline for the full read loop.
	// Use the context's deadline if present; otherwise fall back to now+timeout.
	loopDeadline, ok := ctx.Deadline()
	if !ok {
		loopDeadline = time.Now().Add(timeout)
	}

	// Step 1: authenticate (unless NoAuth).
	if !r.NoAuth {
		authParams, err := json.Marshal(r.AuthParams)
		if err != nil {
			return nil, fmt.Errorf("marshal auth params: %w", err)
		}

		authMsg := rpcMessage{
			JSONRPC: "2.0",
			ID:      1,
			Method:  r.AuthMethod,
			Params:  authParams,
		}
		if r.Verbose != nil {
			_, _ = fmt.Fprintf(r.Verbose, "> WS SEND id=1 method=%s params=[\"<redacted>\"]\n", r.AuthMethod)
		}
		if err := writeMessage(ctx, conn, authMsg, timeout); err != nil {
			return nil, &NetworkError{fmt.Errorf("send auth: %w", err)}
		}

		resp, err := readResponse(ctx, conn, 1, timeout, loopDeadline, r.Verbose)
		if err != nil {
			return nil, err
		}

		// result must be true (bool).
		var ok bool
		if err := json.Unmarshal(resp.Result, &ok); err != nil || !ok {
			return nil, &AuthError{fmt.Errorf("ws-login failed: server returned result=%s", string(resp.Result))}
		}
	}

	// Step 2: send the method.
	params := r.Params
	if len(params) == 0 {
		params = []byte("[]")
	}

	cmdMsg := rpcMessage{
		JSONRPC: "2.0",
		ID:      2,
		Method:  r.Method,
		Params:  params,
	}
	if r.Verbose != nil {
		_, _ = fmt.Fprintf(r.Verbose, "> WS SEND id=2 method=%s params=%s\n", r.Method, string(params))
	}
	if err := writeMessage(ctx, conn, cmdMsg, timeout); err != nil {
		return nil, &NetworkError{fmt.Errorf("send method %s: %w", r.Method, err)}
	}

	resp, err := readResponse(ctx, conn, 2, timeout, loopDeadline, r.Verbose)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		msg := resp.Error.Message
		// Extract .data.reason if present (mirrors bash: .error.data.reason // .error.message).
		if len(resp.Error.Data) > 0 {
			var d struct {
				Reason string `json:"reason"`
			}
			if json.Unmarshal(resp.Error.Data, &d) == nil && d.Reason != "" {
				msg = d.Reason
			}
		}
		return nil, &RPCError{Code: resp.Error.Code, Message: msg, Method: r.Method}
	}

	if r.Verbose != nil {
		_, _ = fmt.Fprintf(r.Verbose, "< WS RECV id=2 result (%d bytes)\n", len(resp.Result))
	}

	return resp.Result, nil
}

// writeMessage serialises msg and sends it as a text WebSocket frame.
func writeMessage(ctx context.Context, conn *websocket.Conn, msg rpcMessage, timeout time.Duration) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal rpc message: %w", err)
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// readResponse reads frames from the WebSocket until it sees a response with
// the expected id. Other frames (e.g. server-push notifications) are skipped.
// deadline bounds the entire loop so we cannot spin indefinitely waiting for
// an id that never arrives.
func readResponse(ctx context.Context, conn *websocket.Conn, id int, timeout time.Duration, deadline time.Time, verbose io.Writer) (*rpcResponse, error) {
	for {
		if time.Now().After(deadline) {
			return nil, &NetworkError{fmt.Errorf("read response: deadline exceeded waiting for id=%d", id)}
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		_, frame, err := conn.Read(rctx)
		cancel()
		if err != nil {
			return nil, &NetworkError{fmt.Errorf("read ws frame: %w", err)}
		}

		var resp rpcResponse
		if err := json.Unmarshal(frame, &resp); err != nil {
			// Not a parseable JSON-RPC response — skip (could be a notification).
			continue
		}
		if resp.ID != id {
			// Wrong id — not our response; keep reading.
			continue
		}
		if verbose != nil {
			_, _ = fmt.Fprintf(verbose, "< WS RECV id=%d (%d bytes)\n", id, len(frame))
		}
		return &resp, nil
	}
}
