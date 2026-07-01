package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jedwards1230/labctl/internal/agentsafety"
	"github.com/jedwards1230/labctl/internal/transport"
)

// errorEnvelope is the --json-errors structure.
type errorEnvelope struct {
	Error   string `json:"error"`
	Service string `json:"service,omitempty"`
	Command string `json:"command,omitempty"`
	Status  int    `json:"status,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// reportError writes err to stderr (plain or JSON) and returns the exit code.
func reportError(w io.Writer, err error, jsonErrors bool, service, cmd string) int {
	code := agentsafety.Classify(err)
	if !jsonErrors {
		_, _ = fmt.Fprintln(w, "Error:", err.Error())
		return code
	}
	env := errorEnvelope{Error: err.Error(), Service: service, Command: cmd}
	var httpErr *transport.HTTPError
	if errors.As(err, &httpErr) {
		env.Status = httpErr.Status
		env.Detail = httpErr.Detail
	}
	b, _ := json.Marshal(env)
	_, _ = fmt.Fprintln(w, string(b))
	return code
}
