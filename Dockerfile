# syntax=docker/dockerfile:1
#
# labctl runtime image. Bundles the 1Password `op` CLI because labctl resolves
# `op://` secret refs by shelling out to `op` (with OP_SERVICE_ACCOUNT_TOKEN).
# Built multi-arch (linux/amd64, linux/arm64) by the release workflow.
#
# Default entrypoint serves the MCP server over streamable-HTTP on :9000 so the
# container is network-reachable (e.g. federated by an MCP gateway). Override
# CMD for stdio or other subcommands.

FROM golang:1.25-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/labctl .

# Pinned to a digest (not the mutable trixie-slim tag) so rebuilds are
# reproducible and don't silently drift onto a different base.
FROM debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2
# Install the 1Password CLI (op) from 1Password's official GPG-signed APT repo.
# The signed repo gives supply-chain integrity verification (CWE-494) without
# pinning brittle per-arch checksums that break on every op bump. arch is
# derived per build platform, so this works under buildx for amd64 + arm64.
# ca-certificates stays for runtime HTTPS to service APIs + 1Password.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg; \
    update-ca-certificates; \
    arch="$(dpkg --print-architecture)"; \
    curl -sSf https://downloads.1password.com/linux/keys/1password.asc \
      | gpg --dearmor --output /usr/share/keyrings/1password-archive-keyring.gpg; \
    printf 'deb [arch=%s signed-by=/usr/share/keyrings/1password-archive-keyring.gpg] https://downloads.1password.com/linux/debian/%s stable main\n' "$arch" "$arch" \
      > /etc/apt/sources.list.d/1password.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends 1password-cli; \
    op --version; \
    apt-get purge -y --auto-remove curl gnupg; \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/labctl /usr/local/bin/labctl
# labctl reads config.yaml + profile.yaml from LABCTL_CONFIG_DIR; service
# manifests are embedded in the binary. HOME holds op/oauth2 caches (kept
# separate from the read-only config mount so a read-only root fs still works).
ENV LABCTL_CONFIG_DIR=/config \
    HOME=/home/labctl
RUN useradd -u 10001 -r -m -d /home/labctl -s /usr/sbin/nologin labctl \
    && mkdir -p /config && chown 10001 /config
USER 10001
WORKDIR /home/labctl
EXPOSE 9000
ENTRYPOINT ["labctl"]
CMD ["mcp", "--http", ":9000"]
