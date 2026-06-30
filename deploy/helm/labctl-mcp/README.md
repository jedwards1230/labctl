# labctl-mcp Helm chart

Deploys [`labctl`](https://github.com/jedwards1230/labctl)'s MCP server over
streamable-HTTP so it is network-reachable in a cluster (e.g. to be federated by
an MCP gateway).

The image bundles the 1Password `op` CLI; labctl resolves `op://` secret refs at
call time using an `OP_SERVICE_ACCOUNT_TOKEN`.

## Quick start

```bash
helm install labctl-mcp oci://ghcr.io/jedwards1230/charts/labctl-mcp \
  --set auth.existingSecret.name=labctl-op-token \
  --set-file config.profileYaml=profile.yaml
```

## Key values

| Key | Default | Purpose |
|-----|---------|---------|
| `mcp.http` | `:9000` | listen address for `labctl mcp --http` |
| `mcp.readOnly` | `false` | `--read-only`: expose read tools only |
| `mcp.services` | `[]` | `--service` allowlist (empty = all) |
| `config.profileYaml` | `""` | per-env binding (base_url + op:// refs) → `/config/profile.yaml` |
| `config.configYaml` | `""` | optional `/config/config.yaml` |
| `auth.existingSecret.name` | `""` | secret holding the op service-account token |
| `auth.onePasswordItem.itemPath` | `""` | render a OnePasswordItem CRD instead (1Password operator) |
| `ingress.enabled` | `false` | expose via Ingress |
| `networkPolicy.enabled` | `false` | render a NetworkPolicy selecting the MCP pod |
| `networkPolicy.ingress.from` | `[]` | NetworkPolicyPeer entries allowed to reach the MCP port (empty = default-deny ingress) |
| `networkPolicy.egress.enabled` | `false` | also restrict egress (adds `Egress` to `policyTypes`) |
| `networkPolicy.egress.rules` | `[]` | raw egress rule objects (empty while enabled = default-deny egress) |

Service manifests are embedded in the labctl binary — only `profile.yaml`
(and optional `config.yaml`) need supplying.

## Network reachability is the access boundary

The `/mcp` endpoint has **no application-layer auth** — whoever can reach the
port can call every tool. In any cluster that supports NetworkPolicy, enable
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

## Federating into ContextForge

Register a gateway with `transport: STREAMABLEHTTP` pointing at
`http://<release>-labctl-mcp.<ns>.svc:9000/mcp`.
