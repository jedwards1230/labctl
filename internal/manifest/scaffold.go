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
// the requested auth scheme. The output is a PORTABLE manifest — it omits the
// user-specific base_url/tls_insecure and secret refs (those move to
// profile.yaml), so the same file is identical for every user and can seed a
// public plugin. Every block carries explanatory `#` comments and generic
// PLACEHOLDER values (no homelab specifics). The result validates cleanly via
// LoadService/Validate (structural). The trailing commented section shows the
// exact profile.yaml entry the user must add to bind it to their machine.
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
	writeProfileSection(&b, name, auth)
	return b.String(), nil
}

// ScaffoldProfileEntry returns the active (non-commented) profile.yaml entry for
// a service — the service-keyed binding block that sits under `services:`,
// carrying the machine-specific base_url (+ tls_insecure for ws-login) and a
// ref for each declared secret. It is the counterpart to the portable manifest
// Scaffold emits: the manifest says WHAT the service is, this says WHERE it lives
// for THIS user. An unknown auth value is an error.
func ScaffoldProfileEntry(name, auth string) (string, error) {
	if auth == "" {
		auth = DefaultScaffoldAuth
	}
	if !scaffoldAuthKnown(auth) {
		return "", fmt.Errorf("unknown auth scheme %q (want one of: %s)", auth, strings.Join(ScaffoldAuthSchemes, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    base_url: %s\n", scaffoldBaseURL(name, auth))
	if auth == "ws-login" {
		b.WriteString("    tls_insecure: true\n")
	}
	secrets := scaffoldSecrets(auth)
	if len(secrets) > 0 {
		b.WriteString("    secrets:\n")
		for _, s := range secrets {
			fmt.Fprintf(&b, "      %s:\n", s.name)
			fmt.Fprintf(&b, "        ref: \"op://VAULT/ITEM/%s\"\n", s.field)
		}
	}
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

	// This manifest is PORTABLE: base_url and tls_insecure are user-specific and
	// live in profile.yaml (see the commented section at the bottom), not here —
	// so the same manifest is identical for every user.
	if auth == "ws-login" {
		b.WriteString("\n# WebSocket JSON-RPC transport (the ws-login auth strategy needs it). This\n")
		b.WriteString("# is portable; the per-machine base_url/tls_insecure live in profile.yaml.\n")
		b.WriteString("transport: jsonrpc-ws\n")
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
		writeSecrets(b, prefix, scaffoldSecrets(auth)...)
	case "bearer":
		b.WriteString("# Send Authorization: Bearer <token>.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: bearer\n")
		b.WriteString("  scheme: Bearer              # the auth scheme word (Bearer, token, …)\n")
		b.WriteString("  value: \"{secret.token}\"\n")
		writeSecrets(b, prefix, scaffoldSecrets(auth)...)
	case "token":
		b.WriteString("# Send Authorization: token <token> (GitHub-style).\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: bearer\n")
		b.WriteString("  scheme: token               # emits `Authorization: token <value>`\n")
		b.WriteString("  value: \"{secret.token}\"\n")
		writeSecrets(b, prefix, scaffoldSecrets(auth)...)
	case "basic":
		b.WriteString("# HTTP Basic auth. username may be a literal or a {secret.X} reference.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: basic\n")
		b.WriteString("  username: \"your-username\"\n")
		b.WriteString("  password: \"{secret.password}\"\n")
		writeSecrets(b, prefix, scaffoldSecrets(auth)...)
	case "oauth2-client-credentials":
		b.WriteString("# OAuth2 client-credentials grant. labctl caches the token on disk and\n")
		b.WriteString("# refreshes it as needed. `token_url` is the token endpoint URL.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: oauth2-client-credentials\n")
		fmt.Fprintf(b, "  token_url: \"https://%s.example.com/oauth/token\"\n", name)
		b.WriteString("  client_id: \"{secret.client_id}\"          # the OAuth client_id\n")
		b.WriteString("  client_secret: \"{secret.client_secret}\"  # the OAuth client_secret\n")
		writeSecrets(b, prefix, scaffoldSecrets(auth)...)
	case "ws-login":
		b.WriteString("# JSON-RPC login: labctl calls `method` with `params` after connecting.\n")
		b.WriteString("auth:\n")
		b.WriteString("  strategy: ws-login\n")
		b.WriteString("  method: auth.login_with_api_key   # the jsonrpc login method\n")
		b.WriteString("  params: [\"{secret.api_key}\"]      # templated params (a JSON array)\n")
		writeSecrets(b, prefix, scaffoldSecrets(auth)...)
	}
}

// secret is a (name, op-field) pair for the secrets block of a scaffold.
type secret struct {
	name  string
	field string
}

// scaffoldSecrets returns the secret slots a given auth scheme references, so the
// manifest's `secrets:` block and the profile-entry refs stay in lockstep.
func scaffoldSecrets(auth string) []secret {
	switch auth {
	case "none":
		return nil
	case "bearer", "token":
		return []secret{{"token", "FIELD"}}
	case "basic":
		return []secret{{"password", "FIELD"}}
	case "oauth2-client-credentials":
		return []secret{{"client_id", "client_id"}, {"client_secret", "client_secret"}}
	default: // header-key, ws-login
		return []secret{{"api_key", "FIELD"}}
	}
}

// scaffoldBaseURL returns the placeholder base_url for a service's profile entry:
// a wss:// JSON-RPC endpoint for ws-login, an https:// URL otherwise.
func scaffoldBaseURL(name, auth string) string {
	if auth == "ws-login" {
		return fmt.Sprintf("wss://%s.example.com/api", name)
	}
	return fmt.Sprintf("https://%s.example.com", name)
}

// writeSecrets emits the manifest's secrets block. In a portable manifest a slot
// DECLARES the secret (with an optional env override for CI) but carries NO ref —
// the ref is user-specific and lives in profile.yaml.
func writeSecrets(b *strings.Builder, prefix string, secrets ...secret) {
	if len(secrets) == 0 {
		return
	}
	b.WriteString("\nsecrets:\n")
	b.WriteString("  # A secret is a reference, never a literal value — labctl resolves it at\n")
	b.WriteString("  # call time via the configured provider (1Password `op://` by default).\n")
	b.WriteString("  # This manifest only DECLARES each slot; bind the `ref:` per machine in\n")
	b.WriteString("  # profile.yaml (see the commented section below).\n")
	// Stable order regardless of caller.
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].name < secrets[j].name })
	for _, s := range secrets {
		fmt.Fprintf(b, "  %s:\n", s.name)
		fmt.Fprintf(b, "    env: %s_%s            # optional env override for CI\n", prefix, strings.ToUpper(s.name))
	}
}

// writeProfileSection appends a fully-commented block showing the exact
// profile.yaml entry the user must add to bind this portable manifest to their
// machine. Every line is a YAML comment, so it never affects validation.
func writeProfileSection(b *strings.Builder, name, auth string) {
	b.WriteString("\n# --- Portable manifest — add your machine-specific binding to profile.yaml ---\n")
	b.WriteString("# The block above is identical for every user. Your base_url and secret refs\n")
	b.WriteString("# live in ~/.config/labctl/profile.yaml (run `labctl init` to provision it):\n")
	b.WriteString("#\n")
	b.WriteString("# version: 1\n")
	b.WriteString("# services:\n")
	// Reuse ScaffoldProfileEntry for the exact active entry, then comment it.
	entry, err := ScaffoldProfileEntry(name, auth)
	if err != nil {
		return // unreachable: auth was already validated by the caller
	}
	for _, line := range strings.Split(strings.TrimRight(entry, "\n"), "\n") {
		fmt.Fprintf(b, "# %s\n", line)
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
