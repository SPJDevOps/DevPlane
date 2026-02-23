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
VERSION=1.0.0

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
  --version 1.0.0 \
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
    tag: "1.0.0"
  replicas: 1
  leaderElect: true

gateway:
  image:
    repository: ghcr.io/spjdevops/devplane/workspace-gateway
    tag: "1.0.0"
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
    tag: "1.0.0"
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
  --version 1.0.0 \
  -n workspace-operator-system --create-namespace \
  -f my-values.yaml
```

**Air-gapped override** — if you mirrored images in step 1, add the private registry to your values file:

```yaml
operator:
  image:
    repository: registry.example.com/devplane/workspace-operator
    tag: "1.0.0"
gateway:
  image:
    repository: registry.example.com/devplane/workspace-gateway
    tag: "1.0.0"
workspace:
  image:
    repository: registry.example.com/devplane/workspace
    tag: "1.0.0"
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

Workspace pods are configured per-workspace via `spec.tls.customCABundle` on the Workspace CR. You can set a cluster-wide default by adding it to the operator's default workspace spec in `values.yaml`, or set it per-user workspace.

**Option A — operator-wide default via values.yaml:**

The Workspace CR created by the gateway inherits the operator's configured defaults. To have all workspace pods trust the CA, create a matching ConfigMap in the `workspaces` namespace and reference it in every Workspace CR, or set it as the default in your provisioning flow.

```bash
# Mirror the ConfigMap into the workspaces namespace
kubectl create configmap devplane-ca-bundle \
  --from-file=ca.crt=/path/to/your-ca.crt \
  -n workspaces
```

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

The entrypoint script detects `CUSTOM_CA_MOUNTED=true` (set automatically by the operator when `spec.tls.customCABundle` is configured) and merges the custom certs with the system trust store:

```
/etc/ssl/certs/ca-certificates.crt  (system)
/etc/ssl/certs/custom/*.crt         (your CA(s))
/etc/ssl/certs/custom/*.pem         ──► /tmp/ca-bundle.crt
```

The following environment variables are then exported so every tool in the workspace trusts the CA without any manual steps:

| Variable | Used by |
|----------|---------|
| `SSL_CERT_FILE` | Go binaries (opencode, custom tools) |
| `REQUESTS_CA_BUNDLE` | Python `requests` / `httpx` |
| `NODE_EXTRA_CA_CERTS` | Node.js / npm |

`curl`, `git`, and `wget` use the system store which is updated via `update-ca-certificates` by the same bundle file.

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
