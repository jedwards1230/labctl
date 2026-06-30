# Example catalog

A minimal, two-service reference catalog demonstrating the shape a third-party
`labctl` catalog repo should ship: one no-auth service
([`uptime.yaml`](uptime.yaml)) and one header-key service
([`inventory.yaml`](inventory.yaml)). Both are placeholders (`example.com`) and
both pass `labctl catalog validate examples/catalog`.

This directory is deliberately **not** `examples/catalogs/` (plural) — that
path is reserved for an *installed* catalog under a config dir. This is just a
reference checked by CI (`internal/manifest/example_catalog_test.go` and the
`validate-catalog-action` job in `.github/workflows/ci.yml`); it is never
auto-loaded by `labctl`.

## Writing a portable manifest

A catalog manifest declares *what* a service is — its commands, auth strategy,
and secret slots — and nothing machine-specific:

```yaml
name: inventory
description: example header-key service — an inventory/warehouse API
env_prefix: INVENTORY

auth:
  strategy: header-key
  header: X-Api-Key
  value: "{secret.api_key}"

secrets:
  api_key:
    env: INVENTORY_API_KEY   # declared here; bound in the CONSUMER's profile.yaml

commands:
  items:
    help: list inventory items
    method: GET
    path: /items
```

**A manifest must NOT carry a `base_url` (service or endpoint) or a secret
`ref`.** Those are machine-specific bindings that live only in the *consumer's*
`profile.yaml` — never in the catalog. `labctl catalog validate` /
`catalog add` enforce this (structural `Validate`, the same gate either way)
and reject anything that carries one. An in-manifest secret `env:` (like
`INVENTORY_API_KEY` above) is fine — it just declares where an env-override
*could* supply the secret; it still resolves from the consumer's environment,
never a value baked into the manifest.

See [`labctl schema`](../../README.md#manifest-json-schema-editor-support) for
the full JSON Schema, and `labctl catalog show <name>` against the embedded
catalog for fuller real-world examples (header-key, bearer, basic auth; named
commands; pagination; multi-endpoint).

## Validating before you publish

```sh
labctl catalog validate .   # run from this directory, or pass any catalog dir
```

Read-only: no network call, no config dir, no install. Exits 0 only if every
top-level `*.yaml`/`*.yml` is a valid, portable manifest and no two manifests
share a service name.

### CI: the validate-catalog action

Wire the bundled composite action into your catalog repo's own CI so a broken
manifest fails the PR instead of breaking a consumer's `catalog add`:

```yaml
# .github/workflows/validate.yml
on: [push, pull_request]
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: jedwards1230/labctl/.github/actions/validate-catalog@v1
        with:
          path: .          # default "."; the dir holding your *.yaml manifests
          version: latest  # default "latest"; pin to a labctl release if you need stability
```

## How a consumer installs and uses it

```sh
labctl catalog add https://github.com/you/your-catalog.git --name yours
labctl catalog add ./local-checkout --name yours          # …or a local dir
labctl catalog installed                                  # confirm it's there

labctl svc inventory items                                # address by bare name
labctl svc yours:inventory items                           # …or the qualified <catalog>:<service> form
```

The qualified `<catalog>:<service>` selector always works. The bare name only
works while it's unambiguous — if a consumer has another installed catalog (or
the embedded catalog) that also defines a service named `inventory`, the bare
name errors and lists both qualified forms instead of silently picking one.
Resolution precedence (highest wins): a consumer's local `services/<name>.yaml`
\> any installed catalog \> the embedded catalog.

An installed catalog is **inert** until the consumer's `profile.yaml` binds a
`base_url` and the declared secrets — installing it only makes the manifests
*available*, the same `labctl` [unopinionated executor](../../README.md#how-it-works)
principle as everywhere else.
