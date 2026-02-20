# DevPlane

DevPlane is a Kubernetes operator that provides **AI-powered development workspaces** for air-gapped and restricted environments. Developers authenticate via OIDC, get isolated terminal sessions with persistent storage, and a pre-configured AI coding assistant (opencode) connected to any OpenAI-compatible LLM endpoint.

## Project Overview

- **Operator**: Watches `Workspace` custom resources and creates per-user Pods, PVCs, and Services with strict security settings.
- **Gateway**: Handles OIDC login and proxies WebSocket traffic to the user’s workspace pod.
- **Workspace Pod**: Ubuntu-based container with ttyd, tmux, and opencode, pre-configured to connect to any OpenAI-compatible LLM endpoint.

Use cases include DevOps in air-gapped environments, understanding codebases with AI, planning implementations, and data science with AI-assisted Python.

## Prerequisites

- **Kubernetes**: 1.27 or later.
- **LLM endpoint (optional)**: Any OpenAI-compatible API reachable from workspace pods — vLLM, Ollama, LM Studio, or a hosted service. Without one, the shell and all dev tools still work; `opencode` will simply report a connection error.
- **OIDC**: An OIDC-compatible IdP (e.g. Keycloak, Dex, Okta) for Gateway authentication.
- **Go**: 1.21+ for building the operator and gateway.
- **kubectl**: Configured for the target cluster.
- **Optional**: `kustomize`, `controller-gen`, `golangci-lint` for manifests, code generation, and linting (see Makefile targets).

## Published Releases

Container images (multi-arch `linux/amd64` + `linux/arm64`) are published to the GitHub Container Registry:

| Image | Pull reference |
|-------|----------------|
| Operator | `ghcr.io/spjdevops/devplane/workspace-operator:1.0.0` |
| Gateway  | `ghcr.io/spjdevops/devplane/workspace-gateway:1.0.0`  |
| Workspace | `ghcr.io/spjdevops/devplane/workspace:1.0.0`         |

The Helm chart is published to the GitHub Pages Helm repository:

```
https://spjdevops.github.io/DevPlane
```

[index.yaml](https://spjdevops.github.io/DevPlane/index.yaml) · [GitHub Releases](https://github.com/SPJDevOps/DevPlane/releases)

## Quick Start

1. **Add the Helm repo and install**:

   ```bash
   helm repo add devplane https://spjdevops.github.io/DevPlane
   helm repo update
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

2. **Create a sample Workspace** (after the operator is running):

   ```bash
   kubectl apply -f config/samples/workspace_v1alpha1_workspace.yaml
   ```

3. **Configure Gateway** with your OIDC issuer, client ID, and client secret, and point users at the Gateway URL (e.g. `https://devplane.company.com`). The Gateway will create or look up a Workspace per user and proxy the terminal WebSocket to the workspace pod.

4. **Run the operator locally** against your current kubeconfig instead of deploying:

   ```bash
   make manifests
   make install   # installs CRDs
   make run       # runs operator in foreground
   ```

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

   For a walkthrough of testing the workspace image with Docker and the full stack with KIND, see **[docs/local-development.md](./docs/local-development.md)**.

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

## NetworkPolicy — Configurable Egress Ports

Each workspace pod gets three NetworkPolicies: deny-all, ingress-from-gateway, and an egress policy. The egress policy allows:

- DNS (port 53 UDP/TCP) to `kube-system`
- Any port to in-cluster LLM namespaces (e.g. `ai-system`)
- A configurable list of TCP ports to external IPs (`0.0.0.0/0`)

The default external port list is `22, 80, 443, 5000, 8000, 8080, 8081, 11434`:

| Port | Purpose |
|------|---------|
| 22 | Git over SSH |
| 80, 443 | HTTP / HTTPS |
| 5000 | Self-hosted Docker registry |
| 8000 | vLLM (bare-metal or in-cluster) |
| 8080, 8081 | Nexus, Artifactory, generic alt-HTTP |
| 11434 | Ollama |

Override operator-wide via `values.yaml` (`workspace.ai.egressPorts`) or per-workspace via `spec.aiConfig.egressPorts`. Changes take effect on the next reconcile without requiring deletion of the existing policy.

## Troubleshooting

- **CRD not found**: Run `make manifests` and install the CRDs from `config/crd/bases/` (or via `config/default` with `make deploy`).
- **Operator not reconciling**: Check operator logs and that the manager has RBAC to read/write Workspaces, Pods, PVCs, and Services.
- **Pod stays Pending**: Check PVC binding and StorageClass; ensure no resource quota or scheduler issues.
- **Gateway cannot create Workspace**: Ensure the Gateway’s ServiceAccount has RBAC to create/update Workspace CRs and to read their status.
- **OIDC TLS errors / private CA**: If your IdP (e.g. Keycloak) uses a certificate signed by an internal CA, mount a CA bundle for both the gateway and workspace pods — see [Private CA certificates](./docs/deployment.md#private--self-signed-ca-certificates) in the deployment guide.
- **Workspace cannot reach external service**: Verify the required port is in `egressPorts` (`kubectl get networkpolicy <userid>-workspace-egress -n workspaces -o yaml`).
- **Workspace cannot reach in-cluster LLM**: Ensure the LLM namespace is listed in `egressNamespaces`.

## License

Apache 2.0 (see hack/boilerplate.go.txt).
