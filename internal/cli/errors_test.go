package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/agentsafety"
	"github.com/jedwards1230/labctl/internal/transport"
)

func TestReportErrorPlain(t *testing.T) {
	var buf bytes.Buffer
	code := reportError(&buf, agentsafety.NewUsageError("nope"), false, "radarr", "list")
	if code != agentsafety.ExitUsage {
		t.Errorf("code = %d, want %d", code, agentsafety.ExitUsage)
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
	if code != agentsafety.ExitHTTP {
		t.Errorf("code = %d, want %d", code, agentsafety.ExitHTTP)
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
	if code != agentsafety.ExitGeneral {
		t.Errorf("code = %d, want %d", code, agentsafety.ExitGeneral)
	}
	var env errorEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if env.Error != "boom" || env.Status != 0 || env.Detail != "" {
		t.Errorf("envelope = %+v, want error=boom status=0 detail=''", env)
	}
}
