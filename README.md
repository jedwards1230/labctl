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
├── services/              # optional: local overrides or new services
└── catalogs/              # optional: installed named catalogs (catalog add)
    └── <name>/            #   one bundle of portable *.yaml manifests + .labctl-catalog.json
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
labctl list                           # all services with their origin (embedded / local / override)
labctl catalog list                   # embedded catalog only (no local/override markers)
labctl catalog show radarr            # dump an embedded manifest to stdout
labctl catalog edit radarr            # seed it into services/ for live editing (no rebuild)
labctl catalog vendor radarr --catalog-dir ./catalog   # promote an edited override into a repo checkout
labctl catalog add ./my-manifests     # install a named catalog (dir or git source)
labctl catalog add ./openapi.json --openapi   # materialize a manifest from an OpenAPI 3.x document
labctl catalog installed              # list installed named catalogs
labctl catalog validate ./my-manifests   # read-only: check a dir's manifests against the same gate (no install)
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

### Named, installable catalogs

The embedded catalog is the floor every install gets for free. Beyond it you can
**install your own named catalogs** — bundles of portable manifests fetched from a
directory or a git repo — into `<config-dir>/catalogs/<name>/`:

```sh
labctl catalog add ./my-manifests                       # install a local dir as a catalog (name = dir basename)
labctl catalog add https://git.example/team/labctl-catalog.git   # …or a git repo (name = repo basename)
labctl catalog add git@host:team/cat.git --name team --ref v1.2  # scp-style remote, pinned to a ref
labctl catalog add https://api.example.com/openapi.json --openapi   # …or materialize a manifest from an OpenAPI 3.x document
labctl catalog installed                                # list installed catalogs (name, type, commit/ref, source)
labctl catalog update [name]                            # re-fetch one (or all) from the recorded source
labctl catalog remove <name>                            # uninstall a catalog
```

`--openapi` treats `<source>` (an `http(s)://` URL or a local file) as an
OpenAPI 3.x document: its operations become `commands:`, and each
`securitySchemes` entry is inferred into an `auth:` block on a best-effort
basis — anything that can't be faithfully mapped (e.g. OAuth2 flows) falls back
to `auth: { strategy: none }` with a comment explaining what to wire by hand.
The spec is parsed once at add-time and **not** vendored — no `spec:` reference
is kept, so the installed manifest stands alone and stays portable. `--ref`
doesn't apply to an `--openapi` source (it's git-only); the catalog name
defaults to the document's `info.title`, slugified.

**Resolution precedence (highest wins):** a local `services/<name>.yaml`  >  an
installed catalog  >  the embedded catalog. `labctl list` marks an
installed-catalog service `catalog:<name>`; a local file shadowing one shows as
`override`, and the embedded floor stays `embedded`.

**Two installed catalogs may define the same service name.** Unlike a local
override, this is no longer a hard load error — both install, and each stays
addressable via its qualified `<catalog>:<service>` selector
(`labctl svc <catalog>:<service> <command>`). The *bare* name becomes ambiguous:
`labctl svc <name>` (and `lint`/`doctor <name>`) error and list both qualified
forms instead of silently picking one. **This means installing a second catalog
that collides with an existing service name renames the first catalog's MCP
tools** — once ambiguous, the MCP server can only expose the qualified form
(`<catalog>-<service>_<command>`, since a bare tool name would also be
ambiguous there), so any agent/automation pinned to the old unqualified tool
name needs updating. Worth checking `labctl list` after adding a catalog if you
run the MCP server.

**Security framing.** A catalog manifest carries **no endpoints or credentials**:
`catalog add` validates every `*.yaml` against the manifest schema *and* the
portability rule (no `base_url`, no secret `ref`) before writing anything — one
bad manifest rejects the whole add, and the same rule is re-enforced at load. So
an installed catalog is **inert** until your `profile.yaml` binds it to a machine.
That portability boundary is why catalogs need **no signing**: there is nothing
executable or secret to trust — only the shape of an API. A git source is pinned
to its resolved **commit SHA** (recorded in `.labctl-catalog.json`), so an install
is reproducible and `catalog update` re-pins it. (Git fetches go through the
system `git` with `ext`/`fd` transport helpers blocked and the URL passed as a
single argument after `--`, never a shell.)

Installing a catalog only makes more manifests *available* — there is no
execution-time policy gating; labctl stays an [unopinionated
executor](#how-it-works).

### Publishing a community catalog

Any git repo (or directory) of portable manifests is a valid `catalog add`
source — there's no registry or signing step. To check your manifests against
labctl's contract before anyone installs them:

```sh
labctl catalog validate ./my-manifests   # read-only: schema + portability + duplicate-name check, no install
```

It's the exact fail-closed gate `catalog add` runs (`manifest.ValidatePortableManifest`),
exposed standalone with no config dir, network call, or install side effect —
prints one `ok <file>` / `FAIL <file>: <reason>` line per manifest and exits 2
if any fail. Wire it into your catalog repo's own CI with the bundled composite
action:

```yaml
# .github/workflows/validate.yml in YOUR catalog repo
on: [push, pull_request]
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: jedwards1230/labctl/.github/actions/validate-catalog@v1
        with:
          path: .   # the dir holding your *.yaml manifests
```

[`examples/catalog/`](examples/catalog/) is a minimal reference catalog (one
no-auth service, one header-key service) that passes `catalog validate` and
demonstrates the shape — see its [README](examples/catalog/README.md) for the
authoring/publishing walkthrough.

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

### Manifest JSON Schema (editor support)

`labctl schema` prints a hand-authored JSON Schema (draft-07) for the **portable
manifest** shape. Pipe it next to your manifests and point your editor's
yaml-language-server at it for completion + inline validation while authoring:

```bash
labctl schema > manifest.schema.json
```

```yaml
# yaml-language-server: $schema=./manifest.schema.json
name: radarr
# …
```

You can point the modeline at the raw GitHub URL instead of a local file:
`# yaml-language-server: $schema=https://raw.githubusercontent.com/jedwards1230/labctl/main/schema/manifest.schema.json`.

The schema validates the portable shape (commands, auth strategy, secret slots);
it deliberately forbids `base_url` and secret `ref`s, which belong in
`profile.yaml`. It is an additive editor aid — the authoritative check stays
`labctl lint`, which also enforces rules JSON Schema can't express (undeclared
secret references, jq validity, spec reachability).

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
  The streamable-HTTP `/mcp` endpoint is unauthenticated by default — network
  reachability is the access boundary. Two opt-in boundaries restrict who can
  reach it (both default-off, so the default behavior is unchanged): an optional
  bearer token (`labctl mcp --http :9000 --auth-token-file <path>`, or the
  `LABCTL_MCP_AUTH_TOKEN` env var) that requires `Authorization: Bearer <token>`
  on `/mcp` (constant-time compared; `401` otherwise — `GET /healthz` stays open),
  and the [`labctl-mcp` chart](deploy/helm/labctl-mcp)'s opt-in NetworkPolicy
  (`networkPolicy.enabled`). Both are *transport-layer access control* (who may
  reach the endpoint at all), not per-tool policy gating — labctl stays an
  unopinionated executor.
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
catalog of 15 portable manifests plus installable named catalogs (`catalog
add/update/remove/installed/validate`; `catalog add --openapi` materializes a
manifest from an OpenAPI 3.x document; precedence local `services/` > installed
catalogs > embedded, with two installed catalogs free to share a service name —
resolved via the `<catalog>:<service>` selector, a bare colliding name errors
and lists both qualified forms); a `validate-catalog` composite GitHub Action
(`.github/actions/validate-catalog`) and a reference catalog
([`examples/catalog/`](examples/catalog/)) for third-party catalog authors;
optional OpenTelemetry tracing.

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
