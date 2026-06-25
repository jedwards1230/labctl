package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/output"
)

// newPipelineSvc builds a minimal service pointing at baseURL, with the given
// commands map. Auth is none so tests don't need a secret runner.
func newPipelineSvc(baseURL string, cmds map[string]manifest.Command) *manifest.Service {
	return &manifest.Service{
		Name:      "test",
		BaseURL:   baseURL,
		EnvPrefix: "TEST",
		Auth:      manifest.Auth{Strategy: "none"},
		Commands:  cmds,
	}
}

// newPipelineReq builds a minimal Request with no secret runner.
func newPipelineReq(svc *manifest.Service, cmdID string, flags Flags) Request {
	return Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: mustCmd(svc, cmdID),
		Flags:   flags,
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}
}

func mustCmd(svc *manifest.Service, id string) *command.Command {
	cmds := command.FromManifest(svc)
	c, ok := cmds[id]
	if !ok {
		panic("mustCmd: unknown command " + id)
	}
	return c
}

// parseBody is a test helper that unmarshals the result body into a map.
func parseBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("parse body %q: %v", body, err)
	}
	return m
}

// TestPipelineMultiStepExtract verifies that a var extracted in step 1 is
// available (via accVars) when step 2 uses it in its path template.
func TestPipelineMultiStepExtract(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"token":"abc123"}`))
		case "/resource/abc123":
			_, _ = w.Write([]byte(`{"name":"foo"}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"fetch": {
			Steps: []manifest.Step{
				{
					ID:      "login",
					Method:  "GET",
					Path:    "/login",
					Extract: map[string]string{"token": ".token"},
				},
				{
					ID:      "get",
					Method:  "GET",
					Path:    "/resource/{token}",
					Extract: map[string]string{"name": ".name"},
				},
			},
			Output: manifest.Output{Filter: "{token: .token, name: .name}"},
		},
	})

	req := newPipelineReq(svc, "fetch", Flags{})
	res, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("want 2 HTTP calls, got %d", calls)
	}
	m := parseBody(t, res.Body)
	if m["name"] != "foo" {
		t.Fatalf("expected name=foo in result, got %v", m)
	}
	if m["token"] != "abc123" {
		t.Fatalf("expected token=abc123 in result, got %v", m)
	}
}

// TestPipelineCaptureHeader verifies that a response header is stored in accVars
// and available in the final output filter.
func TestPipelineCaptureHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "abc123")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"req": {
			Steps: []manifest.Step{
				{
					ID:            "call",
					Method:        "GET",
					Path:          "/endpoint",
					CaptureHeader: map[string]string{"requestID": "X-Request-ID"},
				},
			},
			Output: manifest.Output{Filter: "{id: .requestID}"},
		},
	})

	req := newPipelineReq(svc, "req", Flags{})
	res, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := parseBody(t, res.Body)
	if m["id"] != "abc123" {
		t.Fatalf("expected id=abc123, got %v", m)
	}
}

// TestPipelineWhenSkip verifies that a step with a falsy `when` condition is
// skipped entirely (no HTTP call made for that step).
func TestPipelineWhenSkip(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"guarded": {
			Steps: []manifest.Step{
				{
					ID:     "always",
					Method: "GET",
					Path:   "/always",
				},
				{
					ID:     "skipped",
					Method: "GET",
					Path:   "/skipped",
					When:   ".logged_in",
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	// logged_in is not in accVars at all → .logged_in returns null → falsy.
	req := newPipelineReq(svc, "guarded", Flags{})
	_, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("want 1 HTTP call (skipped step should not fire), got %d", callCount)
	}
}

// TestPipelineWhenSkipFalse verifies that a step with when condition evaluating
// to false is skipped.
func TestPipelineWhenSkipFalse(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"logged_in": false}`))
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"guarded": {
			Steps: []manifest.Step{
				{
					ID:      "login_check",
					Method:  "GET",
					Path:    "/check",
					Extract: map[string]string{"logged_in": ".logged_in"},
				},
				{
					ID:     "conditional",
					Method: "GET",
					Path:   "/conditional",
					When:   ".logged_in",
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "guarded", Flags{})
	_, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only the first step (login_check) should fire; conditional is skipped.
	if callCount != 1 {
		t.Fatalf("want 1 HTTP call (when=false skips step), got %d", callCount)
	}
}

// TestPipelineOnErrorContinue verifies that when a step returns an error and
// OnError is set, the pipeline continues without returning an error.
func TestPipelineOnErrorContinue(t *testing.T) {
	var onErrorCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(404)
		case "/fallback":
			onErrorCalled = true
			_, _ = w.Write([]byte(`{}`))
		case "/final":
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	fallback := manifest.Step{
		ID:     "fallback",
		Method: "GET",
		Path:   "/fallback",
	}
	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"recover": {
			Steps: []manifest.Step{
				{
					ID:      "risky",
					Method:  "GET",
					Path:    "/fail",
					OnError: &fallback,
				},
				{
					ID:      "final",
					Method:  "GET",
					Path:    "/final",
					Extract: map[string]string{"ok": ".ok"},
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "recover", Flags{})
	res, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("expected no error (on_error continues), got %v", err)
	}
	if !onErrorCalled {
		t.Fatal("on_error step was not executed")
	}
	m := parseBody(t, res.Body)
	if m["ok"] != true {
		t.Fatalf("expected ok=true after on_error continue, got %v", m)
	}
}

// TestPipelineConfirmDefaultDeny verifies that a step with Confirm set and
// Flags.Yes=false returns an error.
func TestPipelineConfirmDefaultDeny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP call made despite confirmation denied")
		w.WriteHeader(500)
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"dangerous": {
			Steps: []manifest.Step{
				{
					ID:      "del",
					Method:  "DELETE",
					Path:    "/resource",
					Confirm: "This will delete the resource",
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "dangerous", Flags{Yes: false})
	_, err := Execute(context.Background(), req, nil)
	if err == nil {
		t.Fatal("expected error for unconfirmed step")
	}
	if !strings.Contains(err.Error(), "--yes") && !strings.Contains(err.Error(), "-y") {
		t.Fatalf("error should mention --yes/-y, got: %v", err)
	}
}

// TestPipelineConfirmYesProceeds verifies that Flags.Yes=true allows a confirmed step.
func TestPipelineConfirmYesProceeds(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"deleted":true}`))
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"dangerous": {
			Steps: []manifest.Step{
				{
					ID:      "del",
					Method:  "DELETE",
					Path:    "/resource",
					Confirm: "This will delete the resource",
					Extract: map[string]string{"deleted": ".deleted"},
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "dangerous", Flags{Yes: true})
	res, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("expected no error with --yes, got %v", err)
	}
	if !called {
		t.Fatal("HTTP call not made despite --yes")
	}
	m := parseBody(t, res.Body)
	if m["deleted"] != true {
		t.Fatalf("expected deleted=true, got %v", m)
	}
}

// TestPipelineBodyTransform verifies that body_transform runs jq against accVars
// and the resulting JSON is sent as the request body.
func TestPipelineBodyTransform(t *testing.T) {
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seed":
			_, _ = w.Write([]byte(`{"value":"hello"}`))
		case "/consume":
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"transform": {
			Steps: []manifest.Step{
				{
					ID:      "seed",
					Method:  "GET",
					Path:    "/seed",
					Extract: map[string]string{"value": ".value"},
				},
				{
					ID:            "consume",
					Method:        "POST",
					Path:          "/consume",
					BodyTransform: `{key: .value}`,
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "transform", Flags{})
	_, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the transformed body was sent with the extracted value.
	var sentBody map[string]any
	if err := json.Unmarshal(receivedBody, &sentBody); err != nil {
		t.Fatalf("parse sent body %q: %v", receivedBody, err)
	}
	if sentBody["key"] != "hello" {
		t.Fatalf("expected body.key=hello, got %v", sentBody)
	}
}

// TestPipelineFinalFilterAssembly verifies that the output.filter runs against
// accumulated vars and produces the expected JSON object.
func TestPipelineFinalFilterAssembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/name":
			_, _ = w.Write([]byte(`{"name":"alice"}`))
		case "/age":
			_, _ = w.Write([]byte(`{"age":30}`))
		}
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"profile": {
			Steps: []manifest.Step{
				{
					ID:      "name",
					Method:  "GET",
					Path:    "/name",
					Extract: map[string]string{"name": ".name"},
				},
				{
					ID:      "age",
					Method:  "GET",
					Path:    "/age",
					Extract: map[string]string{"age": ".age"},
				},
			},
			Output: manifest.Output{Filter: "{name: .name, age: .age}"},
		},
	})

	req := newPipelineReq(svc, "profile", Flags{})
	res, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := parseBody(t, res.Body)
	if m["name"] != "alice" {
		t.Fatalf("want name=alice, got %v", m["name"])
	}
	if m["age"] != float64(30) {
		t.Fatalf("want age=30, got %v", m["age"])
	}
}

// TestPipelineStepFailureAbort verifies that when a step returns a non-2xx
// status and OnError is nil, Execute returns an error.
func TestPipelineStepFailureAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"fail": {
			Steps: []manifest.Step{
				{
					ID:     "boom",
					Method: "GET",
					Path:   "/boom",
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "fail", Flags{})
	_, err := Execute(context.Background(), req, nil)
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

// TestPipelineDryRun verifies that dry-run returns a non-empty DryRunMsg
// and makes no HTTP calls.
func TestPipelineDryRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP call made in dry-run mode")
		w.WriteHeader(500)
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"preview": {
			Steps: []manifest.Step{
				{
					ID:     "step1",
					Method: "GET",
					Path:   "/first",
				},
				{
					ID:     "step2",
					Method: "POST",
					Path:   "/second",
				},
			},
			Output: manifest.Output{Filter: "."},
		},
	})

	req := newPipelineReq(svc, "preview", Flags{DryRun: true})
	res, err := Execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRunMsg == "" {
		t.Fatal("expected non-empty DryRunMsg in dry-run mode")
	}
	if res.Body != nil {
		t.Fatalf("expected nil body in dry-run, got %q", res.Body)
	}
}

// TestPipelineExtractAltAndPipe verifies that extract expressions using the //
// (alternative) and | (pipe) operators work correctly end-to-end, and that the
// pipeline output filter is NOT re-applied by the render layer.
//
// Regression test for the double-filter bug: executePipeline applied the
// output filter against accVars (correctly), but dispatch's output.Render then
// ran the same filter again against the already-assembled JSON — causing fields
// renamed by the first pass (e.g. .version→.sunshineVersion) to appear as null.
func TestPipelineExtractAltAndPipe(t *testing.T) {
	cfgResp := `{"version":"2025.1.2","platform":"linux"}`
	clientsResp := `{"named_certs":[{"name":"alice"},{"name":"bob"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config":
			_, _ = w.Write([]byte(cfgResp))
		case "/clients/list":
			_, _ = w.Write([]byte(clientsResp))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	svc := newPipelineSvc(srv.URL, map[string]manifest.Command{
		"status": {
			Steps: []manifest.Step{
				{
					ID:     "cfg",
					Method: "GET",
					Path:   "/config",
					// // alternative operator: falls back to "unknown" when null.
					Extract: map[string]string{
						"version":  `.version // "unknown"`,
						"platform": `.platform // "unknown"`,
					},
				},
				{
					ID:     "clients",
					Method: "GET",
					Path:   "/clients/list",
					// | pipe: count array elements.
					Extract: map[string]string{"paired": ".named_certs | length"},
				},
			},
			// Filter renames keys: .version→.sunshineVersion, .paired→.pairedClients.
			// The render layer must NOT re-run this filter on the assembled body.
			Output: manifest.Output{Filter: `{sunshineVersion: .version, platform: .platform, pairedClients: .paired}`},
		},
	})

	req := newPipelineReq(svc, "status", Flags{})
	res, err := Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate what dispatch does: pass the result through output.Render.
	// With the fix, res.Output has an empty filter so Render uses "." (pass-through).
	var buf bytes.Buffer
	if renderErr := output.Render(res.Body, res.Output, output.Options{}, &buf); renderErr != nil {
		t.Fatalf("render: %v", renderErr)
	}

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("parse rendered body: %v", err)
	}

	if m["sunshineVersion"] != "2025.1.2" {
		t.Errorf("sunshineVersion: want 2025.1.2, got %v (// operator or double-filter bug)", m["sunshineVersion"])
	}
	if got, ok := m["pairedClients"].(float64); !ok || got != 2 {
		t.Errorf("pairedClients: want 2, got %v (| length or double-filter bug)", m["pairedClients"])
	}
	if _, leaked := m["paired"]; leaked {
		t.Error("pre-rename 'paired' key in output: filter applied twice")
	}
	if _, leaked := m["version"]; leaked {
		t.Error("pre-rename 'version' key in output: filter applied twice")
	}
}
