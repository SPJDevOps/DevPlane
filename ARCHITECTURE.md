# Workspace Operator — Architecture

This document describes the design of the AI-powered development workspace operator for air-gapped and restricted environments.

## 1. Component Diagram

```
                    ┌─────────────────────────────────────────────────────────────┐
                    │                     Kubernetes Cluster                        │
                    │                                                               │
  User Browser      │   ┌─────────────┐     ┌──────────────┐     ┌─────────────┐  │
  (HTTPS/WSS)       │   │   Ingress   │────▶│   Gateway    │────▶│  Operator   │  │
  ────────────────▶ │   │ (optional)  │     │ OIDC + Proxy │     │ (controller)│  │
                    │   └─────────────┘     └──────┬───────┘     └──────┬──────┘  │
                    │                               │                    │          │
                    │                               │ create/get        │ watch    │
                    │                               │ Workspace CR      │ Workspace│
                    │                               ▼                    ▼          │
                    │                        ┌──────────────┐     ┌─────────────┐  │
                    │                        │  Workspace   │     │  Workspace  │  │
                    │                        │     CRs      │◀────│    Pod +    │  │
                    │                        │  (per user)  │     │  PVC + Svc  │  │
                    │                        └──────┬───────┘     └──────┬──────┘  │
                    │                               │                    │          │
                    │                               │ WebSocket         │          │
                    │                               ▼                    │          │
                    │                        ┌──────────────┐            │          │
                    │                        │  Workspace    │            │          │
                    │                        │  Pod (ttyd +  │◀───────────┘          │
                    │                        │  tmux+OpenCoder)                       │
                    │                        └──────┬───────┘                        │
                    │                               │                                │
                    │                               │ VLLM_ENDPOINT (egress)         │
                    │                               ▼                                │
                    │                        ┌──────────────┐                        │
                    │                        │  vLLM        │  (AI inference)        │
                    │                        │  (in-cluster) │                        │
                    │                        └──────────────┘                        │
                    └─────────────────────────────────────────────────────────────┘
```

**Components:**

| Component        | Role |
|-----------------|------|
| **Gateway**     | OIDC authentication, session handling, WebSocket proxy to user workspace pods. Single entrypoint for users. |
| **Operator**    | Watches `Workspace` CRs; creates/updates Pod, PVC, Service, ServiceAccount, RoleBinding per workspace. Stateless. |
| **Workspace CR**| One per user (or per namespace). Spec: user identity, resources, AI config, persistence. |
| **Workspace Pod**| Ubuntu-based container with ttyd, tmux, OpenCoder; env vars point to vLLM. |
| **vLLM**         | Internal inference service; workspace pods reach it via cluster DNS (e.g. `http://vllm.ai-system.svc:8000`). |

## 2. Authentication Flow (OIDC → Gateway → Workspace)

```
  User                Gateway                  IdP (OIDC)           Operator / K8s API
   │                      │                         │                        │
   │  1. GET /            │                         │                        │
   │ ──────────────────▶  │                         │                        │
   │                      │  2. Redirect to IdP     │                        │
   │  ◀────────────────── │ ──────────────────────▶│                        │
   │                      │                         │                        │
   │  3. Login at IdP     │                         │                        │
   │ ──────────────────────────────────────────────▶│                        │
   │  4. IdP redirect with code                     │                        │
   │  ◀─────────────────────────────────────────────│                        │
   │                      │                         │                        │
   │  5. GET /callback?code=...                     │                        │
   │ ──────────────────▶  │  6. Token exchange      │                        │
   │                      │ ──────────────────────▶ │                        │
   │                      │  7. ID token + claims   │                        │
   │                      │  ◀──────────────────────│                        │
   │                      │                         │                        │
   │                      │  8. Create/Get Workspace CR (K8s API)             │
   │                      │ ────────────────────────────────────────────────▶ │
   │                      │  9. Workspace status (phase, podName, endpoint)   │
   │                      │  ◀─────────────────────────────────────────────── │
   │                      │                         │                        │
   │  10. 302 to /workspace or WebSocket upgrade   │                        │
   │  ◀────────────────── │                         │                        │
   │  11. WebSocket to pod (proxied by Gateway)     │                        │
   │ ──────────────────▶  │ ──────────────────────────────────────────────▶ Pod (ttyd)
```

- Gateway validates the OIDC token (and optionally caches validation for a short TTL).
- From token claims it derives a sanitized **user ID** and **email** and ensures a `Workspace` CR exists (create or get) in the target namespace.
- Operator reconciles the CR and creates Pod + PVC + Service. Gateway then proxies the user’s WebSocket to the workspace pod’s ttyd (or terminal) endpoint.

## 3. Data Flow (User Request → Pod Creation → WebSocket Proxy)

1. **User hits Gateway** (e.g. `https://devplane.company.com`). After OIDC login, Gateway has identity.
2. **Gateway ensures Workspace CR**: It uses the K8s API (with its own ServiceAccount) to create or get a `Workspace` with `spec.user.id` and `spec.user.email` from the token. Defaults for `spec.resources`, `spec.aiConfig`, `spec.persistence` can come from Helm/gateway config.
3. **Operator reconciles**: Sees new/updated Workspace; ensures:
   - PVC (name e.g. `<user-id>-workspace-pvc`) with requested storage and StorageClass
   - Pod (e.g. `<user-id>-workspace-pod`) with security context (non-root, read-only root, capabilities dropped), env vars (e.g. `VLLM_ENDPOINT`, `VLLM_MODEL`, `USER_ID`, `USER_EMAIL`), volume mount for home/workspace
   - Service and optionally ServiceAccount + RoleBinding for least-privilege
4. **Status update**: Operator sets `status.phase` (Pending → Creating → Running / Failed), `status.podName`, `status.serviceEndpoint`, `status.lastAccessed`.
5. **Gateway proxies**: User’s WebSocket connection is proxied from Gateway to `http://<pod-ip or service>:7681` (ttyd). Traffic stays in-cluster; user only talks to Gateway.

## 4. Security Model

### 4.1 Network

- **NetworkPolicies**: Deny-all by default. Egress allowed only where needed (e.g. vLLM namespace, DNS, optional git/registry). No cross-user pod traffic.
- **Gateway**: Only component exposed to users; no direct access to workspace pods from outside the cluster.

### 4.2 Pod Security (Workspace Pods)

- **Non-root**: `runAsNonRoot: true`, `runAsUser: 1000`.
- **Read-only root**: `readOnlyRootFilesystem: true` with writable volume(s) for home/workspace.
- **No privilege escalation**: `allowPrivilegeEscalation: false`.
- **Capabilities**: `capabilities.drop: ["ALL"]`.
- **Seccomp**: `seccompProfile.type: RuntimeDefault`.
- No privileged mode, no host namespaces; workspace pods do not run Docker or require elevated rights.

### 4.3 RBAC

- **Operator**: ServiceAccount with minimal RBAC: manage `Workspace` CRs, Pods, PVCs, Services, ServiceAccounts, RoleBindings/Roles as needed.
- **Workspace Pods**: Each gets a dedicated ServiceAccount and RoleBinding granting only what that pod needs (e.g. no cluster-wide write).

### 4.4 Secrets and Config

- API keys or user-specific secrets are not stored in cluster Secrets for normal operation; user-provided config can live under `/workspace/.config` on the PVC. Gateway and operator do not log tokens or API keys.

## 5. Storage Strategy

- **One PVC per Workspace** (per user in a given namespace): name pattern e.g. `<user-id>-workspace-pvc`, RWO, size and StorageClass from `spec.resources.storage` and `spec.persistence.storageClass`.
- **Lifecycle**:
  - PVC is created with the Workspace and has an OwnerReference to the Workspace CR so it is deleted when the Workspace is deleted.
  - Data persists across pod restarts; pod spec mounts the PVC to a fixed path (e.g. `/workspace` or `/home/user`).
- **No shared storage** between users; each workspace has isolated persistent storage.

---

## Summary

- **Gateway**: OIDC + create/get Workspace CR + WebSocket proxy.
- **Operator**: Reconcile Workspace → Pod + PVC + Service (+ RBAC); update status.
- **Workspace Pod**: Dev environment + AI client (OpenCoder) talking to in-cluster vLLM.
- **Security**: NetworkPolicies, strict pod security, least-privilege RBAC, no secrets in env for sensitive user data.
- **Storage**: One RWO PVC per Workspace, owned by the CR, lifecycle tied to the CR.
