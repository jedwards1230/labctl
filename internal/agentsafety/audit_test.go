package agentsafety

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestLogMutationSingleLineJSON confirms LogMutation emits exactly one valid
// JSON line carrying the expected fields, stamps a Time, and defaults Caller.
func TestLogMutationSingleLineJSON(t *testing.T) {
	var buf bytes.Buffer
	LogMutation(&buf, MutationRecord{
		Service: "radarr",
		Command: "delete",
		Method:  "DELETE",
		DryRun:  false,
		Outcome: "ok",
		Params:  "/api/v3/movie/42",
	})

	out := buf.String()
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Fatalf("expected a single line, got %q", out)
	}

	var rec MutationRecord
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, out)
	}
	if rec.Service != "radarr" || rec.Command != "delete" || rec.Method != "DELETE" {
		t.Errorf("record = %+v, want service=radarr command=delete method=DELETE", rec)
	}
	if rec.Outcome != "ok" {
		t.Errorf("Outcome = %q, want ok", rec.Outcome)
	}
	if rec.Caller != "unknown" {
		t.Errorf("Caller = %q, want unknown (default)", rec.Caller)
	}
	if rec.Time.IsZero() {
		t.Error("Time was not stamped")
	}
}

// TestLogMutationRedactsSecretInParams proves a {secret.X} token in Params is
// redacted — the audit record must never carry a secret-bearing template.
func TestLogMutationRedactsSecretInParams(t *testing.T) {
	var buf bytes.Buffer
	LogMutation(&buf, MutationRecord{
		Service: "svc",
		Command: "post",
		Outcome: "ok",
		Params:  `{"token":"{secret.api_key}"}`,
	})
	out := buf.String()
	if strings.Contains(out, "{secret.") {
		t.Fatalf("audit record leaked a secret template: %q", out)
	}
	// json.Marshal HTML-escapes '<'/'>', so check the decoded Params value.
	var rec MutationRecord
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if rec.Params != `{"token":"<redacted>"}` {
		t.Fatalf("Params = %q, want the secret token redacted", rec.Params)
	}
}

// TestLogMutationPreservesGivenTimeAndCaller confirms an explicit Time/Caller is
// not overwritten (the caller-identity field is reserved for future use).
func TestLogMutationPreservesGivenTimeAndCaller(t *testing.T) {
	var buf bytes.Buffer
	ts := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	LogMutation(&buf, MutationRecord{Time: ts, Caller: "agent-x", Service: "s", Command: "c", Outcome: "ok"})
	var rec MutationRecord
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !rec.Time.Equal(ts) {
		t.Errorf("Time = %v, want %v", rec.Time, ts)
	}
	if rec.Caller != "agent-x" {
		t.Errorf("Caller = %q, want agent-x", rec.Caller)
	}
}
