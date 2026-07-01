# labctl-mcp Helm chart

Deploys [`labctl`](https://github.com/jedwards1230/labctl)'s MCP server over
streamable-HTTP so it is network-reachable in a cluster (e.g. to be federated by
an MCP gateway).

The image bundles the 1Password `op` CLI; labctl resolves `op://` secret refs at
call time using an `OP_SERVICE_ACCOUNT_TOKEN`.

## Quick start

`labctl mcp --http` is secure by default: a non-loopback bind (this chart
always binds `:9000`, i.e. every interface) refuses to start without a bearer
token, so `mcp.auth.enabled` (with a token source) or `mcp.allowUnauthenticated`
must be set — the chart does not pick a default for you.

```bash
helm install labctl-mcp oci://ghcr.io/jedwards1230/charts/labctl-mcp \
  --set auth.existingSecret.name=labctl-op-token \
  --set mcp.auth.enabled=true \
  --set mcp.auth.existingSecret.name=labctl-mcp-auth-token \
  --set-file config.profileYaml=profile.yaml
```

## Key values

| Key | Default | Purpose |
|-----|---------|---------|
| `mcp.http` | `:9000` | listen address for `labctl mcp --http` |
| `mcp.readOnly` | `false` | `--read-only`: expose read tools only |
| `mcp.services` | `[]` | `--service` allowlist (empty = all) |
| `mcp.auth.enabled` | `false` | require `Authorization: Bearer <token>` on `/mcp` (transport-layer access control) |
| `mcp.auth.existingSecret.name` | `""` | secret holding the bearer token |
| `mcp.auth.onePasswordItem.itemPath` | `""` | render a OnePasswordItem CRD for the bearer token instead (1Password operator) |
| `mcp.allowUnauthenticated` | `false` | `--allow-unauthenticated`: explicit opt-out of the bearer-token requirement (use only when NetworkPolicy/an upstream gateway is the sole intended boundary) |
| `config.profileYaml` | `""` | per-env binding (base_url + op:// refs) → `/config/profile.yaml` |
| `config.configYaml` | `""` | optional `/config/config.yaml` |
| `config.servicesYaml` | `{}` | map of service-name → **unindented** manifest YAML; each entry mounts as `/config/services/<name>.yaml`, overriding the embedded catalog without a rebuild. Values must be unindented — the template indents them for the ConfigMap. Example: `servicesYaml: {radarr: "name: radarr\n..."}` |
| `auth.existingSecret.name` | `""` | secret holding the op service-account token |
| `auth.onePasswordItem.itemPath` | `""` | render a OnePasswordItem CRD instead (1Password operator) |
| `ingress.enabled` | `false` | expose via Ingress |
| `networkPolicy.enabled` | `false` | render a NetworkPolicy selecting the MCP pod |
| `networkPolicy.ingress.from` | `[]` | NetworkPolicyPeer entries allowed to reach the MCP port (empty = default-deny ingress) |
| `networkPolicy.egress.enabled` | `false` | also restrict egress (adds `Egress` to `policyTypes`) |
| `networkPolicy.egress.rules` | `[]` | raw egress rule objects (empty while enabled = default-deny egress) |

Service manifests are embedded in the labctl binary — only `profile.yaml`
(and optional `config.yaml`) need supplying.

## Access boundaries

Two independent, composable boundaries guard the endpoint:

1. **NetworkPolicy** (`networkPolicy.enabled`, default off) — restricts *which
   peers* can reach the port at the network layer (see below).
2. **Bearer-token auth** (`mcp.auth.enabled`) — an app-layer check that requires
   `Authorization: Bearer <token>` on `/mcp` (see [Endpoint authentication](#endpoint-authentication-bearer-token)).

Bearer-token auth is **required, not opt-in**, because this chart's `mcp.http`
is a bare-port bind (non-loopback): `labctl mcp` refuses to start unless
`mcp.auth.enabled` (with a token source) is set, or `mcp.allowUnauthenticated`
explicitly accepts an unauthenticated deploy (e.g. when NetworkPolicy or an
upstream gateway is the sole intended boundary). NetworkPolicy stays opt-in
and composes with either choice.

These are transport-/network-layer access control (*who may reach the endpoint at
all*), not per-tool policy gating — labctl stays an unopinionated executor.
`GET /healthz` is always unauthenticated so liveness/readiness probes work.

### NetworkPolicy

In any cluster that supports NetworkPolicy, enable
`networkPolicy.enabled` and list only the peers that should reach it (e.g. an
MCP gateway). With `networkPolicy.enabled=true` and an empty
`networkPolicy.ingress.from`, the pod is default-deny ingress (no source may
reach it). Example allowing a ContextForge gateway pod:

```yaml
networkPolicy:
  enabled: true
  ingress:
    from:
      - podSelector:
          matchLabels:
            app.kubernetes.io/name: contextforge
```

The pod also runs with `seccompProfile: RuntimeDefault` at both the pod and
container level, alongside the existing non-root / read-only-rootfs /
drop-ALL-capabilities hardening.

### Restricting egress

Egress is **unrestricted by default**. Because the pod holds an
`OP_SERVICE_ACCOUNT_TOKEN`, restricting where it can connect limits the
exfiltration path if it's ever compromised. Enable `networkPolicy.egress.enabled`
and supply raw Kubernetes NetworkPolicy egress rules (objects with `to:` and
`ports:` keys) for exactly what it needs — typically DNS, your LAN/service CIDR,
and 443 out to 1Password (so `op` can resolve refs). With
`networkPolicy.egress.enabled=true` and empty `egress.rules`, the pod is
default-deny egress.

```yaml
networkPolicy:
  enabled: true
  egress:
    enabled: true
    rules:
      # DNS to the cluster resolver
      - to:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: kube-system
        ports:
          - { port: 53, protocol: UDP }
          - { port: 53, protocol: TCP }
      # LAN service hosts
      - to:
          - ipBlock: { cidr: 192.168.8.0/24 }
      # 443 out (1Password.com for op:// resolution)
      - to:
          - ipBlock: { cidr: 0.0.0.0/0, except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16] }
        ports:
          - { port: 443, protocol: TCP }
```

## Endpoint authentication (bearer token)

`mcp.auth.enabled` adds an app-layer boundary: every request to `/mcp`
must carry `Authorization: Bearer <token>`; missing/invalid tokens get `401`
(compared in constant time). `GET /healthz` is never authenticated. Since
`mcp.http` is a non-loopback bind, `labctl mcp` itself now requires this (or
`mcp.allowUnauthenticated`) to start — leaving `mcp.auth.enabled` at its
default `false` without also setting `mcp.allowUnauthenticated` makes the
container CrashLoop with an actionable error, not a silently-unauthenticated
deploy.

Supply the token from a Secret:

```yaml
mcp:
  auth:
    enabled: true
    existingSecret:
      name: labctl-mcp-bearer   # key "token" by default
```

…or have the 1Password operator mint it (renders a OnePasswordItem CRD named
`<release>-labctl-mcp-mcp-auth-token`):

```yaml
mcp:
  auth:
    enabled: true
    onePasswordItem:
      itemPath: vaults/homelab/items/k8s-mcp-gateway-labctl-mcp-auth
```

Enabling `mcp.auth.enabled` without a token source is a hard render error
(fail-closed) rather than a silently-unauthenticated deploy. The token is
injected as the `LABCTL_MCP_AUTH_TOKEN` env var — never passed on argv.

## Federating into ContextForge

Register a gateway with `transport: STREAMABLEHTTP` pointing at
`http://<release>-labctl-mcp.<ns>.svc:9000/mcp`. When `mcp.auth.enabled` is set,
configure the gateway with the matching bearer token (a `Authorization: Bearer
<token>` header) so it can reach the endpoint.
