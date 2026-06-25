package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// wsTestServer starts an httptest server that upgrades to WebSocket and runs
// handler for each connection. Returns the server and a wss:// URL (test
// servers use ws://, which is fine — TLSInsecure is irrelevant for plain WS).
func wsTestServer(t *testing.T, handler func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("ws accept: %v", err)
			return
		}
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wsURL converts an httptest http:// URL to ws://.
func wsURL(srv *httptest.Server) string {
	return strings.Replace(srv.URL, "http://", "ws://", 1)
}

// readRPC reads one JSON-RPC message from conn.
func readRPC(t *testing.T, conn *websocket.Conn) rpcMessage {
	t.Helper()
	var msg rpcMessage
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read rpc: %v", err)
	}
	return msg
}

// sendRPC sends a JSON-RPC response to conn.
func sendRPC(t *testing.T, conn *websocket.Conn, resp rpcResponse) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, resp); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
}

func TestDoJSONRPCWSSuccess(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Expect auth (id=1).
		auth := readRPC(t, conn)
		if auth.ID != 1 {
			t.Errorf("want auth id=1, got %d", auth.ID)
		}
		if auth.Method != "auth.login_with_api_key" {
			t.Errorf("want auth method auth.login_with_api_key, got %q", auth.Method)
		}
		// Verify the auth params contain the key.
		var params []string
		if err := json.Unmarshal(auth.Params, &params); err != nil || len(params) == 0 || params[0] != "myapikey" {
			t.Errorf("auth params = %s; want [\"myapikey\"]", auth.Params)
		}

		// Respond result=true.
		sendRPC(t, conn, rpcResponse{ID: 1, Result: json.RawMessage(`true`)})

		// Expect method call (id=2).
		call := readRPC(t, conn)
		if call.ID != 2 {
			t.Errorf("want call id=2, got %d", call.ID)
		}
		if call.Method != "system.info" {
			t.Errorf("want method system.info, got %q", call.Method)
		}

		// Respond with a result.
		sendRPC(t, conn, rpcResponse{ID: 2, Result: json.RawMessage(`{"hostname":"truenas"}`)})
	})

	result, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:        context.Background(),
		URL:        wsURL(srv),
		Timeout:    5 * time.Second,
		AuthMethod: "auth.login_with_api_key",
		AuthParams: []string{"myapikey"},
		Method:     "system.info",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["hostname"] != "truenas" {
		t.Errorf("hostname = %v; want truenas", got["hostname"])
	}
}

func TestDoJSONRPCWSNoAuth(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Must receive the method directly (no auth frame first).
		call := readRPC(t, conn)
		if call.ID != 2 {
			t.Errorf("want call id=2, got %d", call.ID)
		}
		if call.Method != "core.ping" {
			t.Errorf("want method core.ping, got %q", call.Method)
		}
		sendRPC(t, conn, rpcResponse{ID: 2, Result: json.RawMessage(`"pong"`)})
	})

	result, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:     context.Background(),
		URL:     wsURL(srv),
		Timeout: 5 * time.Second,
		NoAuth:  true,
		Method:  "core.ping",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != `"pong"` {
		t.Errorf("result = %s; want \"pong\"", result)
	}
}

func TestDoJSONRPCWSAuthFail(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Read auth, respond false.
		readRPC(t, conn)
		sendRPC(t, conn, rpcResponse{ID: 1, Result: json.RawMessage(`false`)})
	})

	_, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:        context.Background(),
		URL:        wsURL(srv),
		Timeout:    5 * time.Second,
		AuthMethod: "auth.login_with_api_key",
		AuthParams: []string{"badkey"},
		Method:     "system.info",
	})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("want *AuthError, got %T: %v", err, err)
	}
}

func TestDoJSONRPCWSRPCError(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Auth succeeds.
		readRPC(t, conn)
		sendRPC(t, conn, rpcResponse{ID: 1, Result: json.RawMessage(`true`)})

		// Method returns an error.
		readRPC(t, conn)
		sendRPC(t, conn, rpcResponse{
			ID:    2,
			Error: &rpcError{Code: -32601, Message: "Method not found"},
		})
	})

	_, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:        context.Background(),
		URL:        wsURL(srv),
		Timeout:    5 * time.Second,
		AuthMethod: "auth.login_with_api_key",
		AuthParams: []string{"mykey"},
		Method:     "nonexistent.method",
	})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != -32601 {
		t.Errorf("code = %d; want -32601", rpcErr.Code)
	}
}

func TestDoJSONRPCWSRPCErrorWithDataReason(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Auth succeeds.
		readRPC(t, conn)
		sendRPC(t, conn, rpcResponse{ID: 1, Result: json.RawMessage(`true`)})

		// Method returns an error with data.reason.
		readRPC(t, conn)
		sendRPC(t, conn, rpcResponse{
			ID: 2,
			Error: &rpcError{
				Code:    -32000,
				Message: "Application error",
				Data:    json.RawMessage(`{"reason":"pool not found"}`),
			},
		})
	})

	_, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:        context.Background(),
		URL:        wsURL(srv),
		Timeout:    5 * time.Second,
		AuthMethod: "auth.login_with_api_key",
		AuthParams: []string{"mykey"},
		Method:     "pool.query",
	})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Message != "pool not found" {
		t.Errorf("message = %q; want \"pool not found\" (from data.reason)", rpcErr.Message)
	}
}

func TestDoJSONRPCWSNetworkError(t *testing.T) {
	_, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:     context.Background(),
		URL:     "ws://127.0.0.1:1", // unreachable
		Timeout: time.Second,
		NoAuth:  true,
		Method:  "core.ping",
	})
	var ne *NetworkError
	if !errors.As(err, &ne) {
		t.Fatalf("want *NetworkError, got %T: %v", err, err)
	}
}

// TestReadResponseWrongIDThenClose verifies Fix 3: when the server only sends
// frames with the wrong ID and then closes, readResponse must return a
// NetworkError rather than looping forever (deadline terminates the loop).
func TestReadResponseWrongIDThenClose(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
		// Send a notification with a mismatched id (not 2), then close.
		sendRPC(t, conn, rpcResponse{ID: 99, Result: json.RawMessage(`"notification"`)})
		// Server closes — client must get a NetworkError, not hang.
	})

	_, err := DoJSONRPCWS(JSONRPCWSRequest{
		Ctx:     context.Background(),
		URL:     wsURL(srv),
		Timeout: 2 * time.Second,
		NoAuth:  true,
		Method:  "core.ping",
	})
	var ne *NetworkError
	if !errors.As(err, &ne) {
		t.Fatalf("want *NetworkError, got %T: %v", err, err)
	}
}
