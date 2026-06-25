package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
	if RedactHeader("Authorization", "Bearer x") != "<redacted>" {
		t.Error("Authorization not redacted")
	}
	if RedactHeader("Accept", "application/json") != "application/json" {
		t.Error("Accept should not be redacted")
	}
}

type stubResolver map[string]string

func (s stubResolver) Secret(name string) (string, error) { return s[name], nil }
