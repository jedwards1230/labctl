package transport

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/labctl/internal/auth"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/template"
)

func noAuthApplier() auth.Applier {
	return auth.New(manifest.Auth{Strategy: "none"}, template.Env{})
}

func TestDoHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing Accept header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, err := DoHTTP(HTTPRequest{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second, Auth: noAuthApplier()})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %s", body)
	}
}

func TestDoHTTPErrorExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"bad thing"}`))
	}))
	defer srv.Close()

	_, err := DoHTTP(HTTPRequest{Method: "POST", URL: srv.URL, Timeout: 5 * time.Second, Auth: noAuthApplier()})
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("want *HTTPError, got %T", err)
	}
	if he.Status != 422 || he.Detail != "bad thing" {
		t.Fatalf("got status=%d detail=%q", he.Status, he.Detail)
	}
}

func TestDoHTTPHeaderKeyAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k3y" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	applier := auth.New(
		manifest.Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.api_key}"},
		template.Env{Secrets: stubResolver{"api_key": "k3y"}},
	)
	if _, err := DoHTTP(HTTPRequest{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second, Auth: applier}); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkError(t *testing.T) {
	_, err := DoHTTP(HTTPRequest{Method: "GET", URL: "http://127.0.0.1:1", Timeout: time.Second, Auth: noAuthApplier()})
	var ne *NetworkError
	if !errors.As(err, &ne) {
		t.Fatalf("want *NetworkError, got %T: %v", err, err)
	}
}

func TestRedactHeader(t *testing.T) {
	// The always-redact set: authorization, cookie, proxy-authorization (no args).
	for _, k := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if RedactHeader(k, "secret-value") != "<redacted>" {
			t.Errorf("%s not redacted with zero extra args", k)
		}
	}
	if RedactHeader("Accept", "application/json") != "application/json" {
		t.Error("Accept should not be redacted")
	}
	// A header is redacted only when named as a secretHeader.
	if RedactHeader("X-Plex-Token", "tok") != "tok" {
		t.Error("X-Plex-Token should print when not named as a credential header")
	}
	if RedactHeader("X-Plex-Token", "tok", "X-Plex-Token") != "<redacted>" {
		t.Error("named secret header should redact (case-sensitive match)")
	}
	if RedactHeader("x-plex-token", "tok", "X-Plex-Token") != "<redacted>" {
		t.Error("named secret header should redact case-insensitively")
	}
	// An empty secretHeader name must not redact everything.
	if RedactHeader("X-Custom", "val", "") != "val" {
		t.Error("empty secretHeader name should not redact")
	}
	// x-api-key is no longer auto-redacted (dropped from the static list).
	if RedactHeader("X-Api-Key", "k3y") != "k3y" {
		t.Error("X-Api-Key should print when not the active credential (no longer in static list)")
	}
}

// TestVerboseRedactsAuthHeaderKey proves -v redacts exactly the header the
// header-key strategy wrote (an arbitrary name like X-Plex-Token), while a
// manually-declared header carrying a value still prints in full.
func TestVerboseRedactsAuthHeaderKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	applier := auth.New(
		manifest.Auth{Strategy: "header-key", Header: "X-Plex-Token", Value: "{secret.token}"},
		template.Env{Secrets: stubResolver{"token": "PLEX-SECRET"}},
	)
	var verbose bytes.Buffer
	_, err := DoHTTP(HTTPRequest{
		Method:  "GET",
		URL:     srv.URL,
		Headers: map[string]string{"X-Custom": "plain-visible"},
		Timeout: 5 * time.Second,
		Auth:    applier,
		Verbose: &verbose,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := verbose.String()
	if strings.Contains(out, "PLEX-SECRET") {
		t.Fatalf("verbose output leaked the token:\n%s", out)
	}
	if !strings.Contains(out, "X-Plex-Token: <redacted>") {
		t.Fatalf("auth credential header not redacted:\n%s", out)
	}
	// A manually-declared header that is not the credential still prints.
	if !strings.Contains(out, "X-Custom: plain-visible") {
		t.Fatalf("non-credential header should print in full:\n%s", out)
	}
}

type stubResolver map[string]string

func (s stubResolver) Secret(name string) (string, error) { return s[name], nil }
