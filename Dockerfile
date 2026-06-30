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

FROM debian:trixie-slim
ARG OP_VERSION=2.31.1
ARG TARGETARCH
# ca-certificates for HTTPS to service APIs + 1Password; op CLI for op:// refs.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl unzip; \
    update-ca-certificates; \
    curl -sSfLo /tmp/op.zip "https://cache.agilebits.com/dist/1P/op2/pkg/v${OP_VERSION}/op_linux_${TARGETARCH}_v${OP_VERSION}.zip"; \
    unzip -od /usr/local/bin /tmp/op.zip op; \
    chmod 0755 /usr/local/bin/op; \
    apt-get purge -y --auto-remove curl unzip; \
    rm -rf /tmp/op.zip /var/lib/apt/lists/*
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
