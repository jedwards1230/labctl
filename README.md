# labctl

A single, manifest-driven CLI for HTTP/RPC service APIs. A service is one YAML
file; the binary knows nothing service-specific. Adding or removing a service is
a manifest edit, never a recompile.

`labctl` replaces a pile of bespoke per-service `curl`/`jq`/auth/pagination shell
wrappers with one static Go binary a human runs at a shell, an agent calls over
the CLI, and an agent calls over MCP — all from the same config.

## Install

```sh
go install github.com/jedwards1230/labctl@latest
```

Or grab a static binary from the releases page.

### Updating

Once installed, update in place to the latest release (downloads the matching
`labctl-{os}-{arch}` asset, verifies its sha256, and atomically replaces the
running binary):

```sh
labctl self-update            # update to the latest release
labctl self-update --check    # report current vs latest, download nothing
```

## Quick start

`labctl` reads manifests from `$XDG_CONFIG_HOME/labctl` (or `~/.config/labctl`):
Override the config dir with `LABCTL_CONFIG_DIR=<path>` or `--config-dir <path>`.

```
~/.config/labctl/
├── config.yaml            # global defaults + secret resolver
├── profile.yaml           # optional per-user binding (base_url + secret refs)
└── services/              # optional: local overrides or new services
```

Run `labctl init` (no argument) to provision this layout — it creates the dir,
`services/`, a default `config.yaml`, and a commented `profile.yaml`, leaving any
that already exist untouched.

After `init`, bind one service in `profile.yaml` and verify everything is wired up:

```sh
# Add a service binding to profile.yaml, then:
labctl lint --strict                  # confirm base_url + secrets are bound
labctl svc <name> status              # smoke-test the live endpoint
labctl svc <name> list --dry-run      # preview the resolved request without sending
```

**You don't need any `services/` files to start.** 15 portable manifests
(radarr, sonarr, prowlarr, bazarr, tdarr, n8n, authentik, harbor, abs, forgejo,
sunshine, truenas, ts, contextforge, cloudflare) are **embedded in the binary**.
They are the default catalog — `labctl catalog list` shows them, and a local
`services/<name>.yaml` of the same name *overrides* the embedded one. `list`
marks each service `embedded`, `local`, or `override`. Bind the catalog to your
machine with a `profile.yaml` (below); reach for a local `services/` file only to
add a new service or fork an embedded one — `labctl catalog edit <name>` seeds the
override for you (see [Editing a catalog manifest](#editing-a-catalog-manifest)).

A service ships as a **portable** manifest — it declares *what* the service is
(commands, auth strategy, secret slots), with no machine-specific endpoint or
credentials — and `profile.yaml` binds it to *this* machine:

```yaml
# services/radarr.yaml — portable: identical for every user
name: radarr
env_prefix: RADARR
auth: { strategy: header-key, header: X-Api-Key, value: "{secret.api_key}" }
secrets:
  api_key: { env: RADARR_API_KEY }   # slot declared; bound in profile.yaml
commands:
  list: { method: GET, path: /api/v3/movie }
```

```yaml
# profile.yaml — your machine: base_url + secret refs
version: 1
services:
  radarr:
    base_url: https://movies.example.com
    secrets:
      api_key: { ref: "op://vault/Radarr/api_key" }
```

> **Binding lives only in `profile.yaml`.** A manifest may **not** carry a
> `base_url` (service or endpoint) or a secret `ref` of its own — `labctl lint`
> rejects it (exit 2) and points you at the `profile.yaml` slot to use instead.
> A manifest is the portable shape; the profile (or a `<PREFIX>_URL` /
> `<PREFIX>_<SECRET>` env override) supplies every endpoint and credential.

Service commands live under `svc` (aliased `s`); built-ins stay at the top
level. Putting services in their own namespace means a user-defined service can
never collide with a built-in like `list` or `doctor`:

```sh
labctl list                           # all services (embedded + local + override), with source marker
labctl catalog list                   # embedded catalog only (no local/override markers)
labctl catalog show radarr            # dump an embedded manifest to stdout
labctl catalog edit radarr            # seed it into services/ for live editing (no rebuild)
labctl catalog vendor radarr --catalog-dir ./catalog   # promote an edited override into a repo checkout
labctl svc                            # same list as `list`, under the svc namespace
labctl svc tdarr get /api/v2/status   # generic verb passthrough
labctl svc tdarr status               # a named command, if the manifest defines one
labctl svc radarr list --filter 'length'
labctl svc radarr list --dry-run      # print the resolved request, send nothing
labctl s radarr list                  # `s` is shorthand for `svc`
labctl doctor                         # probe each service's reachability (built-in)
labctl lint                           # validate every manifest schema (built-in)
labctl lint --strict                  # also require completeness (bound base_url + secrets)
labctl init                           # provision the config dir (config.yaml + profile.yaml)
labctl init myservice                 # scaffold a portable starter manifest (built-in, stdout)
labctl init myservice --auth bearer -o services/myservice.yaml
```

The embedded catalog is the source of fuller manifests (header-key, bearer, basic
auth; named commands; pagination; multi-endpoint; jsonrpc-ws) — read any with
`labctl catalog show <name>`. [`examples/`](examples/) is a **profile-only** config
dir: no `services/`, just an `examples/profile.yaml` binding all 15 embedded
services to placeholder hosts (run `LABCTL_CONFIG_DIR=examples labctl lint --strict`).

### Editing a catalog manifest

The binary just ships **sane defaults** — a manifest is plain YAML, and editing
one is **rebuild-free**. The authoring loop:

```sh
labctl catalog edit authentik          # seed the FULL manifest into services/authentik.yaml
$EDITOR "$(labctl catalog edit authentik)"   # …or open it straight away (prints the path)
# iterate: edit services/authentik.yaml, re-run `labctl svc authentik …` — no recompile.
# the override shadows the embedded manifest by name at every load.

labctl catalog vendor authentik --catalog-dir ./catalog   # when it's right, promote it back
git add catalog/authentik.yaml && git commit                # …and it ships embedded next release
```

`catalog edit` copies the **complete** embedded manifest into
`<config-dir>/services/<name>.yaml`. It seeds a full copy, not a sparse patch,
because a local override **wholesale replaces** the embedded entry — it is
validated standalone with no field-level merge, so a partial override would drop
endpoints or fail validation. It refuses to clobber an in-progress override
without `--force`, prints the absolute path to stdout, and opens `$VISUAL`/`$EDITOR`
on the file with `--edit`.

`catalog vendor` is the maintainer half: it promotes an edited override back into a
labctl repo checkout's `catalog/` source tree (`--catalog-dir` points at it; the
running binary can't know the repo path). It **validates** the override first — a
portable manifest with no `base_url`/secret `ref` — so a broken manifest is never
promoted, and won't overwrite an existing `catalog/<name>.yaml` without `--force`.

### Portable manifests & profiles

This is the default workflow. `labctl init` provisions `config.yaml`, `services/`,
and a `profile.yaml`. A **portable** manifest — the embedded catalog's manifests,
or one you write in `services/` — declares *what* a service is (its commands, auth
strategy, secret slots) and is identical for every user; the `profile.yaml` at the
config root binds each service to *this* machine — `base_url`, secret `ref`s, and
any per-machine endpoint/var/`tls_insecure` overrides.

Precedence at resolution time is **env override > profile**. The profile (or an
env override) is the **sole** binding mechanism — a manifest carries no `base_url`
or secret `ref` of its own; `labctl lint` rejects one that does and names the
`profile.yaml` slot to use instead.

Structural validation (`labctl lint`) checks a manifest is well-formed; a portable
manifest passes it even unbound. **Completeness** (a resolvable `base_url` and a
bound `ref`/`env` for every declared secret) is enforced post-merge at execute
time, surfaced by `labctl lint --strict`, and reported by `labctl doctor` (which
prints `incomplete: …` for an unbound service instead of probing it).

### Secrets

`config.yaml` declares scheme-dispatched secret providers. A ref routes to a
provider by its URI scheme (`op://` → the `onepassword` provider):

```yaml
secrets:
  env_override: true            # allow <PREFIX>_<SECRET> env to skip resolution
  providers:
    onepassword:                # map key supplies the default scheme alias → op
      scheme: op
      command: ["op", "read", "{ref}"]   # {ref} ← the op:// URI
      auth:
        service_account_token:           # optional; omit to use the desktop op session
          file: ~/.config/labctl/sa-token  # exactly one of file | value | env
```

The legacy single-resolver `secret:` block is a still-supported deprecated alias
(normalized into an equivalent `op` provider), so older configs keep working.

When `auth.service_account_token` is set, the op provider injects
`OP_SERVICE_ACCOUNT_TOKEN` into the `op` subprocess only — never argv, never a
global export, never any log line. A ref dispatches by its URI scheme, so adding
a backend (e.g. `aws://`, `vault://`) is three edits in
[`internal/secret/provider.go`](internal/secret/provider.go) — no engine/CLI
changes.

## How it works

- **Two command producers, one model.** Hand-written `commands:` or OpenAPI
  inference (`spec:` via libopenapi). Both emit one format-neutral command the
  executor runs, so the CLI and the MCP server behave identically.
- **Transports.** `http` (default, curl-equivalent) and `jsonrpc-ws` (WebSocket
  JSON-RPC with ws-login auth — used by TrueNAS).
- **Auth strategies.** `none`, `header-key`, `bearer`, `basic`,
  `oauth2-client-credentials` (with on-disk token cache), and `ws-login`.
- **Composed pipelines.** A command can declare `steps:` instead of a single
  path — each step issues a request, extracts variables, and feeds the next. Use
  `when:`, `confirm:`, `on_error:`, and `body_transform:` for control flow.
- **MCP server.** `labctl mcp` exposes every non-ignored command as a tool over
  either stdio MCP (default) or streamable-HTTP (`labctl mcp --http :9000`, MCP
  endpoint at `/mcp`, with a `GET /healthz` liveness probe for in-cluster
  deployment behind an MCP gateway). It also exposes the generic verbs as
  per-service tools — `<svc>_get/_post/_put/_patch/_delete` for HTTP services and
  `<svc>_call` for jsonrpc-ws — so an agent has labctl's full write surface, not
  just the named reads (`--read-only` drops the write verbs). Same executor as
  the CLI on both transports.
  The streamable-HTTP `/mcp` endpoint has **no app-layer auth** — network
  reachability is the access boundary, so the [`labctl-mcp` chart](deploy/helm/labctl-mcp)
  ships an opt-in NetworkPolicy (`networkPolicy.enabled`) to restrict who can
  reach the port.
- **Secrets are references, resolved at call time.** A manifest stores
  `op://vault/item/field`, never a value. A ref is routed to a provider by its
  URI scheme (`op://` → the 1Password provider; the seam is open for `aws://`,
  `vault://`, … — see [`internal/secret/provider.go`](internal/secret/provider.go)).
  The op provider can inject an `OP_SERVICE_ACCOUNT_TOKEN` into its own
  subprocess (never a global export); omit `auth` to use the personal/desktop op
  session. An env override (`<PREFIX>_<SECRET>`) skips resolution entirely for
  ephemeral devcontainers/CI.
- **Unopinionated executor.** The binary gates nothing — no `--read-only`, no MCP
  write-gating — **except** a step a manifest explicitly marks `confirm:`, which
  aborts unless `--yes/-y` clears it (manifest-opt-in, fail-closed; no interactive
  prompt). It otherwise does exactly what the manifest says. Guardrails belong in
  the consuming layer (e.g. an agent-host pre-call hook), not baked into the tool.
- **Unix-native.** stdout is data, stderr is diagnostics, exit codes are real,
  secrets never appear in argv, manifests are re-read just-in-time per call.

## Observability (OpenTelemetry)

Tracing is **off by default** and adds zero cost unless the standard `OTEL_*`
env configures an OTLP endpoint. When set, each invocation emits one span
(`<service> <command>` with service/command/method/status/duration attributes),
so back-to-back and parallel-agent calls are traceable in Tempo/Jaeger:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf   # or grpc (default per spec: http/protobuf)
labctl svc radarr list
```

Export is fail-open and flush is time-bounded — a slow or down collector never
hangs or breaks a command. stdout stays clean (diagnostics go to stderr).

**Transport security**: span data leaves the process over whatever the standard
`OTEL_*` env points at, so prefer an HTTPS (or TLS gRPC) collector endpoint —
plain `http://` sends spans in cleartext and suits only a trusted local network.
Never put credentials in `OTEL_EXPORTER_OTLP_HEADERS` (it transits to the
collector as-is); use TLS client certs or your collector's standard auth instead.

## Status

Shipped: `http` and `jsonrpc-ws` transports; `none`/`header-key`/`bearer`/`basic`/
`oauth2-client-credentials`/`ws-login` auth; scheme-dispatched secrets providers
(1Password today, with optional service-account-token injection) and env
override; OpenAPI inference (`spec:`); composed `steps:` pipelines; an embedded
catalog of 15 portable manifests (local `services/` overrides by name); optional
OpenTelemetry tracing.

`labctl init <service>` scaffolds a commented starter manifest (pick the auth
stanza with `--auth`, write a file with `-o`) that validates against `labctl
lint`. The MCP server (`labctl mcp`, stdio by default or streamable-HTTP via
`--http :9000` with the endpoint at `/mcp`) annotates every tool with the
read-only / destructive / idempotent / open-world hints derived from the
command, and accepts `--read-only` (omit write tools) and `--service` (restrict
to named services); both filters compose and apply to either transport.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, build and test commands, branching conventions, and release process.

## License

MIT. Studies patterns from [`rest-sh/restish`](https://github.com/rest-sh/restish)
(MIT) — see [NOTICE](NOTICE).
