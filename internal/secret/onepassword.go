package secret

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// OnePassword resolves op:// references via the 1Password CLI. When configured
// with a service-account token it injects OP_SERVICE_ACCOUNT_TOKEN into the
// child process environment only; otherwise it inherits the ambient op session
// (personal/desktop). The token never appears in argv and is never written to
// any writer.
type OnePassword struct {
	command []string // argv with {ref} placeholder; default ["op","read","{ref}"]
	auth    opAuth
	run     Runner // non-nil only in tests; nil = real exec
}

// newOnePassword builds the provider from its config. Side-effect-free: it reads
// no files and resolves no token (that is lazy, on the real-exec path).
func newOnePassword(cfg manifest.ProviderConfig, runner Runner) *OnePassword {
	cmd := cfg.Command
	if len(cmd) == 0 {
		cmd = append([]string(nil), manifest.DefaultResolverCommand...)
	}
	return &OnePassword{
		command: append([]string(nil), cmd...),
		auth:    opAuth{src: cfg.Auth.ServiceAccountToken},
		run:     runner,
	}
}

// Scheme returns "op".
func (p *OnePassword) Scheme() string { return "op" }

// ResolvedBinary resolves the absolute path of the configured resolver binary
// (command[0], "op" by default) via exec.LookPath, for --dry-run/--verbose
// audit visibility only — an operator can see exactly which binary on $PATH
// would be trusted with the service-account token before ever running a real
// command. It performs no invocation. This is deliberately not a policy
// gate — a lookup failure is reported by the caller as unresolved, never as a
// hard failure (the binary itself gates nothing; guardrails belong in the
// consuming layer, see CLAUDE.md).
func (p *OnePassword) ResolvedBinary() (string, error) {
	if len(p.command) == 0 || p.command[0] == "" {
		return "", fmt.Errorf("resolver command is empty")
	}
	return exec.LookPath(p.command[0])
}

// Resolve turns an op:// ref into its value, honoring idiom and field fallback.
// ctx carries cancellation/deadline into the op subprocess on the real-exec path.
func (p *OnePassword) Resolve(ctx context.Context, ref Ref) (string, error) {
	idiom := ref.Idiom
	if idiom == "" {
		idiom = "read"
	}
	switch idiom {
	case "read":
		return p.resolveRead(ctx, ref)
	case "item-get", "item-json":
		return p.resolveItem(ctx, ref, idiom)
	default:
		return "", &ConfigError{Err: fmt.Errorf("unknown idiom %q", idiom)}
	}
}

// resolveRead runs the configured command with {ref} substituted. With a fields
// fallback it tries each candidate (replacing the ref's final segment) until one
// returns non-empty. An all-empty result returns ("", nil); the caller maps that
// to the "resolved empty" error.
func (p *OnePassword) resolveRead(ctx context.Context, ref Ref) (string, error) {
	refs := []string{ref.URI}
	if len(ref.Fields) > 0 {
		refs = refs[:0]
		for _, f := range ref.Fields {
			refs = append(refs, replaceLastSegment(ref.URI, f))
		}
	}
	var lastErr error
	for _, r := range refs {
		argv := substituteRef(p.command, r)
		out, err := p.exec(ctx, argv)
		if err != nil {
			lastErr = err
			continue
		}
		if out != "" {
			return out, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}

// resolveItem builds an op-specific `item get` argv. The ref is parsed as
// op://<vault>/<item>/<field>.
func (p *OnePassword) resolveItem(ctx context.Context, ref Ref, idiom string) (string, error) {
	vault, item, field, err := parseOpRef(ref.URI)
	if err != nil {
		return "", err
	}
	// Pass the item after a "--" end-of-flags guard so an item title/id beginning
	// with "-" can never be misread as a flag (hardening; inputs are trusted today).
	var argv []string
	switch idiom {
	case "item-get":
		argv = []string{"op", "item", "get", "--vault", vault, "--field", field, "--reveal", "--", item}
	case "item-json":
		// Resolve the whole item; field selection is left to the caller's filter.
		argv = []string{"op", "item", "get", "--vault", vault, "--format", "json", "--reveal", "--", item}
		_ = field
	}
	return p.exec(ctx, argv)
}

// exec runs argv via the injected runner (tests) or the real op CLI. On the real
// path it lazily resolves the service-account token and injects it into the
// child process env; a nil/empty token inherits the ambient op session. ctx is
// not plumbed into the test runner (the Runner seam is intentionally ctx-free).
func (p *OnePassword) exec(ctx context.Context, argv []string) (string, error) {
	if p.run != nil {
		return p.run(argv)
	}
	tok, err := p.auth.token()
	if err != nil {
		return "", err
	}
	return execWithEnv(ctx, argv, tok)
}

// execWithEnv mirrors the default exec runner, additionally injecting the
// service-account token into the child environment when non-empty. The token
// goes into the process env only — never argv, never any writer. ctx cancellation
// kills the op subprocess.
func execWithEnv(ctx context.Context, argv []string, tok string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty resolver command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr // let op print its own diagnostics (session expired, etc.)
	if tok != "" {
		// cmd.Env replaces the whole environment, so start from os.Environ().
		cmd.Env = withToken(os.Environ(), tok)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", argv[0], err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// withToken returns a copy of base with the OP_SERVICE_ACCOUNT_TOKEN entry
// appended. It does not mutate base. Pure and unit-testable.
func withToken(base []string, tok string) []string {
	out := make([]string, len(base), len(base)+1)
	copy(out, base)
	return append(out, "OP_SERVICE_ACCOUNT_TOKEN="+tok)
}

func substituteRef(command []string, ref string) []string {
	out := make([]string, len(command))
	for i, a := range command {
		out[i] = strings.ReplaceAll(a, "{ref}", ref)
	}
	return out
}

func replaceLastSegment(ref, field string) string {
	i := strings.LastIndexByte(ref, '/')
	if i < 0 {
		return ref
	}
	return ref[:i+1] + field
}

func parseOpRef(ref string) (vault, item, field string, err error) {
	trimmed := strings.TrimPrefix(ref, "op://")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("ref %q is not op://vault/item/field", ref)
	}
	return parts[0], parts[1], parts[2], nil
}
