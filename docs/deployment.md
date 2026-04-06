# DevPlane Deployment Guide

Audience: platform engineers who know Kubernetes but are new to this project.

---

## Namespace Architecture

DevPlane uses two namespaces to keep concerns cleanly separated:

```
workspace-operator-system   ← operator, gateway, RBAC, secrets
workspaces                  ← user pods, PVCs, headless services (one per user)
```

**Why separate?**

- Resource quotas and LimitRanges can target `workspaces` alone without affecting the control plane.
- NetworkPolicies for user pods (deny-all default, egress only to `ai-system`) are scoped to `workspaces` without touching the gateway.
- RBAC is easier to audit: the operator's ClusterRole covers both namespaces, but the gateway ServiceAccount only needs to interact with the `workspaces` namespace.

The Helm chart creates the `workspaces` namespace by default (`gateway.createWorkspaceNamespace: true`). Set to `false` if you manage namespace lifecycle externally (e.g., via GitOps).

---

## Gateway high availability and rate limits

**Replicas.** Scale the gateway Deployment with `gateway.replicas` (default `2`). Each replica is stateless: OIDC validation, Workspace CR reads/writes, and WebSocket proxying do not require session affinity to a specific gateway pod. Browsers that lose a connection during a rolling restart can reload or reconnect; the workspace pod is the long-lived endpoint.

**Probes and shutdown.** The chart configures `livenessProbe` and `readinessProbe` on `GET /health`. `terminationGracePeriodSeconds` is set to `30` so in-flight HTTP requests and WebSocket proxies can drain when the pod receives `SIGTERM` (the process calls `http.Server.Shutdown` with a 30s budget).

**Rate limits (abuse controls).** After a successful OIDC token validation, the gateway can apply token-bucket limits to:

- `GET`/`POST` `/api/workspace` (lifecycle polling),
- `GET` `/ws` (WebSocket terminal connect).

Configure via `gateway.rateLimit.lifecycle` and `gateway.rateLimit.websocket`: each block has `globalRPS`, `globalBurst`, `perUserRPS`, and `perUserBurst`. **Zero means unlimited** for that bucket. Per-user keys use the OIDC `sub` claim. When a limit trips, the gateway returns **HTTP 429** with JSON `{"error":"rate_limited"}`, increments `devplane_gateway_rate_limit_hits_total`, and logs `devplane.event=gateway.rate_limit.exceeded`.

**CI-friendly unit tests** exercise the limiter without sleeps (`pkg/gateway/ratelimit_test.go`, `cmd/gateway/main_test.go` — including **per-user-only** `2 RPS / burst 3` cases that assert HTTP 429, JSON `rate_limited`, and `devplane_gateway_rate_limit_hits_total` for `scope="user"`).

**Manual / staging proof (non-zero tuning).** Helm defaults keep all buckets at `0` (unlimited); before calling limits “verified” in production, apply a **non-zero** profile and confirm predictable tripping plus metric/log correlation. Example values fragment (matches automated per-user tests):

```yaml
gateway:
  rateLimit:
    lifecycle:
      globalRPS: 0
      globalBurst: 0
      perUserRPS: 2
      perUserBurst: 3
    websocket:
      globalRPS: 0
      globalBurst: 0
      perUserRPS: 2
      perUserBurst: 3
```

After OIDC login, issue **four** back-to-back authenticated `GET /api/workspace` requests (same user): the first three may pass the limiter and fail later with `500` if the workspace backend errors; the **fourth** should return **429** with `{"error":"rate_limited"}`. Repeat on `GET /ws` (WebSocket upgrade path): fourth connect attempt should see 429 **before** upgrade. Watch:

- `devplane_gateway_rate_limit_hits_total{endpoint="lifecycle",scope="user"}` (and `endpoint="websocket"`) increment;
- gateway logs with `devplane.event=gateway.rate_limit.exceeded` and the same `devplane.request_id` as the `X-Request-ID` response header.

**PromQL (examples).**

```promql
sum(rate(devplane_gateway_rate_limit_hits_total[5m])) by (endpoint, scope)
```

---

## Prerequisites

| Requirement | Notes |
|-------------|-------|
| Kubernetes 1.27+ | Stable APIs only |
| Helm 3.10+ | |
| Container image registry | Accessible from the cluster |
| LLM endpoint (optional) | Any OpenAI-compatible URL reachable from workspace pods (vLLM, Ollama, LM Studio, remote API). Without one, workspaces still work — opencode will just fail to connect. |
| OIDC Identity Provider | Any OIDC-compliant IdP (Keycloak, Dex, Azure AD, etc.) |
| StorageClass | With `ReadWriteOnce` support for workspace PVCs |

---

## Published images and Helm chart

Pre-built, multi-arch (`linux/amd64` + `linux/arm64`) images are published to the GitHub Container Registry for every release:

| Image | Registry path |
|-------|---------------|
| Operator  | `ghcr.io/spjdevops/devplane/workspace-operator` |
| Gateway   | `ghcr.io/spjdevops/devplane/workspace-gateway`  |
| Workspace | `ghcr.io/spjdevops/devplane/workspace`          |

The Helm chart is served from the GitHub Pages Helm repository:

```
https://spjdevops.github.io/DevPlane
```

Available chart versions: [index.yaml](https://spjdevops.github.io/DevPlane/index.yaml)

---

## Installation

### 1. Images

**Standard (internet-connected) cluster** — the default `values.yaml` already points at the published GHCR images; no extra steps are needed.

**Air-gapped cluster** — mirror the images to your internal registry first:

```bash
REGISTRY=registry.example.com/devplane
VERSION=1.1.5

for img in workspace-operator workspace-gateway workspace; do
  docker pull ghcr.io/spjdevops/devplane/${img}:${VERSION}
  docker tag  ghcr.io/spjdevops/devplane/${img}:${VERSION} ${REGISTRY}/${img}:${VERSION}
  docker push ${REGISTRY}/${img}:${VERSION}
done
```

Then override the image repositories in your values file (see step 2).

If you need to build from source instead:

```bash
make docker-build   # produces workspace-operator:latest, workspace-gateway:latest, workspace:latest
```

### 2. Install with Helm

Add the Helm repository once:

```bash
helm repo add devplane https://spjdevops.github.io/DevPlane
helm repo update
```

Install (quick start, public cluster):

```bash
helm install workspace-operator devplane/workspace-operator \
  --version 1.1.5 \
  --namespace workspace-operator-system \
  --create-namespace \
  --set gateway.oidc.issuerURL=https://idp.example.com \
  --set gateway.oidc.clientID=devplane \
  --set 'workspace.ai.providers[0].name=local' \
  --set 'workspace.ai.providers[0].endpoint=http://vllm.ai-system.svc:8000' \
  --set 'workspace.ai.providers[0].models[0]=deepseek-coder-33b-instruct'
```

Using a values file (recommended for production):

```yaml
# my-values.yaml
operator:
  image:
    repository: ghcr.io/spjdevops/devplane/workspace-operator
    tag: "1.1.5"
  replicas: 1
  leaderElect: true

gateway:
  image:
    repository: ghcr.io/spjdevops/devplane/workspace-gateway
    tag: "1.1.5"
  replicas: 2
  workspaceNamespace: "workspaces"
  createWorkspaceNamespace: true
  oidc:
    issuerURL: "https://idp.example.com"
    clientID: "devplane"
  ingress:
    enabled: true
    className: nginx
    host: devplane.example.com
    tls:
    - secretName: devplane-tls
      hosts:
      - devplane.example.com

workspace:
  image:
    repository: ghcr.io/spjdevops/devplane/workspace
    tag: "1.1.5"
  defaultResources:
    cpu: "2"
    memory: "4Gi"
    storage: "20Gi"
  storageClass: "fast-ssd"
  ai:
    providers:
      - name: local
        endpoint: "http://vllm.ai-system.svc:8000"
        models:
          - deepseek-coder-33b-instruct
    egressNamespaces:
      - ai-system
```

```bash
helm install workspace-operator devplane/workspace-operator \
  --version 1.1.5 \
  -n workspace-operator-system --create-namespace \
  -f my-values.yaml
```

**Air-gapped override** — if you mirrored images in step 1, add the private registry to your values file:

```yaml
operator:
  image:
    repository: registry.example.com/devplane/workspace-operator
    tag: "1.1.5"
gateway:
  image:
    repository: registry.example.com/devplane/workspace-gateway
    tag: "1.1.5"
workspace:
  image:
    repository: registry.example.com/devplane/workspace
    tag: "1.1.5"
```

### 3. OIDC configuration

The gateway reads OIDC credentials from a Kubernetes Secret. The chart creates the Secret automatically from four values:

| Value | Description |
|-------|-------------|
| `gateway.oidc.issuerURL` | OIDC issuer URL (e.g. `https://keycloak.example.com/realms/devplane`) |
| `gateway.oidc.clientID` | OAuth2 client ID registered with the IdP |
| `gateway.oidc.clientSecret` | OAuth2 client secret for the authorization code flow |
| `gateway.oidc.redirectURL` | Full callback URL, e.g. `https://devplane.example.com/callback` — **must be registered with the IdP** |

`cookieSecure` is derived automatically: if `redirectURL` starts with `https://`, session cookies are set with `Secure=true`. HTTP URLs (local dev) work without any extra flag.

To use a pre-existing Secret (e.g. managed by an external secrets operator):

```yaml
gateway:
  oidc:
    existingSecret: "my-oidc-secret"  # must have keys: issuer-url, client-id, client-secret, redirect-url
```

Create it manually:

```bash
kubectl create secret generic my-oidc-secret \
  -n workspace-operator-system \
  --from-literal=issuer-url=https://idp.example.com \
  --from-literal=client-id=devplane \
  --from-literal=client-secret=<your-client-secret> \
  --from-literal=redirect-url=https://devplane.example.com/callback
```

### 4. Verify the deployment

```bash
# Namespaces created
kubectl get namespace workspace-operator-system workspaces

# Operator and gateway pods running
kubectl get pods -n workspace-operator-system

# CRDs installed
kubectl get crd workspaces.workspace.devplane.io

# Create a test workspace
kubectl apply -f - <<EOF
apiVersion: workspace.devplane.io/v1alpha1
kind: Workspace
metadata:
  name: test-user
  namespace: workspaces
spec:
  user:
    id: "test-user"
    email: "test@example.com"
  resources:
    cpu: "2"
    memory: "4Gi"
    storage: "20Gi"
  aiConfig:
    providers:
      - name: local
        endpoint: "http://vllm.ai-system.svc:8000"
        models:
          - deepseek-coder-33b-instruct
EOF

# Watch the workspace progress through phases: Pending → Creating → Running
kubectl get workspace test-workspace -n workspaces -w

# Confirm pod and PVC landed in workspaces namespace
kubectl get pods,pvc -n workspaces
```

---

## Private / Self-Signed CA Certificates

If your OIDC provider (e.g. Keycloak) or internal services use a certificate signed by a private CA, you need that CA trusted in two places:

- **Gateway** — makes outbound HTTPS calls to the OIDC issuer to validate tokens.
- **Workspace pods** — users run `git`, `curl`, opencode, and other tools that need to trust the same CA.

### 1. Create a ConfigMap with your CA certificate(s)

Put every PEM-encoded CA certificate you need into a single ConfigMap. The keys can end in `.crt` or `.pem` — any filename works.

```bash
# Single CA file
kubectl create configmap devplane-ca-bundle \
  --from-file=ca.crt=/path/to/your-ca.crt \
  -n workspace-operator-system

# Multiple CAs (e.g. root + intermediate)
kubectl create configmap devplane-ca-bundle \
  --from-file=root-ca.crt=/path/to/root-ca.crt \
  --from-file=intermediate-ca.crt=/path/to/intermediate-ca.crt \
  -n workspace-operator-system
```

> The ConfigMap must be in the same namespace as the gateway (`workspace-operator-system` by default).

### 2. Configure the Gateway to trust the CA

Set `gateway.tls.customCABundle.configMapName` in your values file:

```yaml
gateway:
  tls:
    customCABundle:
      configMapName: devplane-ca-bundle
```

The Helm chart mounts the ConfigMap at `/etc/ssl/certs/custom` inside the gateway container and sets `SSL_CERT_FILE=/etc/ssl/certs/custom/ca-certificates.crt`, which Go's `net/http` (and therefore all OIDC validation) picks up automatically.

### 3. Configure workspace pods to trust the CA

There are two ways to inject a CA bundle into workspace pods. Option A is recommended for most clusters — it requires no per-CR config.

**Option A — operator-wide default via `workspace.defaultCABundle` (recommended):**

Create the ConfigMap in the `workspaces` namespace and set one Helm value. Every workspace pod will automatically mount it, regardless of how the Workspace CR was created.

```bash
# Create the ConfigMap in the workspaces namespace
kubectl create configmap devplane-ca-bundle \
  --from-file=ca.crt=/path/to/your-ca.crt \
  -n workspaces
```

```yaml
# values.yaml
workspace:
  defaultCABundle:
    configMapName: devplane-ca-bundle   # ConfigMap in the workspaces namespace
```

Individual Workspace CRs can still override this via `spec.tls.customCABundle`; the per-CR setting takes precedence when both are configured.

**Option B — per-workspace Workspace CR:**

```yaml
apiVersion: workspace.devplane.io/v1alpha1
kind: Workspace
metadata:
  name: alice
  namespace: workspaces
spec:
  user:
    id: alice
    email: alice@example.com
  tls:
    customCABundle:
      name: devplane-ca-bundle   # ConfigMap in the workspaces namespace
  resources:
    cpu: "2"
    memory: "4Gi"
    storage: "20Gi"
  aiConfig:
    providers:
      - name: local
        endpoint: "http://vllm.ai-system.svc:8000"
        models:
          - deepseek-coder-33b-instruct
```

### What happens inside the workspace pod

The entrypoint always exports CA environment variables pointing at a known trust store. When a custom CA is mounted (`CUSTOM_CA_MOUNTED=true`), it first merges the custom certs with the system store:

```
/etc/ssl/certs/ca-certificates.crt  (system)
/etc/ssl/certs/custom/*.crt         (your CA(s))   ──► /tmp/ca-bundle.crt
/etc/ssl/certs/custom/*.pem         (your CA(s))
```

When no custom CA is mounted, the standard system bundle (`/etc/ssl/certs/ca-certificates.crt`) is used instead. Either way, the following variables are always exported so every tool trusts the correct store without any manual configuration:

| Variable | Used by |
|----------|---------|
| `SSL_CERT_FILE` | Go binaries (opencode, custom tools) |
| `REQUESTS_CA_BUNDLE` | Python `requests`, `httpx`, `boto3`, `pip` |
| `CURL_CA_BUNDLE` | `curl` |
| `GIT_SSL_CAINFO` | `git` (HTTPS remotes) |
| `NODE_EXTRA_CA_CERTS` | Node.js / npm |

### Typical Keycloak setup

```yaml
# values.yaml excerpt
gateway:
  oidc:
    issuerURL: https://keycloak.internal.example.com/realms/devplane
    clientID: devplane
  tls:
    customCABundle:
      configMapName: devplane-ca-bundle   # in workspace-operator-system namespace

workspace:
  defaultCABundle:
    configMapName: devplane-ca-bundle     # in workspaces namespace
```

```bash
# CA bundle in operator namespace (for gateway)
kubectl create configmap devplane-ca-bundle \
  --from-file=ca.crt=/path/to/internal-ca.crt \
  -n workspace-operator-system

# CA bundle in workspaces namespace (for workspace pods)
kubectl create configmap devplane-ca-bundle \
  --from-file=ca.crt=/path/to/internal-ca.crt \
  -n workspaces
```

---

## Package Mirror Configuration (pip and npm)

In air-gapped environments, `pip install` and `npm install` must reach an internal proxy (Nexus, Artifactory, Devpi, Verdaccio, etc.) instead of the public registries. Set these once in Helm and every workspace pod gets the correct environment variables automatically — no per-user configuration required.

### pip

```yaml
workspace:
  packageMirrors:
    pip:
      indexUrl: "https://nexus.example.com/repository/pypi-proxy/simple"
      trustedHost: "nexus.example.com"   # only needed for self-signed certs
```

These map directly to pip's native environment variables `PIP_INDEX_URL` and `PIP_TRUSTED_HOST`, which pip reads before any `pip.conf`. The `trustedHost` setting is only required when the mirror uses a certificate that is **not** covered by your CA bundle — for example, a plain HTTP endpoint or a host with a self-signed cert that you haven't added to the bundle.

If your mirror is reachable over HTTPS and your CA bundle already trusts it, you can omit `trustedHost`:

```yaml
workspace:
  packageMirrors:
    pip:
      indexUrl: "https://nexus.example.com/repository/pypi-proxy/simple"
```

### npm

```yaml
workspace:
  packageMirrors:
    npm:
      registry: "https://nexus.example.com/repository/npm-proxy"
```

This sets `npm_config_registry` in the workspace pod environment. npm (and opencode's dependency installer) reads this variable before any `.npmrc` file, so it works without touching user home directories.

### Combined air-gapped example

```yaml
workspace:
  defaultCABundle:
    configMapName: devplane-ca-bundle
  packageMirrors:
    pip:
      indexUrl: "https://nexus.internal.example.com/repository/pypi-proxy/simple"
    npm:
      registry: "https://nexus.internal.example.com/repository/npm-proxy"
```

> If your Nexus/Artifactory instance uses a certificate signed by your internal CA, add that CA to `workspace.defaultCABundle` — pip and npm will pick it up via `REQUESTS_CA_BUNDLE` and `NODE_EXTRA_CA_CERTS` respectively, so you won't need `pip.trustedHost`.

---

## AI Provider Configuration: Helm vs. Workspace CR

DevPlane uses the same AI provider data in two places with **different key names** depending on context:

| Context | Key prefix | Example |
|---------|------------|---------|
| Helm values | `workspace.ai.*` | `workspace.ai.providers`, `workspace.ai.egressNamespaces` |
| Workspace CR | `spec.aiConfig.*` | `spec.aiConfig.providers`, `spec.aiConfig.egressNamespaces` |

These are the **same data** — the Helm chart translates between them automatically:

1. `workspace.ai.providers` in `values.yaml` → serialised to `AI_PROVIDERS_JSON` env var on the gateway pod.
2. When a user logs in, the gateway calls `EnsureWorkspace`, which writes `spec.aiConfig.providers` on the resulting Workspace CR using the providers from `AI_PROVIDERS_JSON`.
3. The operator reads `spec.aiConfig.*` and injects `AI_PROVIDERS_JSON` into the workspace pod, which opencode uses to generate its configuration.

**Common mistake:** setting `workspace.ai.providers` in Helm but then trying to override `workspace.ai.providers` in a Workspace CR manifest as `workspace.ai.providers` (or vice-versa). If you are editing a Workspace CR directly, use `spec.aiConfig.providers`. If you are configuring the Helm chart (or checking defaults), use `workspace.ai.providers`.

Similarly, `workspace.ai.egressNamespaces` and `workspace.ai.egressPorts` in `values.yaml` map to `spec.aiConfig.egressNamespaces` and `spec.aiConfig.egressPorts` on the CR.

---

## Helm Values Reference

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `operator.image.repository` | string | `workspace-operator` | Operator image repository |
| `operator.image.tag` | string | `latest` | Operator image tag |
| `operator.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `operator.replicas` | int | `1` | Operator replica count (use 1 unless HA tested) |
| `operator.leaderElect` | bool | `true` | Enable leader election for HA |
| `operator.resources` | object | see values.yaml | CPU/memory requests and limits |
| `gateway.enabled` | bool | `true` | Deploy the gateway component |
| `gateway.image.repository` | string | `workspace-gateway` | Gateway image repository |
| `gateway.image.tag` | string | `latest` | Gateway image tag |
| `gateway.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `gateway.replicas` | int | `2` | Gateway replica count |
| `gateway.workspaceNamespace` | string | `workspaces` | Namespace where user pods/PVCs/services are created |
| `gateway.createWorkspaceNamespace` | bool | `true` | Create the workspace namespace during install |
| `gateway.oidc.issuerURL` | string | `""` | OIDC issuer URL |
| `gateway.oidc.clientID` | string | `""` | OIDC client ID |
| `gateway.oidc.clientSecret` | string | `""` | OIDC client secret for authorization code flow |
| `gateway.oidc.redirectURL` | string | `""` | Full callback URL (must be registered with IdP), e.g. `https://devplane.example.com/callback` |
| `gateway.oidc.existingSecret` | string | `""` | Use a pre-existing Secret for OIDC credentials (keys: `issuer-url`, `client-id`, `client-secret`, `redirect-url`) |
| `gateway.resources` | object | see values.yaml | CPU/memory requests and limits |
| `gateway.ingress.enabled` | bool | `false` | Create an Ingress for the gateway |
| `gateway.ingress.className` | string | `""` | IngressClass name |
| `gateway.ingress.host` | string | `devplane.example.com` | Ingress hostname |
| `gateway.ingress.tls` | list | `[]` | TLS configuration for the Ingress |
| `workspace.image.repository` | string | `workspace` | Workspace pod image repository |
| `workspace.image.tag` | string | `latest` | Workspace pod image tag |
| `workspace.defaultResources.cpu` | string | `2` | Default CPU request for workspace pods |
| `workspace.defaultResources.memory` | string | `4Gi` | Default memory request for workspace pods |
| `workspace.defaultResources.storage` | string | `20Gi` | Default PVC size for workspace pods |
| `workspace.storageClass` | string | `""` | StorageClass for workspace PVCs (cluster default if empty) |
| `workspace.ai.providers` | list | see below | List of AI provider backends. Each entry requires `name` (opencode provider key), `endpoint` (OpenAI-compatible base URL), and `models` (list of model IDs). At least one provider must be specified. Example: `[{name: local, endpoint: "http://vllm.ai-system.svc:8000", models: [deepseek-coder-33b-instruct]}]` |
| `workspace.ai.egressNamespaces` | string | `ai-system` | Comma-separated in-cluster namespaces whose pods workspace pods may reach on any port (LLM services) |
| `workspace.ai.egressPorts` | string | `22,80,443,5000,8000,8080,8081,11434` | Comma-separated TCP ports allowed for egress to external IPs. Covers SSH (22), HTTP/HTTPS (80/443), Docker registry (5000), vLLM (8000), Nexus/Artifactory (8080/8081), Ollama (11434). Override to suit your environment. |
| `workspace.idleTimeout` | string | `24h` | How long a Running workspace may be idle before its pod is stopped. Go duration syntax (`24h`, `8h30m`). Leave empty to disable. |
| `workspace.defaultCABundle.configMapName` | string | `""` | Name of a ConfigMap **in the workspaces namespace** containing PEM-encoded CA certificates. Mounted in all workspace pods when set. Individual Workspace CRs can still override this via `spec.tls.customCABundle`. |
| `workspace.packageMirrors.pip.indexUrl` | string | `""` | Sets `PIP_INDEX_URL` in every workspace pod. Use the full simple-index URL of your internal PyPI mirror, e.g. `https://nexus.example.com/repository/pypi-proxy/simple`. |
| `workspace.packageMirrors.pip.trustedHost` | string | `""` | Sets `PIP_TRUSTED_HOST` in every workspace pod. Hostname only (no scheme). Only required when the pip mirror uses a certificate not covered by the CA bundle (e.g. plain HTTP or an untrusted self-signed cert). |
| `workspace.packageMirrors.npm.registry` | string | `""` | Sets `npm_config_registry` in every workspace pod. Full URL of your internal npm registry, e.g. `https://nexus.example.com/repository/npm-proxy`. |

---

## Upgrade & Rollback

### Upgrade

```bash
helm upgrade workspace-operator deploy/helm/workspace-operator \
  -n workspace-operator-system \
  -f my-values.yaml \
  --atomic \
  --timeout 5m
```

`--atomic` rolls back automatically if the upgrade does not complete within the timeout.

**CRD caveat:** Helm does not upgrade CRDs that are already installed (by design). When a new version changes the `Workspace` CRD schema, apply it manually before upgrading:

```bash
kubectl apply -f deploy/helm/workspace-operator/crds/
# Then run helm upgrade
```

### Rollback

```bash
# List release history
helm history workspace-operator -n workspace-operator-system

# Roll back to a previous revision
helm rollback workspace-operator <REVISION> -n workspace-operator-system
```

Rolling back does **not** revert CRD changes. If the new CRD schema is incompatible with the old operator, restore the previous CRD manually.

---

## Observability

### Metrics

The operator exposes Prometheus metrics on `:8080/metrics` (controller-runtime defaults). The gateway exposes application metrics on `:8080/metrics`.

Scrape config example:

```yaml
- job_name: devplane-operator
  static_configs:
  - targets: ['workspace-operator-controller-manager.workspace-operator-system.svc:8080']
- job_name: devplane-gateway
  static_configs:
  - targets: ['workspace-operator-gateway.workspace-operator-system.svc:8080']
```

### Structured logs

Both components emit JSON-structured logs via `logr`. Key fields:

| Field | Description |
|-------|-------------|
| `workspace` | Workspace CR name |
| `namespace` | Workspace CR namespace |
| `user` | User ID from the Workspace spec |
| `phase` | Workspace phase transition |
| `error` | Wrapped error message |

```bash
# Stream operator logs
kubectl logs -n workspace-operator-system -l app=workspace-operator -f | jq .

# Stream gateway logs
kubectl logs -n workspace-operator-system -l app=workspace-gateway -f | jq .
```

### Useful kubectl one-liners

```bash
# All workspaces and their phases
kubectl get workspaces -n workspaces

# Workspaces stuck in Creating for more than 5 minutes
kubectl get workspaces -n workspaces -o json \
  | jq '.items[] | select(.status.phase=="Creating") | .metadata.name'

# Events for a specific workspace
kubectl describe workspace <name> -n workspaces

# Pod logs for a user's workspace
kubectl logs -n workspaces <userid>-workspace-pod
```

---

## Production Hardening

1. **Pin image tags** — never use `latest` in production. Use immutable digests or semver tags.

2. **Resource limits** — set meaningful limits for operator, gateway, and workspace defaults to prevent noisy-neighbour issues:
   ```yaml
   operator:
     resources:
       limits:
         cpu: 500m
         memory: 128Mi
   gateway:
     resources:
       limits:
         cpu: 500m
         memory: 256Mi
   workspace:
     defaultResources:
       cpu: "2"
       memory: "4Gi"
   ```

3. **StorageClass** — specify an explicit `workspace.storageClass` with a fast block storage backend. Avoid the cluster default, which may be slow or shared.

4. **TLS Ingress** — enable TLS on the gateway Ingress. Terminate at the Ingress controller and configure cert-manager for automatic renewal:
   ```yaml
   gateway:
     ingress:
       enabled: true
       tls:
       - secretName: devplane-tls
         hosts: [devplane.example.com]
   ```

5. **HA — replicas ≥ 2 for gateway** — the gateway is stateless and safe to scale horizontally. Keep `gateway.replicas: 2` (or more) and enable `operator.leaderElect: true` for the operator.

6. **Dedicated workspace namespace** — keep `gateway.workspaceNamespace: workspaces` and apply a ResourceQuota to cap total workspace resource usage:
   ```bash
   kubectl apply -f - <<EOF
   apiVersion: v1
   kind: ResourceQuota
   metadata:
     name: workspace-quota
     namespace: workspaces
   spec:
     hard:
       pods: "50"
       requests.cpu: "100"
       requests.memory: "200Gi"
       persistentvolumeclaims: "50"
   EOF
   ```

7. **NetworkPolicy prerequisites** — for the deny-all-plus-vllm-egress NetworkPolicy to work, the cluster CNI must enforce NetworkPolicy (Calico, Cilium, or similar). Confirm with:
   ```bash
   kubectl get networkpolicies -n workspaces
   ```

8. **RBAC audit** — review the operator ClusterRole. It needs `pods`, `persistentvolumeclaims`, and `services` in the `workspaces` namespace. Scope down to a namespaced Role if cluster-wide access is undesirable.

---

## Troubleshooting

### Workspace stuck in `Creating`

```bash
kubectl describe workspace <name> -n workspaces
kubectl describe pod <userid>-workspace-pod -n workspaces
kubectl get events -n workspaces --sort-by=.lastTimestamp
```

Common causes:
- Image pull failure — check `imagePullSecrets` and registry accessibility.
- PVC pending — no available PV or StorageClass misconfiguration (`kubectl describe pvc <userid>-workspace-pvc -n workspaces`).
- Pod scheduling failure — insufficient node resources.

### Pod `CrashLoopBackOff`

```bash
kubectl logs -n workspaces <userid>-workspace-pod --previous
```

Common causes:
- ttyd or opencode binary missing from image (re-build `Dockerfile.workspace`).
- LLM endpoint unreachable — check that `AI_PROVIDERS_JSON` is set correctly and that the endpoints are reachable from the pod (workspaces still start without a reachable endpoint; only opencode is affected).
- Filesystem permission error — workspace image must run as UID 1000 with writable mounted volumes.

### OIDC 401 errors

```bash
kubectl logs -n workspace-operator-system -l app=workspace-gateway | grep -i oidc
```

Common causes:
- `OIDC_ISSUER_URL` mismatch — the URL must exactly match the `iss` claim in tokens.
- Clock skew — ensure cluster nodes are NTP-synced.
- `existingSecret` missing or has wrong keys (`issuer-url`, `client-id`).

### NetworkPolicy issues

```bash
# Verify policies exist
kubectl get networkpolicy -n workspaces

# Inspect the egress policy for a user
kubectl get networkpolicy <userid>-workspace-egress -n workspaces -o yaml

# Test egress from a workspace pod
kubectl exec -n workspaces <userid>-workspace-pod -- \
  curl -v http://vllm.ai-system.svc:8000/health

# Test git-over-SSH egress
kubectl exec -n workspaces <userid>-workspace-pod -- \
  nc -zv github.com 22
```

**Allowed external TCP ports** are controlled by `workspace.ai.egressPorts` in `values.yaml` (operator default) or `spec.aiConfig.egressPorts` on the Workspace CR (per-workspace override). The built-in default list is `22,80,443,5000,8000,8080,8081,11434`.

Changes to `egressPorts` or `egressNamespaces` take effect on the next reconcile — you do not need to delete the existing NetworkPolicy.

If the workspace cannot reach vLLM, verify:
1. The vLLM service namespace is included in `egressNamespaces`.
2. The NetworkPolicy egress rule targets the correct namespace label (`kubernetes.io/metadata.name: <ns>`).
3. If vLLM runs on a bare-metal host outside the cluster, its port must be listed in `egressPorts` and `egressNamespaces` should be left empty (the external IP rule covers it).
