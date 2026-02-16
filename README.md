# DevPlane

DevPlane is a Kubernetes operator that provides **AI-powered development workspaces** for air-gapped and restricted environments. Developers authenticate via OIDC, get isolated terminal sessions with persistent storage, and a pre-configured AI coding assistant (OpenCoder) connected to an in-cluster vLLM endpoint.

## Project Overview

- **Operator**: Watches `Workspace` custom resources and creates per-user Pods, PVCs, and Services with strict security settings.
- **Gateway**: Handles OIDC login and proxies WebSocket traffic to the user’s workspace pod.
- **Workspace Pod**: Ubuntu-based container with ttyd, tmux, and OpenCoder, configured to use your vLLM service.

Use cases include DevOps in air-gapped environments, understanding codebases with AI, planning implementations, and data science with AI-assisted Python.

## Prerequisites

- **Kubernetes**: 1.27 or later (tested against 1.27, 1.28, 1.29).
- **vLLM**: A running vLLM (or compatible) service inside the cluster, e.g. in a dedicated namespace (e.g. `ai-system`), reachable at a URL like `http://vllm.ai-system.svc.cluster.local:8000`.
- **OIDC**: An OIDC-compatible IdP (e.g. Keycloak, Dex, Okta) for Gateway authentication.
- **Go**: 1.21+ for building the operator and gateway.
- **kubectl**: Configured for the target cluster.
- **Optional**: `kustomize`, `controller-gen`, `golangci-lint` for manifests, code generation, and linting (see Makefile targets).

## Quick Start

1. **Install CRDs and run the operator locally** (against current kubeconfig):

   ```bash
   make manifests
   make install                    # install CRDs (if you add an install target)
   make run                        # run operator in foreground
   ```

2. **Create a sample Workspace** (after ensuring the CRD is installed):

   ```bash
   kubectl apply -f config/samples/workspace_v1alpha1_workspace.yaml
   ```

3. **Deploy with Helm** (when the chart is ready):

   ```bash
   helm install workspace-operator deploy/helm/workspace-operator -n workspace-operator-system --create-namespace
   ```

4. **Configure Gateway** with your OIDC issuer, client ID, and client secret, and point users at the Gateway URL (e.g. `https://devplane.company.com`). The Gateway will create or look up a Workspace per user and proxy the terminal WebSocket to the workspace pod.

## Architecture Overview

See **[ARCHITECTURE.md](./ARCHITECTURE.md)** for:

- Component diagram (Gateway, Operator, Workspace Pods, vLLM)
- Authentication flow (OIDC → Gateway → Workspace creation)
- Data flow (user request → pod creation → WebSocket proxy)
- Security model (NetworkPolicies, RBAC, pod security)
- Storage strategy (one PVC per user, lifecycle)

## Development Setup

1. **Clone and dependencies**

   ```bash
   cd /path/to/DevPlane
   go mod download
   make tidy
   ```

2. **Generate code and manifests**

   ```bash
   make generate
   make manifests
   ```

3. **Run tests and lint**

   ```bash
   make test
   make lint
   ```

4. **Build**

   ```bash
   make build
   ```

5. **Run operator locally**

   ```bash
   make run
   ```

6. **Docker images**

   ```bash
   make docker-build
   ```

7. **Deploy to cluster** (after setting `IMG` if needed)

   ```bash
   make deploy
   ```

### Makefile Targets

| Target          | Description                    |
|-----------------|--------------------------------|
| `make manifests`| Generate CRDs and RBAC         |
| `make generate` | Generate deepcopy etc.         |
| `make test`     | Run tests                      |
| `make build`    | Build operator binary          |
| `make docker-build` | Build operator/gateway/workspace images |
| `make deploy`   | Deploy with kustomize          |
| `make lint`     | Run golangci-lint              |

### Directory Layout

- `api/v1alpha1/` — Workspace CRD types and generated code
- `controllers/` — Workspace reconciliation logic
- `cmd/gateway/` — Gateway service entrypoint
- `pkg/gateway/` — Gateway HTTP handlers (auth, proxy, lifecycle)
- `pkg/security/` — RBAC and NetworkPolicy helpers
- `config/` — CRD, RBAC, manager, samples
- `deploy/helm/workspace-operator/` — Helm chart
- `hack/` — Boilerplate and workspace entrypoint script

## Troubleshooting

- **CRD not found**: Run `make manifests` and install the CRDs from `config/crd/bases/` (or via `config/default` with `make deploy`).
- **Operator not reconciling**: Check operator logs and that the manager has RBAC to read/write Workspaces, Pods, PVCs, and Services.
- **Pod stays Pending**: Check PVC binding and StorageClass; ensure no resource quota or scheduler issues.
- **Gateway cannot create Workspace**: Ensure the Gateway’s ServiceAccount has RBAC to create/update Workspace CRs and to read their status.

## License

Apache 2.0 (see hack/boilerplate.go.txt).
