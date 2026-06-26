package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/transport"
)

// TestClassifyExitCodes pins the documented exit-code contract (plan §11):
// 0 ok, 2 usage, 3 auth, 4 HTTP≥400, 5 network, 6 decode, 1 general.
func TestClassifyExitCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, exitOK},
		{"usage", &usageError{"bad flag"}, exitUsage},
		{"auth", &transport.AuthError{Err: errors.New("401")}, exitAuth},
		{"http", &transport.HTTPError{Status: 404, Method: "GET", URL: "u"}, exitHTTP},
		{"network", &transport.NetworkError{Err: errors.New("dial")}, exitNetwork},
		{"decode", &decodeError{errors.New("jq")}, exitDecode},
		{"secret-config", &secret.ConfigError{Err: errors.New("bad source")}, exitUsage},
		{"manifest-config", &manifest.ConfigError{Err: errors.New("bad config")}, exitUsage},
		{"general", errors.New("other"), exitGeneral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.err); got != tt.want {
				t.Errorf("classify(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyWrappedError confirms classify uses errors.As, so a typed error
// wrapped with %w still maps to its code.
func TestClassifyWrappedError(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", &transport.HTTPError{Status: 500})
	if got := classify(wrapped); got != exitHTTP {
		t.Errorf("wrapped HTTPError classify = %d, want %d", got, exitHTTP)
	}
}

func TestReportErrorPlain(t *testing.T) {
	var buf bytes.Buffer
	code := reportError(&buf, &usageError{"nope"}, false, "radarr", "list")
	if code != exitUsage {
		t.Errorf("code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(buf.String(), "Error: nope") {
		t.Errorf("stderr = %q, want to contain 'Error: nope'", buf.String())
	}
}

// TestReportErrorJSONEnvelope checks the --json-errors envelope carries the HTTP
// status/detail and the service/command context, and is valid JSON.
func TestReportErrorJSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	httpErr := &transport.HTTPError{Status: 404, Detail: "not found", Method: "GET", URL: "u"}
	code := reportError(&buf, httpErr, true, "radarr", "get")
	if code != exitHTTP {
		t.Errorf("code = %d, want %d", code, exitHTTP)
	}
	var env errorEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("envelope not valid JSON: %v (%q)", err, buf.String())
	}
	if env.Status != 404 || env.Detail != "not found" || env.Service != "radarr" || env.Command != "get" {
		t.Errorf("envelope = %+v, want status=404 detail='not found' service=radarr command=get", env)
	}
}

// TestReportErrorJSONNonHTTP confirms a non-HTTP error yields an envelope with no
// status/detail (omitempty) but the message and exit code intact.
func TestReportErrorJSONNonHTTP(t *testing.T) {
	var buf bytes.Buffer
	code := reportError(&buf, errors.New("boom"), true, "", "")
	if code != exitGeneral {
		t.Errorf("code = %d, want %d", code, exitGeneral)
	}
	var env errorEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if env.Error != "boom" || env.Status != 0 || env.Detail != "" {
		t.Errorf("envelope = %+v, want error=boom status=0 detail=''", env)
	}
}
