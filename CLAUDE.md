# CLAUDE.md

@CONTRIBUTING.md

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

## Architecture

```
main.go                 entry → internal/cli
internal/
  manifest/   YAML model + XDG load/merge + schema validation
  command/    format-neutral Command model + producers (commands: block, generic verbs)
  template/   {secret.X}/{env.X}/{arg.N}/{var} expansion (JSON braces pass through)
  secret/     scheme-dispatched Provider interface (op:// → 1Password) + env override
              + idioms + cache; op provider injects OP_SERVICE_ACCOUNT_TOKEN into
              its subprocess only (never argv/log); legacy `secret:` block normalized
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

Phase 1 (done): http transport; none/header-key/bearer/basic auth; scheme-dispatched
secrets-provider interface (op:// → 1Password provider, with optional
service-account-token env injection into the `op` subprocess) + env override;
generic verbs; gojq output; XDG load; `list`/`lint`/`doctor`/`self-update` (sha256-verified
in-place binary update from the GitHub release). Adding a provider is
three edits in `internal/secret/provider.go` (new `Provider`, a config block, a
`NewRegistry` case) — dispatch is by URI scheme, so no engine/cli changes.

Phase 2+3 (done): `jsonrpc-ws` transport + ws-login auth; oauth2-client-credentials
with on-disk token cache; OpenAPI inference via libopenapi (`spec:` + `spec_filter:`);
composed multi-step pipelines (`steps:` with extract/when/confirm/on_error); stdio
MCP server (`labctl mcp`). The `truenas` and `sunshine` manifests execute fully.

## Conventions

- stdout = data, stderr = diagnostics, real exit codes (0 ok, 2 usage, 3 auth,
  4 HTTP≥400, 5 network, 6 decode).
- Secrets are refs (`op://...`) resolved at call time — never values in manifests,
  never in argv, redacted in verbose/dry-run output.
- A manifest is **portable** (what a service *is*); user-specific endpoints and
  credentials (`base_url`, secret `ref`s, per-machine endpoint/var/tls overrides)
  live in an optional `profile.yaml` at the config root. Precedence is **env
  override > profile > manifest**; absence of `profile.yaml` ⇒ all-in-one
  manifests behave unchanged. Structural `Validate` (well-formed) is split from
  `ValidateComplete` (resolvable base_url + every secret bound); completeness is
  enforced post-merge at execute time and surfaced by `doctor` / `lint --strict`.
  Portable + `profile.yaml` is now the **default** the shipped `examples/` use;
  in-manifest all-in-one binding (`base_url` / secret `ref` inline) is legacy and
  slated for removal.
- New auth strategy / transport / pagination style → wire it in its package + add
  a test; keep the manifest schema additive.
- Release: opt-in `semver:*` label on the merged PR (no label → no release);
  shared `ai-release.yml@v1`; ships cross-compiled static binaries.
