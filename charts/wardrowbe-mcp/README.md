# wardrowbe-mcp Helm chart

Deploys [`wardrowbe-mcp`](https://github.com/jansitarski/wardrowbe-mcp) — the
Wardrowbe wardrobe API exposed as MCP tools for Claude — into a Kubernetes
cluster.

The chart is published as an OCI artifact alongside each release image, so the
chart version always matches an image that exists.

## Install

```bash
helm install wardrowbe-mcp \
  oci://ghcr.io/jansitarski/charts/wardrowbe-mcp --version 1.0.0 \
  --namespace wardrowbe --create-namespace \
  --set config.wardrowbeUrl=http://backend.wardrowbe.svc.cluster.local:8000 \
  --set apiKey.value="$MCP_API_KEY"
```

Or with a values file:

```bash
helm install wardrowbe-mcp \
  oci://ghcr.io/jansitarski/charts/wardrowbe-mcp --version 1.0.0 \
  -n wardrowbe --create-namespace -f my-values.yaml
```

The chart's `appVersion` pins the image tag by default, so the chart version and
the running binary stay in lock-step. The GHCR package is private by default —
set `imagePullSecrets` to a Secret that can pull it.

## Minimal `values.yaml`

```yaml
config:
  wardrowbeUrl: http://backend.wardrowbe.svc.cluster.local:8000
  auth: dev
  externalId: you-example-com
  externalEmail: you@example.com
apiKey:
  # Prefer an existing (e.g. SOPS-managed) Secret over an inline value:
  existingSecret: wardrowbe-mcp-secrets
imagePullSecrets:
  - name: ghcr-pull
nodeSelector:
  kubernetes.io/arch: amd64
```

## The API key

The http transport requires an incoming bearer key (`MCP_API_KEY`). Provide it
one of two ways:

- **`apiKey.value`** — the chart creates a Secret holding it. Convenient for
  `--set`, but keep it out of committed values files.
- **`apiKey.existingSecret`** — reference a Secret you manage yourself (SOPS,
  Sealed Secrets, External Secrets, …). The key defaults to `mcp-api-key`
  (`apiKey.key`). This is the recommended path for GitOps.

## OIDC auth

Set `config.auth=oidc` to send a real per-user identity (instead of the fixed
dev identity) to the backend. The issuer URL and client ID are non-secret and are
passed as flags (`oidc.issuerUrl`, `oidc.clientId`). The **client secret and
refresh token are secret**, so the chart wires them from a Secret as environment
variables — never as pod args, which are visible via `kubectl get pod -o yaml`,
in etcd, and in audit logs:

```yaml
config:
  auth: oidc
oidc:
  issuerUrl: https://issuer.example.com
  clientId: my-client-id
  existingSecret: wardrowbe-mcp-oidc   # holds the secret material
  # clientSecretKey / refreshTokenKey default to oidc-client-secret / oidc-refresh-token
```

Create the Secret out-of-band (SOPS / Sealed Secrets / External Secrets):

```bash
kubectl -n wardrowbe create secret generic wardrowbe-mcp-oidc \
  --from-literal=oidc-client-secret="$OIDC_CLIENT_SECRET" \
  --from-literal=oidc-refresh-token="$OIDC_REFRESH_TOKEN"
```

## Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Number of pods. |
| `image.repository` | `ghcr.io/jansitarski/wardrowbe-mcp` | Image repository. |
| `image.tag` | `""` | Image tag; falls back to `.Chart.AppVersion`. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Pull secrets for a private image, e.g. `[{name: ghcr-pull}]`. |
| `config.transport` | `http` | `http` (Streamable HTTP) or `stdio`. |
| `config.host` | `0.0.0.0` | Bind host. |
| `config.port` | `8080` | Bind port (also the container/probe port). |
| `config.wardrowbeUrl` | `""` | **Required.** Backend base URL (no `/api/v1`). |
| `config.auth` | `dev` | `dev` or `oidc`. |
| `config.externalId` | `""` | Dev identity sent to `/auth/sync`. |
| `config.externalEmail` | `""` | Real email sent in dev `/auth/sync`. |
| `config.portalResourceUrl` | `""` | Emits RFC 9728 `resource_metadata` on `401`. |
| `config.maxConcurrent` | `""` | In-flight `/mcp` cap (empty → binary default 16). |
| `config.maxBodyMb` | `""` | Inbound `/mcp` body cap MB (empty → default 40). |
| `config.extraArgs` | `[]` | Additional raw flags. |
| `oidc.issuerUrl` | `""` | OIDC issuer URL (flag; `config.auth=oidc`). |
| `oidc.clientId` | `""` | OIDC client ID (flag; `config.auth=oidc`). |
| `oidc.existingSecret` | `""` | Secret holding the OIDC client secret / refresh token (env, not args). |
| `oidc.clientSecretKey` | `oidc-client-secret` | Secret key for the client secret. |
| `oidc.refreshTokenKey` | `oidc-refresh-token` | Secret key for the refresh token. |
| `apiKey.value` | `""` | Inline bearer key; chart creates a Secret. |
| `apiKey.existingSecret` | `""` | Reference an existing Secret instead. |
| `apiKey.key` | `mcp-api-key` | Secret key holding the bearer. |
| `env` | `{MCP_LOG_LEVEL: INFO}` | Extra environment variables. |
| `service.type` | `ClusterIP` | Service type. |
| `service.port` | `8080` | Service port. |
| `resources` | requests 128Mi/50m, limits 384Mi/500m | Container resources. |
| `podSecurityContext` | `runAsNonRoot`, `seccompProfile: RuntimeDefault` | Pod security context. |
| `securityContext` | no-priv-esc, read-only root, drop ALL caps | Container security context. |
| `serviceAccount.create` | `false` | Create a ServiceAccount. |
| `serviceAccount.name` | `""` | ServiceAccount name (or existing). |
| `nodeSelector` / `tolerations` / `affinity` | `{}` / `[]` / `{}` | Scheduling. |
| `podAnnotations` | `{}` | Pod annotations. |
| `readinessProbe` | `GET /readyz` | Readiness probe (pings the backend). |
| `livenessProbe` | `GET /` | Liveness probe (static). |

See [`values.yaml`](values.yaml) for the full, commented set.

## Use from Flux

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: wardrowbe-mcp
  namespace: flux-system
spec:
  interval: 1h
  url: oci://ghcr.io/jansitarski/charts/wardrowbe-mcp
  ref:
    tag: "1.0.0"
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: wardrowbe-mcp
  namespace: wardrowbe
spec:
  interval: 1h
  chartRef:
    kind: OCIRepository
    name: wardrowbe-mcp
    namespace: flux-system
  valuesFrom:
    - kind: Secret
      name: wardrowbe-mcp-values
```
