package agentsafety

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// MutationRecord is one structured audit record emitted per WRITE call from the
// MCP boundary (the agent-facing face). It is additive diagnostics — never a
// gate — and carries no live secret: Params is redacted via RedactSecretTokens.
type MutationRecord struct {
	Time    time.Time `json:"time"`             // set by LogMutation
	Caller  string    `json:"caller"`           // best-effort; "unknown" until caller-identity plumbing exists
	Service string    `json:"service"`          // service name
	Command string    `json:"command"`          // command/verb id
	Method  string    `json:"method,omitempty"` // c.Method when known
	DryRun  bool      `json:"dry_run"`          // true when the call was a --dry-run preview
	Outcome string    `json:"outcome"`          // "ok" | "error" | "dry-run"
	Class   string    `json:"class,omitempty"`  // ClassName on error, else ""
	Status  int       `json:"status,omitempty"` // http status when known (error path)
	Params  string    `json:"params,omitempty"` // best-effort, REDACTED (never a live secret)
}

// LogMutation marshals rec to a single JSON line on w. It stamps rec.Time (if
// unset), defaults an empty Caller to "unknown", and redacts any {secret.X}
// token still present in Params. It never fails a call: a marshal error is
// dropped silently (this is best-effort diagnostics, not a gate).
func LogMutation(w io.Writer, rec MutationRecord) {
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	if rec.Caller == "" {
		rec.Caller = "unknown"
	}
	rec.Params = RedactSecretTokens(rec.Params)
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(w, string(b))
}
