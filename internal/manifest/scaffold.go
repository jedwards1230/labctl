package manifest

import (
	"fmt"
	"sort"
	"strings"
)

// ScaffoldAuthSchemes lists the --auth values Scaffold understands, in a stable
// order suitable for help text and error messages.
var ScaffoldAuthSchemes = []string{
	"none",
	"header-key",
	"bearer",
	"token",
	"basic",
	"oauth2-client-credentials",
	"ws-login",
}

// DefaultScaffoldAuth is the auth scheme Scaffold emits when none is requested.
const DefaultScaffoldAuth = "header-key"

// Scaffold returns a commented starter manifest for a service named name, using
// the requested auth scheme. The output is a teaching template — every block
// carries explanatory `#` comments and uses generic PLACEHOLDER values (no
// homelab specifics) so it can seed a public plugin. The result validates
// cleanly via LoadService/Validate.
//
// auth is a CLI-facing alias, not a 1:1 manifest strategy: "token" emits a
// bearer strategy with `scheme: token`; "ws-login" switches the transport to
// jsonrpc-ws. An unknown auth value is an error.
func Scaffold(name, auth string) (string, error) {
	if auth == "" {
		auth = DefaultScaffoldAuth
	}
	if !scaffoldAuthKnown(auth) {
		return "", fmt.Errorf("unknown auth scheme %q (want one of: %s)", auth, strings.Join(ScaffoldAuthSchemes, ", "))
	}
	prefix := envPrefix(name)

	var b strings.Builder
	writeHeader(&b, name)
	writeConnection(&b, name, prefix, auth)
	writeAuth(&b, name, prefix, auth)
	writeCommands(&b, name, auth)
	return b.String(), nil
}

func scaffoldAuthKnown(auth string) bool {
	for _, s := range ScaffoldAuthSchemes {
		if s == auth {
			return true
		}
	}
	return false
}

// envPrefix derives an env-override prefix from a service name: uppercase, with
// every run of non-alphanumeric characters collapsed to a single underscore.
func envPrefix(name string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func writeHeader(b *strings.Builder, name string) {
	fmt.Fprintf(b, "# %s — labctl service manifest.\n", name)
	b.WriteString("#\n")
	b.WriteString("# A service is one YAML file; the binary compiles in zero service-specific\n")
	b.WriteString("# logic. Fill in the placeholders below, drop this file at\n")
	fmt.Fprintf(b, "#   ~/.config/labctl/services/%s.yaml\n", name)
	fmt.Fprintf(b, "# then run `labctl svc %s status`. Validate any time: `labctl lint <this-file>`.\n\n", name)
	fmt.Fprintf(b, "name: %s\n", name)
}

func writeConnection(b *strings.Builder, name, prefix, auth string) {
	b.WriteString("\n# Description shown by `labctl list`.\n")
	fmt.Fprintf(b, "description: %s service\n", name)

	if auth == "ws-login" {
		b.WriteString("\n# WebSocket JSON-RPC transport (the ws-login auth strategy needs it).\n")
		b.WriteString("transport: jsonrpc-ws\n")
		b.WriteString("\n# Base URL every call is resolved against. Replace with your service's URL.\n")
		fmt.Fprintf(b, "base_url: wss://%s.example.com/api\n", name)
		b.WriteString("\n# Skip TLS verification (curl -k). Drop this for a publicly-trusted cert.\n")
		b.WriteString("tls_insecure: true\n")
	} else {
		b.WriteString("\n# Base URL every command is resolved against. Replace with your service's URL.\n")
		fmt.Fprintf(b, "base_url: https://%s.example.com\n", name)
	}

	b.WriteString("\n# Prefix for env-var overrides: <PREFIX>_URL overrides base_url and\n")
	b.WriteString("# <PREFIX>_<SECRET> overrides a resolved secret (handy in CI/devcontainers).\n")
	fmt.Fprintf(b, "env_prefix: %s\n", prefix)
}

func writeAuth(b *strings.Builder, name, prefix, auth string) {
	b.WriteString("\n# --- Authentication ------------------------------------------------------\n")
	switch auth {
	case "none":
		b.WriteString("# This service needs no credentials.\n")
		b.WriteString("auth: { strategy: none }\n")
	case "header-key":
		b.WriteString("# Send a key in a custom header (e.g. X-Api-Key: <key>).\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: header-key\n")
		b.WriteString("  header: X-Api-Key            # the header name your API expects\n")
		b.WriteString("  value: \"{secret.api_key}\"    # resolves the api_key secret below\n")
		writeSecrets(b, prefix, secret{"api_key", "FIELD"})
	case "bearer":
		b.WriteString("# Send Authorization: Bearer <token>.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: bearer\n")
		b.WriteString("  scheme: Bearer              # the auth scheme word (Bearer, token, …)\n")
		b.WriteString("  value: \"{secret.token}\"\n")
		writeSecrets(b, prefix, secret{"token", "FIELD"})
	case "token":
		b.WriteString("# Send Authorization: token <token> (GitHub-style).\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: bearer\n")
		b.WriteString("  scheme: token               # emits `Authorization: token <value>`\n")
		b.WriteString("  value: \"{secret.token}\"\n")
		writeSecrets(b, prefix, secret{"token", "FIELD"})
	case "basic":
		b.WriteString("# HTTP Basic auth. username may be a literal or a {secret.X} reference.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: basic\n")
		b.WriteString("  username: \"your-username\"\n")
		b.WriteString("  password: \"{secret.password}\"\n")
		writeSecrets(b, prefix, secret{"password", "FIELD"})
	case "oauth2-client-credentials":
		b.WriteString("# OAuth2 client-credentials grant. labctl caches the token on disk and\n")
		b.WriteString("# refreshes it as needed. `value` is the token endpoint URL.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: oauth2-client-credentials\n")
		fmt.Fprintf(b, "  value: \"https://%s.example.com/oauth/token\"\n", name)
		b.WriteString("  username: \"{secret.client_id}\"      # the OAuth client_id\n")
		b.WriteString("  password: \"{secret.client_secret}\"  # the OAuth client_secret\n")
		writeSecrets(b, prefix, secret{"client_id", "client_id"}, secret{"client_secret", "client_secret"})
	case "ws-login":
		b.WriteString("# JSON-RPC login: labctl calls `method` with `params` after connecting.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: ws-login\n")
		b.WriteString("  method: auth.login_with_api_key   # the jsonrpc login method\n")
		b.WriteString("  params: [\"{secret.api_key}\"]      # templated params (a JSON array)\n")
		writeSecrets(b, prefix, secret{"api_key", "FIELD"})
	}
}

// secret is a (name, op-field) pair for the secrets block of a scaffold.
type secret struct {
	name  string
	field string
}

func writeSecrets(b *strings.Builder, prefix string, secrets ...secret) {
	b.WriteString("\nsecrets:\n")
	b.WriteString("  # A secret is a reference, never a literal value — labctl resolves it at\n")
	b.WriteString("  # call time via the configured provider (1Password `op://` by default).\n")
	// Stable order regardless of caller.
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].name < secrets[j].name })
	for _, s := range secrets {
		fmt.Fprintf(b, "  %s:\n", s.name)
		fmt.Fprintf(b, "    ref: \"op://VAULT/ITEM/%s\"   # e.g. op://Personal/MyService/%s\n", s.field, s.field)
		fmt.Fprintf(b, "    env: %s_%s            # optional env override for CI\n", prefix, strings.ToUpper(s.name))
	}
}

func writeCommands(b *strings.Builder, name, auth string) {
	b.WriteString("\n# --- Commands ------------------------------------------------------------\n")
	b.WriteString("# Each command is one request. {arg.0}, {arg.1}, … interpolate positional\n")
	b.WriteString("# CLI args; {secret.X} injects a resolved secret; {env.X} an env var.\n")
	b.WriteString("commands:\n")

	if auth == "ws-login" {
		b.WriteString("  # A read command. Run: labctl svc " + name + " status\n")
		b.WriteString("  status:\n")
		b.WriteString("    help: service status / health\n")
		b.WriteString("    method: system.info        # the jsonrpc method to call\n")
		b.WriteString("    output: { filter: \".\" }    # optional jq filter over the response\n")
		b.WriteString("\n  # An unauthenticated health check taking no args.\n")
		b.WriteString("  ping:\n")
		b.WriteString("    help: liveness ping (no auth)\n")
		b.WriteString("    method: core.ping\n")
		b.WriteString("    noauth: true\n")
		b.WriteString("\n# Generic JSON-RPC passthrough is always available without declaring a command:\n")
		b.WriteString("#   labctl svc " + name + " call system.info\n")
		b.WriteString("#   labctl svc " + name + " call some.method '[\"arg1\", 42]'\n")
		return
	}

	b.WriteString("  # A read command (GET). Run: labctl svc " + name + " status\n")
	b.WriteString("  status:\n")
	b.WriteString("    help: service status / health\n")
	b.WriteString("    method: GET\n")
	b.WriteString("    path: /api/status\n")
	b.WriteString("    output: { filter: \".\" }    # optional jq filter over the JSON response\n")
	b.WriteString("\n  # A read command taking a positional arg: labctl svc " + name + " get-item 42\n")
	b.WriteString("  get-item:\n")
	b.WriteString("    help: fetch one item by id\n")
	b.WriteString("    method: GET\n")
	b.WriteString("    path: /api/items/{arg.0}\n")
	b.WriteString("\n# Generic verb passthrough is always available without declaring a command:\n")
	b.WriteString("#   labctl svc " + name + " get /api/items\n")
	b.WriteString("#   labctl svc " + name + " post /api/items '{\"name\":\"demo\"}'   # a [WRITE] call\n")
}
