package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/transport"
)

// Exit codes (documented in the plan §11). stdout=data, stderr=diagnostics.
const (
	exitOK      = 0
	exitUsage   = 2
	exitAuth    = 3
	exitHTTP    = 4
	exitNetwork = 5
	exitDecode  = 6
	exitGeneral = 1
)

// usageError marks an argument/usage problem (exit 2).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// decodeError marks a filter/decode failure (exit 6).
type decodeError struct{ err error }

func (e *decodeError) Error() string { return e.err.Error() }
func (e *decodeError) Unwrap() error { return e.err }

// classify maps an error to its exit code.
func classify(err error) int {
	if err == nil {
		return exitOK
	}
	var httpErr *transport.HTTPError
	var rpcErr *transport.RPCError
	var authErr *transport.AuthError
	var netErr *transport.NetworkError
	var useErr *usageError
	var decErr *decodeError
	var secretAuthErr *secret.AuthError
	var secretCfgErr *secret.ConfigError
	var manifestCfgErr *manifest.ConfigError
	switch {
	case errors.As(err, &useErr):
		return exitUsage
	case errors.As(err, &secretCfgErr):
		return exitUsage
	case errors.As(err, &manifestCfgErr):
		return exitUsage
	case errors.As(err, &secretAuthErr):
		return exitAuth
	case errors.As(err, &authErr):
		return exitAuth
	case errors.As(err, &httpErr):
		return exitHTTP
	case errors.As(err, &rpcErr):
		return exitHTTP // JSON-RPC errors map to the same exit code as HTTP ≥400
	case errors.As(err, &netErr):
		return exitNetwork
	case errors.As(err, &decErr):
		return exitDecode
	default:
		return exitGeneral
	}
}

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
	code := classify(err)
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
