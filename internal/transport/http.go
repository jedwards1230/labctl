// Package transport carries a resolved request over the wire. Phase 1 implements
// http (curl-equivalent); jsonrpc-ws lands in a later phase. The transport is
// dumb on purpose — template expansion, auth, and pagination happen above it.
package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/internal/auth"
)

// HTTPRequest is a fully-resolved HTTP call (no templates remain).
type HTTPRequest struct {
	Ctx         context.Context // nil → context.Background()
	Method      string
	URL         string // includes any query string
	Headers     map[string]string
	Body        []byte // nil for no body
	ContentType string
	Accept      string
	TLSInsecure bool
	Timeout     time.Duration
	Auth        auth.Applier
	NoAuth      bool
	Verbose     io.Writer // non-nil → write request/response diagnostics (secrets redacted)
}

// HTTPError is a ≥400 response; Detail is the extracted server message.
type HTTPError struct {
	Status int
	Detail string
	Method string
	URL    string
}

func (e *HTTPError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d on %s %s: %s", e.Status, e.Method, e.URL, e.Detail)
	}
	return fmt.Sprintf("HTTP %d on %s %s", e.Status, e.Method, e.URL)
}

// DoHTTPWithHeaders executes the request and returns the response body and
// headers, or an *HTTPError on a ≥400 status, or a transport error.
func DoHTTPWithHeaders(r HTTPRequest) ([]byte, http.Header, error) {
	accept := r.Accept
	if accept == "" {
		accept = "application/json"
	}

	var bodyReader io.Reader
	if r.Body != nil {
		bodyReader = bytes.NewReader(r.Body)
	}
	ctx := r.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(r.Method), r.URL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", accept)
	if r.Body != nil {
		ct := r.ContentType
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if err := r.Auth.Apply(req, r.NoAuth); err != nil {
		return nil, nil, &AuthError{err}
	}

	if r.Verbose != nil {
		writeVerboseRequest(r.Verbose, req, r.Body)
	}

	client := &http.Client{Timeout: r.Timeout}
	if r.TLSInsecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in per manifest
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, &NetworkError{err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, &NetworkError{err}
	}

	if r.Verbose != nil {
		_, _ = fmt.Fprintf(r.Verbose, "< %s\n", resp.Status)
	}

	if resp.StatusCode >= 400 {
		return nil, nil, &HTTPError{
			Status: resp.StatusCode,
			Detail: extractError(body),
			Method: strings.ToUpper(r.Method),
			URL:    r.URL,
		}
	}
	return body, resp.Header, nil
}

// DoHTTP executes the request and returns the response body, or an *HTTPError on
// a ≥400 status (after extracting a server message), or a transport error.
func DoHTTP(r HTTPRequest) ([]byte, error) {
	body, _, err := DoHTTPWithHeaders(r)
	return body, err
}

// AuthError wraps a credential/auth-apply failure (exit code 3).
type AuthError struct{ Err error }

func (e *AuthError) Error() string { return e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

// NetworkError wraps a transport/connection failure (exit code 5).
type NetworkError struct{ Err error }

func (e *NetworkError) Error() string { return e.Err.Error() }
func (e *NetworkError) Unwrap() error { return e.Err }

// extractError mirrors the wrappers' `.message // .detail // .error // .`.
func extractError(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		for _, key := range []string{"message", "detail", "error"} {
			if v, ok := obj[key]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return string(trimmed)
}

func writeVerboseRequest(w io.Writer, req *http.Request, body []byte) {
	_, _ = fmt.Fprintf(w, "> %s %s\n", req.Method, req.URL.String())
	for k, vals := range req.Header {
		for _, v := range vals {
			_, _ = fmt.Fprintf(w, "> %s: %s\n", k, RedactHeader(k, v))
		}
	}
	if len(body) > 0 {
		_, _ = fmt.Fprintf(w, "> (body %d bytes)\n", len(body))
	}
}

// RedactHeader hides credential-bearing header values in verbose/dry-run output.
func RedactHeader(key, val string) string {
	switch strings.ToLower(key) {
	case "authorization", "x-api-key", "x-n8n-api-key", "cookie", "proxy-authorization":
		return "<redacted>"
	default:
		return val
	}
}
