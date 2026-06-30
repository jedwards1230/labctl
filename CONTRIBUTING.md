# Contributing to labctl

`labctl` is a manifest-driven Go CLI for homelab service APIs. All changes go through the workflow below.

## Prerequisites

- Go 1.25 or later (see `go.mod` for the exact floor version)

## Build, test & lint

```bash
# Build
go build -o labctl .

# Format check (must be clean before committing)
gofmt -l .

# Vet
go vet ./...

# Lint (required by CI)
golangci-lint run

# Test with race detector (CI requires ≥75% coverage)
go test -race ./...

# Module hygiene (go.mod and go.sum must stay tidy)
go mod tidy && git diff --exit-code go.mod go.sum

# Full build smoke test
go build ./...

# Try against the example manifests without installing
LABCTL_CONFIG_DIR="$PWD/examples" ./labctl list
LABCTL_CONFIG_DIR="$PWD/examples" ./labctl lint
LABCTL_CONFIG_DIR="$PWD/examples" ./labctl --dry-run svc radarr list
```

CI runs `gofmt`, `go vet`, `golangci-lint`, `go mod tidy` check, `go test -race` (with a 75% coverage floor), and `go build`. All checks must pass before a PR can merge.

## Changing an embedded manifest

The embedded catalog lives in `catalog/`. Editing a manifest is **rebuild-free** — the authoring loop assumes one terminal session where `LABCTL_CONFIG_DIR` stays exported throughout:

1. **Seed a local override**:
   ```bash
   export LABCTL_CONFIG_DIR=$(mktemp -d)
   labctl catalog edit <name>          # copies the full manifest into $LABCTL_CONFIG_DIR/services/<name>.yaml
   ```
2. **Edit and test**:
   ```bash
   $EDITOR "$LABCTL_CONFIG_DIR/services/<name>.yaml"
   labctl svc <name> <command> --dry-run   # preview the resolved request without sending
   ```
3. **Promote back into the catalog**:
   ```bash
   labctl catalog vendor <name> --catalog-dir catalog   # run from the repo root
   ```
4. **Validate**:
   ```bash
   labctl lint catalog/<name>.yaml
   ```
5. **Commit and open a PR**:
   ```bash
   git add catalog/<name>.yaml
   git commit -m "fix(catalog): update <name> manifest"
   ```

## Documentation

Keep documentation current as part of the change, not as a follow-up — update the README and any affected docs in the same PR.

## Before you open a PR

- Make sure all CI checks pass locally first — run the formatter, linter, and tests.

## Branching & commits

- Branch off `main`; never commit directly to `main`.
- Use [Conventional Commits](https://www.conventionalcommits.org/) prefixes (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, …).
- Sign your commits where possible (`git commit -S`).
- Keep each PR focused; delete dead code rather than commenting it out.

## Pull requests

- Open the PR against `main`.
- Every PR runs CI (required check: **Test & Lint**). Resolve **all** review threads before the PR is merged.
- An automated code review runs on each PR; address and resolve its threads like any other review.
- A PR can be merged once CI is green and all review threads are resolved.

## Releases

Releases are opt-in. Before merging, add one of `semver:patch`, `semver:minor`, or `semver:major` to the PR to cut a release on merge; with no label, merging does not release. A release publishes a single immutable `vX.Y.Z` tag with AI-generated release notes and cross-compiled static binaries for Linux and macOS (amd64 + arm64).
