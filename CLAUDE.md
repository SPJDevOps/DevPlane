# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

DevPlane is a Kubernetes operator (Go, kubebuilder v3) that provides AI-powered development workspaces for air-gapped environments. Users authenticate via OIDC, get isolated terminal sessions with persistent storage, and a pre-configured AI assistant (OpenCoder) connected to an in-cluster vLLM endpoint.

## Build & Development Commands

```bash
make build              # Compile operator binary to bin/manager
make test               # Run all tests with coverage (envtest-based)
make lint               # Run golangci-lint
make fmt                # Format Go code
make vet                # Run go vet
make manifests          # Generate CRD and RBAC YAML from code annotations
make generate           # Generate deepcopy methods for CRD types
make tidy               # Tidy go.mod
make docker-build       # Build all 3 Docker images (operator, gateway, workspace)
make install            # Install CRDs to cluster
make deploy             # Deploy operator via Kustomize
make run                # Run operator locally against current kubeconfig
```

Run a single test:
```bash
go test -run TestIsPodReady ./controllers
go test -run TestBuildPod ./pkg/workspace
```

After modifying `api/v1alpha1/workspace_types.go`, always run `make manifests generate` to regenerate CRD YAML and deepcopy methods.

## Architecture

Three components, each with its own Dockerfile:

1. **Operator** (`main.go`, `controllers/`, `pkg/workspace/`) — Watches `Workspace` CRDs, reconciles Pod + PVC + headless Service per workspace. Phase 1 complete.
2. **Gateway** (`cmd/gateway/main.go`, `pkg/gateway/`) — OIDC auth, workspace lifecycle API, WebSocket proxy to pods. Phase 2, currently placeholder.
3. **Workspace Pod** (`Dockerfile.workspace`) — Ubuntu 24.04 container with ttyd, tmux, git, OpenCoder. Non-root (UID 1000), read-only root filesystem.

**User flow:** Browser → Gateway (OIDC) → creates/gets Workspace CR → Operator provisions Pod+PVC+Service → Gateway proxies WebSocket to Pod's ttyd (port 7681).

**CRD:** `Workspace` (`workspace.devplane.io/v1alpha1`) — spec contains user info, resource requests, AI config, persistence settings. Status tracks phase (Pending→Creating→Running/Failed/Stopped), podName, serviceEndpoint.

## Key Conventions

- All operator-created resources MUST have OwnerReferences to parent Workspace CR
- Labels: `app: workspace`, `user: <user-id>`, `managed-by: workspace-operator`
- Resource naming: `<user-id>-workspace-pod`, `<user-id>-workspace-pvc`
- Errors: wrap with `fmt.Errorf("context: %w", err)`
- Structured logging with logr (`log.Info`, `log.Error`)
- Use typed clients (`client.Client`), not dynamic clients
- Business logic goes in `pkg/`, not in `controllers/`
- Stateless reconciliation — no state stored in operator memory

## Security Requirements (Critical)

All workspace pods must have:
- `runAsNonRoot: true`, `runAsUser: 1000`
- `readOnlyRootFilesystem: true` (except mounted volumes)
- `allowPrivilegeEscalation: false`
- `capabilities: drop: ["ALL"]`
- `seccompProfile: type: RuntimeDefault`
- No Docker daemon access, no privileged mode

NetworkPolicies: deny-all default, allow egress only to vLLM namespace. RBAC: least-privilege ServiceAccounts.

## Anti-Patterns

- Don't use sleeps for synchronization (use watch/informers)
- Don't create resources without OwnerReferences
- Don't store state in operator (stateless reconciliation)
- Don't log sensitive data (API keys, tokens)
- Don't use `latest` tags in production images
- Don't use string concatenation for resource names (use `fmt.Sprintf`)

## Testing

- Unit tests with standard `testing` package, integration tests with `envtest` (fake K8s API)
- 70%+ coverage goal
- Run `make test` and `make lint` before commits
- Test files: `controllers/workspace_controller_test.go`, `pkg/workspace/resources_test.go`

## Environment Variables (Workspace Pod)

`VLLM_ENDPOINT`, `VLLM_MODEL`, `USER_EMAIL`, `USER_ID` — injected by operator into workspace pods.

## Target: Kubernetes 1.27+ (stable APIs only)
