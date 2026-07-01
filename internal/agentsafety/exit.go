package agentsafety

import (
	"errors"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/transport"
)

// Exit codes (documented in the plan §11). stdout=data, stderr=diagnostics.
// These are the canonical taxonomy shared by the CLI and the MCP server.
const (
	ExitOK      = 0
	ExitUsage   = 2
	ExitAuth    = 3
	ExitHTTP    = 4
	ExitNetwork = 5
	ExitDecode  = 6
	ExitGeneral = 1
)

// UsageError marks an argument/usage problem (exit 2).
type UsageError struct{ msg string }

// NewUsageError builds a *UsageError carrying msg.
func NewUsageError(msg string) *UsageError { return &UsageError{msg: msg} }

func (e *UsageError) Error() string { return e.msg }

// DecodeError marks a filter/decode failure (exit 6).
type DecodeError struct{ err error }

// NewDecodeError builds a *DecodeError wrapping err.
func NewDecodeError(err error) *DecodeError { return &DecodeError{err: err} }

func (e *DecodeError) Error() string { return e.err.Error() }
func (e *DecodeError) Unwrap() error { return e.err }

// Classify maps an error to its exit code.
func Classify(err error) int {
	if err == nil {
		return ExitOK
	}
	var httpErr *transport.HTTPError
	var rpcErr *transport.RPCError
	var authErr *transport.AuthError
	var netErr *transport.NetworkError
	var useErr *UsageError
	var decErr *DecodeError
	var secretAuthErr *secret.AuthError
	var secretCfgErr *secret.ConfigError
	var manifestCfgErr *manifest.ConfigError
	var manifestDecErr *manifest.DecodeError
	switch {
	case errors.As(err, &useErr):
		return ExitUsage
	case errors.As(err, &secretCfgErr):
		return ExitUsage
	case errors.As(err, &manifestCfgErr):
		return ExitUsage
	case errors.As(err, &manifestDecErr):
		return ExitDecode
	case errors.As(err, &secretAuthErr):
		return ExitAuth
	case errors.As(err, &authErr):
		return ExitAuth
	case errors.As(err, &httpErr):
		return ExitHTTP
	case errors.As(err, &rpcErr):
		return ExitHTTP // JSON-RPC errors map to the same exit code as HTTP ≥400
	case errors.As(err, &netErr):
		return ExitNetwork
	case errors.As(err, &decErr):
		return ExitDecode
	default:
		return ExitGeneral
	}
}

// ClassName returns the short symbolic name for an exit code:
// "ok"/"usage"/"auth"/"http"/"network"/"decode"/"error".
func ClassName(code int) string {
	switch code {
	case ExitOK:
		return "ok"
	case ExitUsage:
		return "usage"
	case ExitAuth:
		return "auth"
	case ExitHTTP:
		return "http"
	case ExitNetwork:
		return "network"
	case ExitDecode:
		return "decode"
	default:
		return "error"
	}
}

// HTTPStatus extracts the HTTP status from a *transport.HTTPError anywhere in
// err's chain, reporting ok=false when none is present.
func HTTPStatus(err error) (int, bool) {
	var httpErr *transport.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status, true
	}
	return 0, false
}
