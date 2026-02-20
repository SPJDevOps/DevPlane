# Changelog

All notable changes to DevPlane are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
DevPlane uses [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

---

## [1.0.0] — 2026-02-20

### Phase 1 — Operator (workspace lifecycle)

- **Workspace CRD** (`workspace.devplane.io/v1alpha1`) with typed `WorkspacePhase` constants (`Pending`, `Creating`, `Running`, `Failed`, `Stopped`).
- **Reconciler** creates and owns a Pod, PVC, and headless Service per workspace CR.  All resources carry OwnerReferences for automatic cascade deletion.
- **Security hardening**: per-user ServiceAccount + least-privilege Role/RoleBinding; deny-all NetworkPolicy with dynamic egress allowlist (namespace + port level); pod security context (`runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, `capabilities: drop ALL`, `seccompProfile: RuntimeDefault`).
- **Readiness probe** on the workspace container (TCPSocket on port 7681) ensures Kubernetes waits for ttyd to be listening before marking the pod Ready.
- **Idle-timeout**: configurable duration; operator deletes the pod and sets `Stopped` phase after inactivity.
- **Image upgrade**: pod is deleted and recreated when the desired workspace image changes.

### Phase 2 — Gateway (OIDC auth + WebSocket proxy)

- **OIDC token validation** with LRU cache (bounded to 10 000 entries, 5-minute TTL) to reduce IdP round-trips.
- **Workspace lifecycle API**: `EnsureWorkspace` gets or creates a Workspace CR and waits for it to reach `Running`.
- **Stopped workspace self-service recovery**: when a workspace is `Stopped` (idle timeout), the gateway automatically clears the phase in the CR so the operator recreates the pod — no manual intervention required.
- **Activity tracking**: `LastAccessed` is updated at connection establishment and then rate-limited (once per minute) on each proxied WebSocket frame, so the idle-timeout controller sees genuine activity.
- **WebSocket proxy** with bidirectional frame forwarding and clean close-frame propagation.
- **Graceful shutdown**: gateway server runs in a goroutine; on SIGTERM/SIGINT it drains active connections for up to 30 seconds before exiting.

### v1 Stabilisation

- Typed `WorkspacePhase` constants (`WorkspacePhaseRunning`, `WorkspacePhaseStopped`, etc.) replace raw string literals throughout the codebase.
- `handleWS` and `extractToken` covered by `httptest`-based unit tests.
- Integration test CI job runs the full `envtest` suite (etcd + kube-apiserver) and enforces the 70% coverage threshold.
- Helm `values.yaml` image tags default to `""` (resolved via `| default .Chart.AppVersion`) instead of `latest`.
- `docs/deployment.md` quick-start example updated to the current CRD schema (`spec.user.id`, `spec.user.email`).

---

## Pre-release history

| Phase | Description |
|-------|-------------|
| Phase 1 | Operator — watches Workspace CRs, reconciles Pod + PVC + Service |
| Phase 2 | Gateway — OIDC auth, workspace lifecycle API, WebSocket proxy to ttyd |
