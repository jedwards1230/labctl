# CLAUDE.md

Guidance for Claude Code in this repository.

## What this is

`labctl` is a single, manifest-driven Go CLI for HTTP/RPC service APIs. A service
is one `services/<name>.yaml` manifest; the binary compiles in **zero**
service-specific logic. Adding/removing a service is a YAML edit, never a
recompile. It replaces a set of bespoke per-service bash wrappers.

Design docs (the *what/why* and *how*) live in `jedwards1230/home-orchestration`
under `docs/projects/homelab-api-cli-prd.md` and `homelab-api-cli-plan.md`.

## Core principle: unopinionated executor

The binary **gates nothing** — no `--read-only`, no write-confirm, no MCP
write-gating. It does exactly what the manifest says. Guardrails belong in the
consuming layer (an agent-host pre-call hook), not baked into the tool. Don't add
safety/policy logic here.

## Commands

```bash
go build -o labctl .
go test ./...                 # unit tests (httptest + fake `op` runner)
go test -race ./...
gofmt -l . && go vet ./...     # CI gates these (plus `go mod tidy` clean)

# Run against the example manifests without installing:
LABCTL_CONFIG_DIR="$PWD/examples" ./labctl list
LABCTL_CONFIG_DIR="$PWD/examples" ./labctl lint
LABCTL_CONFIG_DIR="$PWD/examples" ./labctl --dry-run radarr list
```

Go floor: **1.25** (see `go.mod`).

## Architecture

```
main.go                 entry → internal/cli
internal/
  manifest/   YAML model + XDG load/merge + schema validation
  command/    format-neutral Command model + producers (commands: block, generic verbs)
  template/   {secret.X}/{env.X}/{arg.N}/{var} expansion (JSON braces pass through)
  secret/     external-tool resolver (op read {ref}) + env override + idioms + cache
  auth/       apply none/header-key/bearer/basic to a request
  transport/  http (curl-equivalent, error extraction, typed errors→exit codes)
  output/     gojq filter + render modes (json/raw/scalar)
  engine/     resolve template→endpoint→auth→transport; pagination (none/fixed-query)
  telemetry/  optional OpenTelemetry tracing (no-op unless OTEL_* env configures it)
  cli/        cobra tree, dynamic per-service registration, builtins, exit-code mapping
```

**Telemetry**: off by default; one span per invocation when `OTEL_*` is set.
Fail-open, time-bounded flush — never blocks a command. The CLI is the first
consumer; the long-running MCP server (a later phase) reuses the same provider
and is where span-per-tool-call + metrics earn their keep.

**Two faces, one executor**: the CLI and the stdio MCP server both drive
`engine.Execute`, so behavior is identical.

## Status / roadmap

Phase 1 (done): http transport; none/header-key/bearer/basic auth; op resolver +
env override; generic verbs; gojq output; XDG load; `list`/`lint`/`doctor`.

Phase 2+3 (done): `jsonrpc-ws` transport + ws-login auth; oauth2-client-credentials
with on-disk token cache; OpenAPI inference via libopenapi (`spec:` + `spec_filter:`);
composed multi-step pipelines (`steps:` with extract/when/confirm/on_error); stdio
MCP server (`labctl mcp`). The `truenas` and `sunshine` manifests execute fully.

## Conventions

- stdout = data, stderr = diagnostics, real exit codes (0 ok, 2 usage, 3 auth,
  4 HTTP≥400, 5 network, 6 decode).
- Secrets are refs (`op://...`) resolved at call time — never values in manifests,
  never in argv, redacted in verbose/dry-run output.
- New auth strategy / transport / pagination style → wire it in its package + add
  a test; keep the manifest schema additive.
- Release: opt-in `semver:*` label on the merged PR (no label → no release);
  shared `ai-release.yml@v1`; ships cross-compiled static binaries.
