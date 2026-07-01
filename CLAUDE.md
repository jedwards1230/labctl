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

The binary **gates nothing** — no `--read-only`, no MCP write-gating — **except** a
step a manifest explicitly marks `confirm:`, which aborts unless `--yes/-y` clears
it (manifest-opt-in, fail-closed; no interactive prompt). It otherwise does exactly
what the manifest says. Guardrails belong in the consuming layer (an agent-host
pre-call hook), not baked into the tool. Don't add safety/policy logic here.

## Architecture

```
main.go                 entry → internal/cli
catalog/                portable service manifests embedded in the binary (//go:embed *.yaml)
internal/
  manifest/   YAML model + XDG load/merge + schema validation + embedded/installed-catalog merge + catalog store
  command/    format-neutral Command model + producers (commands: block, generic verbs)
  template/   {secret.X}/{env.X}/{arg.N}/{var} expansion (JSON braces pass through)
  secret/     scheme-dispatched Provider interface (op:// → 1Password) + env override
              + idioms + cache; op provider injects OP_SERVICE_ACCOUNT_TOKEN into
              its subprocess only (never argv/log); legacy `secret:` block normalized
  auth/       apply none/header-key/bearer/basic/oauth2-client-credentials/ws-login to a request
  transport/  http (curl-equivalent) + jsonrpc-ws; error extraction, typed errors→exit codes
  output/     gojq filter + render modes (json/raw/scalar)
  engine/     resolve template→endpoint→auth→transport; pagination (none/fixed-query/cursor/page-number/page-until-short)
  agentsafety/ shared agent-safety layer: secret scrubber, dry-run preview render, exit-code taxonomy+classifier, tool-annotation policy, mutation audit log (renders/classifies/records — gates nothing)
  telemetry/  optional OpenTelemetry tracing (no-op unless OTEL_* env configures it)
  cli/        cobra tree, dynamic per-service registration, builtins, exit-code mapping
```

**Telemetry**: off by default; one span per invocation when `OTEL_*` is set.
Fail-open, time-bounded flush — never blocks a command. The CLI emits one span
per invocation; the now-shipped MCP server reuses the same provider and emits
one span per tool call (`<svc>_<command>`). Metrics remain future work.

**Two faces, one executor**: the CLI and the MCP server (stdio or
streamable-HTTP) both drive `engine.Execute`, so behavior is identical. The MCP
face reaches parity with the CLI on agent-safety: every tool takes an optional
`dry_run` boolean (previews the request, no network — reusing the CLI's
preview/redaction); error results are structured (`IsError` + text fallback +
`StructuredContent {error,class,status}` from the shared exit-code classifier);
and each WRITE call emits one JSON mutation audit record to stderr. It still
gates nothing — no write-confirmation or elicitation (a later PR).

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
composed multi-step pipelines (`steps:` with extract/when/confirm/on_error); MCP
server (`labctl mcp`) over stdio (default) or streamable-HTTP (`--http :9000`,
MCP endpoint at `/mcp`, `GET /healthz` probe — for in-cluster gateway federation).
Secure by default: a `--http` bind to a non-loopback address refuses to start
without a bearer token (`LABCTL_MCP_AUTH_TOKEN` or `--auth-token-file`) unless
`--allow-unauthenticated` explicitly opts out; a loopback bind (127.0.0.1/::1/
localhost) needs no token. The `truenas` and `sunshine` manifests execute fully.

Embedded catalog (done): 15 portable manifests (top-level `catalog/`) are
compiled into the binary via `//go:embed`, so consumers no longer vendor copies.
A manifest is plain YAML and editing one is **rebuild-free** — the binary just
ships sane defaults. The authoring loop:

- `labctl catalog list` / `catalog show <name>` — inspect/dump the embedded manifests.
- `labctl catalog edit <name>` — seed the **full** embedded manifest into
  `<config-dir>/services/<name>.yaml`, where it shadows the embedded one at the
  next load. Iterate live (no recompile). A FULL copy is seeded, not a sparse
  patch, because a local override **wholesale replaces** the embedded entry
  (validated standalone, no field-level merge — see `decodeService`/`Validate` in
  `load.go`); a partial override would drop endpoints or fail validation. Refuses
  to clobber without `--force`; prints the absolute path; `--edit` opens
  `$VISUAL`/`$EDITOR`.
- `labctl catalog vendor <name> [--catalog-dir catalog]` — promote an edited
  override back into a repo checkout's `catalog/` source tree to commit and ship
  embedded. Validates first (structural `Validate` — a portable manifest, no
  `base_url`/secret `ref`), so a broken manifest is never promoted; refuses to
  clobber without `--force`. `--catalog-dir` is required because the running
  binary can't know the repo path.

Named, installable catalogs (done): beyond the single embedded catalog, install
**named** catalogs of portable manifests into `<config-dir>/catalogs/<name>/` from
a directory or a git repo:

- `labctl catalog add <source> [--name --ref --force]` — fetch a dir or git URL,
  validate every top-level `*.yaml` against the schema AND structural `Validate`
  (portability: no `base_url`/secret `ref`) fail-closed, then install atomically
  (stage in a temp dir, swap into place). A git source is pinned to its resolved
  commit SHA in `.labctl-catalog.json`. Git fetches shell to the system `git` with
  `ext`/`fd` transports blocked and the URL after `--` (no shell). `--openapi
  <url|file>` materializes a single-service portable manifest from an OpenAPI
  3.x document instead (operations → `commands:`, `securitySchemes` inferred
  into `auth:` on a best-effort basis, un-mappable auth falls back to `auth: {
  strategy: none }` with an explanatory comment; `servers[]` is never carried
  over; the spec is parsed once at add-time and not vendored — no `spec:`
  reference is kept). Implementation: `internal/manifest/openapi_scaffold.go`
  + `internal/cli/catalog_openapi.go`.
- `labctl catalog update [name]` / `remove <name>` / `installed`.
- `labctl catalog validate <dir>` — the SAME fail-closed gate `catalog add`
  runs (`ValidatePortableManifest` + intra-dir duplicate-name check), exposed
  read-only and config-dir-free: no network, no install, no profile/catalog
  interaction — just a per-file `ok`/`FAIL` report and exit 0/2. This is what a
  third-party catalog repo runs in its own CI (see the `validate-catalog`
  composite action below) before anyone runs `catalog add` against it.
  Implementation: `internal/cli/catalog_validate.go`.
- **Resolution precedence (highest wins):** local `services/<name>.yaml` >
  installed catalogs (`catalogs/*/`, sorted) > embedded floor. `OriginOf` returns
  the dynamic `catalog:<name>` for an installed-catalog service; a local file
  shadowing one is `override`. **Two installed catalogs MAY define the same
  service name** — both install (no load error); each stays addressable via its
  qualified `<catalog>:<service>` selector (`Loaded.Services` keys every
  installed-catalog service both ways — bare AND qualified — except a bare name
  more than one catalog defines, which is dropped from `Services` and recorded in
  `Loaded.Ambiguous` instead). `Loaded.Lookup` on an ambiguous bare name is a
  `*ConfigError` (exit 2) listing both qualified forms — labctl never silently
  picks one. The MCP server derives a tool's name from the *selector*
  (`<catalog>-<service>_<command>` once qualified, `:` sanitized to `-`), so
  installing a second catalog that collides with an existing name **renames**
  the first catalog's tools from `<service>_<command>` to
  `<catalog>-<service>_<command>` — inherent to disambiguation, not a bug, but
  worth knowing (`internal/mcpserver/mcpserver.go`'s `selectorToolPrefix`).
  The portability rule is the security boundary (enforced on add AND at load),
  so a catalog is inert until `profile.yaml` binds it — no signing needed, no
  execution-time gating added. Store API lives in
  `internal/manifest/catalogstore.go`; the validate-on-add gate
  (`SchemaValidate`/`ValidatePortableManifest`) in `internal/manifest/schemacheck.go`;
  CLI handlers in `internal/cli/catalog_install.go`.
- `.github/actions/validate-catalog` — a composite action a third-party catalog
  repo points its own CI at (`uses:
  jedwards1230/labctl/.github/actions/validate-catalog@v1`): installs labctl
  (`go install …@<version>`, default `latest`) and runs `labctl catalog
  validate <path>` against it. `examples/catalog/` (singular — NOT
  `examples/catalogs/`, which `Load` would scan as an installed catalog) is the
  reference catalog both this action and `internal/manifest/example_catalog_test.go`
  exercise in CI, with its own authoring/publishing README.

MCP Apps result View, Phase 1+2 (done, read tools only): every read tool
(`!Write`, including the generic `<svc>_get` verb) carries
`_meta.ui.resourceUri = "ui://labctl/result"`, an MCP Apps link to one
universal table/record/tree HTML View registered ONCE on the server
(`internal/mcpserver.BuildServer`) — zero per-service Go. The View itself is a
single built HTML file (`internal/mcpserver/views/result.html`, built from the
separate `views/` TS/Vite project and committed so plain `go build` needs no
npm) `//go:embed`'d via `internal/mcpserver/views`, with `LABCTL_VIEWS_DIR`
overriding it from disk for the dev loop (mirrors `LABCTL_CONFIG_DIR`). A read
tool's `executeAndRender` populates `CallToolResult.StructuredContent`
ADDITIVELY (the existing `TextContent` is unchanged — the fallback for
non-Apps hosts and the headless/ContextForge agent path) with an object-root
wrapper: `{"result": <jq-filtered value>, "labctl": {"service", "command",
"title", "ui"}}`; `result` is computed by `output.Filtered`, which mirrors
`output.Render`'s decode+jq path exactly so the structured value always
matches the text. Write tools and dry-run never get the `_meta.ui` link or
StructuredContent. A command can shape its own rendering with an optional
`ui:` hint block (`manifest.UI`, sibling of `output:`) — `view`
(table|record|tree), `columns`, `primary`, `badges`, `sort`, `drilldown` — DATA
only (no HTML/URLs/secrets), so it stays portable and never trips
`validateNoInManifestBinding`; absent, the View auto-detects by result shape.
A write-confirmation View is a separate, later PR.

## Conventions

- stdout = data, stderr = diagnostics, real exit codes (0 ok, 2 usage, 3 auth,
  4 HTTP≥400, 5 network, 6 decode).
- Secrets are refs (`op://...`) resolved at call time — never values in manifests,
  never in argv, redacted in verbose/dry-run output.
- Services resolve from **three sources, highest wins**: a local
  `<config-dir>/services/<name>.yaml` > an installed named catalog
  (`<config-dir>/catalogs/*/`) > the embedded catalog (the top-level `catalog`
  package, the 15 built-in portable manifests). `list` marks each `local`,
  `override` (a local file shadowing embedded/an installed catalog), `catalog:<name>`
  (from an installed catalog), or `embedded`. Two *local* files with one name is
  still a duplicate error. Two *installed catalogs* defining one name is **not**
  an error — both stay addressable as `<catalog>:<service>`; the bare name is
  ambiguous and errors (listing both qualified forms) until you qualify it.
  Absent any local `services/` or `catalogs/`, all 15 come from the embedded
  catalog.
- A manifest is **portable** (what a service *is*); user-specific endpoints and
  credentials (`base_url`, secret `ref`s, per-machine endpoint/var/tls overrides)
  live in a `profile.yaml` at the config root, which is the **sole** binding
  mechanism. Precedence is **env override > profile**. A manifest may **not**
  carry a `base_url` (service or endpoint) or a secret `ref` — structural
  `Validate` rejects it (`*ConfigError` → exit 2, message points at the
  `profile.yaml` slot); an in-manifest secret `env:` stays allowed (a
  CI/devcontainer override). Structural `Validate` (well-formed, runs on the RAW
  pre-merge manifest) is split from `ValidateComplete` (post-merge: resolvable
  base_url + every secret bound); completeness is enforced post-merge at execute
  time and surfaced by `doctor` / `lint --strict`. Portable + `profile.yaml` is
  the form the shipped `examples/` use.
- New auth strategy / transport / pagination style → wire it in its package + add
  a test; keep the manifest schema additive.
- Release: opt-in `semver:*` label on the merged PR (no label → no release);
  shared `ai-release.yml@v1`; ships cross-compiled static binaries.
