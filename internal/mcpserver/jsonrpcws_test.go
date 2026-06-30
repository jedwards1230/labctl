package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/mcpserver"
)

// wsFrame is a JSON-RPC frame as seen by the test server — enough to inspect the
// method and the raw params the engine sent. Copied from the engine package's
// jsonrpc-ws test harness (those helpers are unexported and in `package engine`,
// so they can't be imported here).
type wsFrame struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// engineWSServer starts an httptest WebSocket server that runs handler per
// connection and returns a ws:// URL. Copied from the engine package's harness.
func engineWSServer(t *testing.T, handler func(*websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Logf("ws accept: %v", err)
			return
		}
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "http://", "ws://", 1)
}

func wsRead(t *testing.T, conn *websocket.Conn) wsFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var f wsFrame
	if err := wsjson.Read(ctx, conn, &f); err != nil {
		t.Fatalf("ws read: %v", err)
	}
	return f
}

func wsWrite(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, v); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

// TestVerbToolCallDispatchJSONRPC drives the generic `call` verb tool over a real
// WebSocket round-trip. A jsonrpc-ws service with NO Auth block resolves no
// secrets (auth.Params is empty), so executeAndRender's Runner: nil is never
// asked for a credential and the round-trip completes cleanly.
//
// NOTE: omitting Auth does NOT mark the synthesized `call` command NoAuth (only a
// manifest command can set that), so the engine still sends an empty-method auth
// frame (id=1) before the command frame (id=2). With no auth params that frame
// carries no secret; the handler simply answers it with result:true, then asserts
// the command frame carried method+params through, and replies with the result.
func TestVerbToolCallDispatchJSONRPC(t *testing.T) {
	url := engineWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Frame 1: the (empty-method) auth frame the engine sends for a non-NoAuth
		// command. With an empty Auth block it carries no credential.
		auth := wsRead(t, conn)
		if auth.ID != 1 {
			t.Errorf("auth frame id = %d, want 1", auth.ID)
		}
		wsWrite(t, conn, map[string]any{"id": auth.ID, "result": true})

		// Frame 2: the actual jsonrpc call. method+params must have round-tripped.
		call := wsRead(t, conn)
		if call.Method != "system.info" {
			t.Errorf("call method = %q, want system.info", call.Method)
		}
		var cp []string
		if err := json.Unmarshal(call.Params, &cp); err != nil || len(cp) != 1 || cp[0] != "pool" {
			t.Errorf("call params = %s, want [\"pool\"]", call.Params)
		}
		wsWrite(t, conn, map[string]any{"id": call.ID, "result": map[string]any{"ok": true}})
	})

	svc := &manifest.Service{
		Name:      "truenas",
		BaseURL:   url,
		Transport: "jsonrpc-ws",
		Timeout:   "5s",
		// No Auth block: nothing to resolve, so Runner: nil is never invoked.
		Commands: map[string]manifest.Command{
			"info": {Help: "system info", Method: "system.info"},
		},
	}
	loaded := &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{"truenas": svc},
	}

	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "truenas_call",
		Arguments: map[string]any{
			"method": "system.info",
			"params": `["pool"]`,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content in result")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type = %T, want *mcp.TextContent", result.Content[0])
	}
	if !strings.Contains(txt.Text, "ok") {
		t.Errorf("result text = %q, want to contain \"ok\"", txt.Text)
	}
}
