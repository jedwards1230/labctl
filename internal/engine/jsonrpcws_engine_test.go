package engine

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
	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// wsFrame is a JSON-RPC frame as seen by the test server — enough to inspect the
// method and the raw params the engine sent.
type wsFrame struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// engineWSServer starts an httptest WebSocket server that runs handler per
// connection and returns a ws:// URL. Mirrors the transport package's helper but
// is self-contained so the engine package can drive the real ws call path.
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

// TestExecuteJSONRPCWSSuccess drives the engine's jsonrpc-ws path on the real
// (non-dry-run) call: it proves auth-param template expansion ({secret.api_key}
// → resolved value) and command-param expansion ({var}) reach the ws frames,
// and that a result body comes back through Execute.
func TestExecuteJSONRPCWSSuccess(t *testing.T) {
	url := engineWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		// Frame 1: auth login. The {secret.api_key} template must have resolved
		// to the fakeOp value "test-key".
		auth := wsRead(t, conn)
		if auth.ID != 1 {
			t.Errorf("auth id = %d, want 1", auth.ID)
		}
		if auth.Method != "auth.login_with_api_key" {
			t.Errorf("auth method = %q", auth.Method)
		}
		var ap []string
		if err := json.Unmarshal(auth.Params, &ap); err != nil || len(ap) != 1 || ap[0] != "test-key" {
			t.Errorf("auth params = %s, want [\"test-key\"]", auth.Params)
		}
		wsWrite(t, conn, map[string]any{"id": 1, "result": true})

		// Frame 2: the command. The {poolname} var must have expanded to "tank".
		call := wsRead(t, conn)
		if call.ID != 2 {
			t.Errorf("call id = %d, want 2", call.ID)
		}
		if call.Method != "pool.query" {
			t.Errorf("call method = %q, want pool.query", call.Method)
		}
		var cp []string
		if err := json.Unmarshal(call.Params, &cp); err != nil || len(cp) != 1 || cp[0] != "tank" {
			t.Errorf("call params = %s, want [\"tank\"]", call.Params)
		}
		wsWrite(t, conn, map[string]any{"id": 2, "result": map[string]any{"name": "tank"}})
	})

	svc := &manifest.Service{
		Name:      "truenas",
		BaseURL:   url,
		Transport: "jsonrpc-ws",
		EnvPrefix: "TRUENAS",
		Timeout:   "5s",
		Vars:      map[string]string{"poolname": "tank"},
		Auth: manifest.Auth{
			Strategy: "ws-login",
			Method:   "auth.login_with_api_key",
			Params:   []string{"{secret.api_key}"},
		},
		Secrets: map[string]manifest.Secret{"api_key": {Ref: "op://vault/truenas/api_key"}},
		Commands: map[string]manifest.Command{
			"pool-query": {Method: "pool.query", Params: `["{poolname}"]`},
		},
	}
	cmds := command.FromManifest(svc)
	res, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["pool-query"],
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatalf("execute jsonrpc-ws: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatalf("parse result: %v (%s)", err, res.Body)
	}
	if got["name"] != "tank" {
		t.Fatalf("result name = %v, want tank", got["name"])
	}
}

// TestExecuteJSONRPCWSNoAuthHonored proves that a command marked noauth skips the
// auth frame entirely: the server must receive the method directly as id=2 with
// no preceding login, even though the service declares a ws-login strategy.
func TestExecuteJSONRPCWSNoAuthHonored(t *testing.T) {
	authSeen := false
	url := engineWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		f := wsRead(t, conn)
		if f.ID == 1 {
			authSeen = true
			t.Errorf("received an auth frame despite noauth")
		}
		if f.Method != "core.ping" {
			t.Errorf("method = %q, want core.ping (sent directly, no auth)", f.Method)
		}
		wsWrite(t, conn, map[string]any{"id": f.ID, "result": "pong"})
	})

	svc := &manifest.Service{
		Name:      "truenas",
		BaseURL:   url,
		Transport: "jsonrpc-ws",
		Timeout:   "5s",
		Auth: manifest.Auth{
			Strategy: "ws-login",
			Method:   "auth.login_with_api_key",
			Params:   []string{"{secret.api_key}"},
		},
		// A secret resolver that fails loudly — noauth must skip it entirely.
		Secrets: map[string]manifest.Secret{"api_key": {Ref: "op://vault/truenas/api_key"}},
		Commands: map[string]manifest.Command{
			"ping": {Method: "core.ping", NoAuth: true},
		},
	}
	cmds := command.FromManifest(svc)
	failOp := func([]string) (string, error) { return "", errBoom }
	res, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["ping"],
		Runner:  failOp, // would error if auth resolution were attempted
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatalf("execute noauth jsonrpc-ws: %v", err)
	}
	if authSeen {
		t.Fatal("auth frame was sent despite noauth")
	}
	if string(res.Body) != `"pong"` {
		t.Fatalf("result = %s, want \"pong\"", res.Body)
	}
}
