# labctl

A single, manifest-driven CLI for HTTP/RPC service APIs. A service is one YAML
file; the binary knows nothing service-specific. Adding or removing a service is
a manifest edit, never a recompile.

`labctl` replaces a pile of bespoke per-service `curl`/`jq`/auth/pagination shell
wrappers with one static Go binary a human runs at a shell, an agent calls over
the CLI, and (soon) an agent calls over MCP — all from the same config.

## Install

```sh
go install github.com/jedwards1230/labctl@latest
```

Or grab a static binary from the releases page.

## Quick start

`labctl` reads manifests from `$XDG_CONFIG_HOME/labctl` (or `~/.config/labctl`):

```
~/.config/labctl/
├── config.yaml            # global defaults + secret resolver
└── services/
    ├── radarr.yaml
    └── tdarr.yaml
```

A minimal connection-only manifest is usable immediately via generic verbs:

```yaml
# services/tdarr.yaml
name: tdarr
base_url: https://tdarr.lilbro.cloud
auth: { strategy: none }
```

```sh
labctl list                       # all configured services
labctl tdarr get /api/v2/status   # generic verb passthrough
labctl tdarr status               # a named command, if the manifest defines one
labctl radarr list --filter 'length'
labctl radarr list --dry-run      # print the resolved request, send nothing
labctl doctor                     # probe each service's reachability
labctl lint                       # validate every manifest
```

See [`examples/`](examples/) for fuller manifests (header-key, bearer, basic auth;
named commands; pagination; multi-endpoint).

## How it works

- **Two command producers, one model.** Hand-written `commands:` today; OpenAPI
  inference (`spec:`) is on the roadmap. Both emit one format-neutral command the
  executor runs, so the CLI and the (planned) MCP server behave identically.
- **Secrets are references, resolved at call time.** A manifest stores
  `op://vault/item/field`, never a value. An env override
  (`<PREFIX>_<SECRET>`) skips the resolver for ephemeral devcontainers/CI.
- **Unopinionated executor.** The binary gates nothing — no `--read-only`, no
  write-confirm. It does exactly what the manifest says. Guardrails belong in the
  consuming layer (e.g. an agent-host pre-call hook), not baked into the tool.
- **Unix-native.** stdout is data, stderr is diagnostics, exit codes are real,
  secrets never appear in argv, manifests are re-read just-in-time per call.

## Status

Phase 1: `http` transport; `none`/`header-key`/`bearer`/`basic` auth; the `op`
external-tool secret resolver with env override; generic verbs; gojq filtering
(json/raw/scalar). Roadmap: OpenAPI inference, `jsonrpc-ws`, composed pipelines,
and a stdio MCP server.

## License

MIT. Studies patterns from [`rest-sh/restish`](https://github.com/rest-sh/restish)
(MIT) — see [NOTICE](NOTICE).
