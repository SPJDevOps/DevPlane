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
| vLLM endpoint | Running in-cluster, e.g. `http://vllm.ai-system.svc:8000` |
| OIDC Identity Provider | Any OIDC-compliant IdP (Keycloak, Dex, Azure AD, etc.) |
| StorageClass | With `ReadWriteOnce` support for workspace PVCs |

---

## Installation

### 1. Build & push images

```bash
# Build all three images
make docker-build

# Tag and push to your registry (replace REGISTRY)
REGISTRY=registry.example.com/devplane

docker tag workspace-operator:latest  $REGISTRY/workspace-operator:v1.0.0
docker tag workspace-gateway:latest   $REGISTRY/workspace-gateway:v1.0.0
docker tag workspace:latest           $REGISTRY/workspace:v1.0.0

docker push $REGISTRY/workspace-operator:v1.0.0
docker push $REGISTRY/workspace-gateway:v1.0.0
docker push $REGISTRY/workspace:v1.0.0
```

### 2. Install with Helm

```bash
helm install workspace-operator deploy/helm/workspace-operator \
  --namespace workspace-operator-system \
  --create-namespace \
  --set operator.image.repository=registry.example.com/devplane/workspace-operator \
  --set operator.image.tag=v1.0.0 \
  --set gateway.image.repository=registry.example.com/devplane/workspace-gateway \
  --set gateway.image.tag=v1.0.0 \
  --set workspace.image.repository=registry.example.com/devplane/workspace \
  --set workspace.image.tag=v1.0.0 \
  --set gateway.oidc.issuerURL=https://idp.example.com \
  --set gateway.oidc.clientID=devplane \
  --set workspace.ai.vllmEndpoint=http://vllm.ai-system.svc:8000 \
  --set workspace.ai.vllmModel=deepseek-coder-33b-instruct
```

Using a values file (recommended for production):

```yaml
# my-values.yaml
operator:
  image:
    repository: registry.example.com/devplane/workspace-operator
    tag: v1.0.0
  replicas: 1
  leaderElect: true

gateway:
  image:
    repository: registry.example.com/devplane/workspace-gateway
    tag: v1.0.0
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
    repository: registry.example.com/devplane/workspace
    tag: v1.0.0
  defaultResources:
    cpu: "2"
    memory: "4Gi"
    storage: "20Gi"
  storageClass: "fast-ssd"
  ai:
    vllmEndpoint: "http://vllm.ai-system.svc:8000"
    vllmModel: "deepseek-coder-33b-instruct"
    vllmNamespace: "ai-system"
```

```bash
helm install workspace-operator deploy/helm/workspace-operator \
  -n workspace-operator-system --create-namespace \
  -f my-values.yaml
```

### 3. OIDC configuration

The gateway reads OIDC credentials from a Kubernetes Secret. The chart creates the Secret automatically when `gateway.oidc.issuerURL` and `gateway.oidc.clientID` are set.

To use a pre-existing Secret (e.g. managed by an external secrets operator):

```yaml
gateway:
  oidc:
    existingSecret: "my-oidc-secret"  # must have keys: issuer-url, client-id
```

Create it manually:

```bash
kubectl create secret generic my-oidc-secret \
  -n workspace-operator-system \
  --from-literal=issuer-url=https://idp.example.com \
  --from-literal=client-id=devplane
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
  name: test-workspace
  namespace: workspaces
spec:
  userID: "test-user"
  userEmail: "test@example.com"
EOF

# Watch the workspace progress through phases: Pending → Creating → Running
kubectl get workspace test-workspace -n workspaces -w

# Confirm pod and PVC landed in workspaces namespace
kubectl get pods,pvc -n workspaces
```

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
| `gateway.oidc.existingSecret` | string | `""` | Use a pre-existing Secret for OIDC credentials |
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
| `workspace.ai.vllmEndpoint` | string | `http://vllm.ai-system.svc:8000` | vLLM endpoint injected into workspace pods |
| `workspace.ai.vllmModel` | string | `deepseek-coder-33b-instruct` | vLLM model name injected into workspace pods |
| `workspace.ai.vllmNamespace` | string | `ai-system` | Namespace of the vLLM service (used in NetworkPolicy) |
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
- ttyd or OpenCoder binary missing from image (re-build `Dockerfile.workspace`).
- `VLLM_ENDPOINT` unreachable — verify the vLLM service is up and the NetworkPolicy allows egress.
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
