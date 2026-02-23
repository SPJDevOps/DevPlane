# Local Development Guide

This guide covers two levels of local testing:

- **Part 1** — smoke-test the workspace image with Docker (no Kubernetes needed).
- **Part 2** — run the full DevPlane stack in a local KIND cluster.

---

## Prerequisites

| Tool | Install |
|------|---------|
| Docker | [docs.docker.com](https://docs.docker.com/get-docker/) |
| kind | `brew install kind` / [kind.sigs.k8s.io](https://kind.sigs.k8s.io/) |
| kubectl | `brew install kubectl` |
| helm | `brew install helm` |
| make | usually pre-installed; `xcode-select --install` on macOS |

`kind`, `kubectl`, and `helm` are only needed for Part 2.

---

## Part 1 — Testing the workspace image with Docker

### 1.1 Build the image

```bash
# Build for the host architecture (swap amd64 ↔ arm64 as needed)
docker build -f Dockerfile.workspace \
  --build-arg TARGETARCH=amd64 \
  -t devplane-workspace:local .
```

On Apple Silicon, replace `amd64` with `arm64` (or omit `--build-arg TARGETARCH` to use the default).

### 1.2 Run the container

```bash
# A named volume makes /workspace persistent across restarts, simulating the PVC
docker run --rm \
  -p 7681:7681 \
  -v devplane-workspace-data:/workspace \
  -e USER_ID=localdev \
  -e USER_EMAIL=dev@localhost \
  -e AI_PROVIDERS_JSON='[{"name":"local","endpoint":"http://host.docker.internal:11434","models":["llama3"]}]' \
  devplane-workspace:local
```

`AI_PROVIDERS_JSON` is a JSON array of provider objects, each with a `name` (used as the opencode provider key), an `endpoint` (OpenAI-compatible base URL), and a `models` list. It accepts any reachable OpenAI-compatible API — Ollama, LM Studio, vLLM, a hosted service. You can supply multiple providers, e.g.:

```bash
-e AI_PROVIDERS_JSON='[
  {"name":"local","endpoint":"http://host.docker.internal:11434","models":["llama3"]},
  {"name":"cloud","endpoint":"https://api.openai.com","models":["gpt-4o"]}
]'
```

If you don't have a local LLM running, pass a placeholder value — the shell and all other dev tools still work; opencode will simply report a connection error.

### 1.3 Open the browser terminal

Navigate to **http://localhost:7681** in your browser. You should see a full terminal session inside the workspace container.

### 1.4 Smoke tests

Run these in a separate terminal to verify the image contents without starting a full ttyd session:

```bash
# Confirm opencode is installed and executable
docker run --rm devplane-workspace:local opencode --version

# Confirm Starship prompt is present
docker run --rm devplane-workspace:local starship --version

# Confirm zsh-autosuggestions is installed
docker run --rm devplane-workspace:local \
  zsh -c 'source /usr/share/zsh-autosuggestions/zsh-autosuggestions.zsh && echo ok'
```

All three should exit 0 and print a version string / `ok`.

### 1.5 Iterating

After editing `Dockerfile.workspace` or `hack/entrypoint.sh`:

```bash
docker build -f Dockerfile.workspace --build-arg TARGETARCH=amd64 -t devplane-workspace:local .
docker run --rm -p 7681:7681 -v devplane-workspace-data:/workspace \
  -e USER_ID=localdev -e USER_EMAIL=dev@localhost \
  -e AI_PROVIDERS_JSON='[{"name":"local","endpoint":"http://host.docker.internal:11434","models":["llama3"]}]' \
  devplane-workspace:local
```

There is no hot-reload; you must rebuild and re-run. The named volume preserves `/workspace` content between rebuilds.

---

## Part 2 — Full-stack testing with KIND

This section walks through running the operator, gateway, and a workspace pod inside a local Kubernetes cluster.

> **Note:** Step 2.6 uses `kubectl port-forward` to bypass the gateway and test the operator + workspace pod directly. For the full gateway login flow (browser → Keycloak → terminal), set `gateway.oidc.clientSecret` and `gateway.oidc.redirectURL` as well — see the [AI Provider Configuration and OIDC setup](../docs/deployment.md#oidc-configuration) section in the deployment guide.

### 2.1 Create the cluster

The config at `.kind/kind-config.yaml` maps host ports 8080→80 and 8443→443 on the KIND node:

```bash
kind create cluster --name devplane --config .kind/kind-config.yaml
```

Verify:

```bash
kubectl cluster-info --context kind-devplane
kubectl get nodes
```

### 2.2 Build and load images

```bash
# Build all three images (uses local Docker daemon)
make docker-build
# Produces: workspace-operator:latest, workspace-gateway:latest, workspace:latest

# Load images into the KIND cluster (no registry needed)
kind load docker-image workspace-operator:latest --name devplane
kind load docker-image workspace-gateway:latest  --name devplane
kind load docker-image workspace:latest          --name devplane
```

### 2.3 Install Dex (OIDC IdP)

`.kind/dex-values.yaml` configures Dex as a NodePort service on port 32000 with a static test user.

```bash
helm repo add dex https://charts.dexidp.io
helm repo update
helm install dex dex/dex \
  --namespace dex --create-namespace \
  -f .kind/dex-values.yaml
```

Wait for Dex to be ready:

```bash
kubectl rollout status deployment/dex -n dex
```

Dex issues tokens at `http://172.21.0.2:32000` (Docker bridge IP + NodePort, as set in `dex-values.yaml`). The default test user is `dev@example.com` with password `password`.

### 2.4 Install DevPlane via Helm

Use `pullPolicy=Never` so KIND serves the images you loaded in step 2.2:

```bash
helm install workspace-operator deploy/helm/workspace-operator \
  --namespace workspace-operator-system \
  --create-namespace \
  --set operator.image.repository=workspace-operator \
  --set operator.image.tag=latest \
  --set operator.image.pullPolicy=Never \
  --set gateway.image.repository=workspace-gateway \
  --set gateway.image.tag=latest \
  --set gateway.image.pullPolicy=Never \
  --set workspace.image.repository=workspace \
  --set workspace.image.tag=latest \
  --set workspace.image.pullPolicy=Never \
  --set gateway.oidc.issuerURL=http://172.21.0.2:32000 \
  --set gateway.oidc.clientID=devplane \
  --set gateway.oidc.clientSecret=devsecret \
  --set gateway.oidc.redirectURL=http://localhost:8080/callback \
  --set 'workspace.ai.providers[0].name=local' \
  --set 'workspace.ai.providers[0].endpoint=http://host.docker.internal:11434' \
  --set 'workspace.ai.providers[0].models[0]=llama3'
```

`workspace.ai.providers` is a list — each entry needs a `name`, an `endpoint` (any OpenAI-compatible URL), and at least one model in `models`. For multiple providers use additional `[1]`, `[2]` indexes. If you don't have a local LLM, pass placeholder values — the operator and workspace pod will start regardless; opencode will report a connection error inside the terminal.

### 2.5 Verify the operator

```bash
kubectl get pods -n workspace-operator-system
```

Both the operator and gateway pods should reach `Running` (gateway may stay in a non-ready state — this is expected while it is a placeholder).

### 2.6 Create a test Workspace and access it

```bash
# Create the workspaces namespace if the chart didn't
kubectl create namespace workspaces --dry-run=client -o yaml | kubectl apply -f -

# Apply a minimal Workspace CR
kubectl apply -f - <<EOF
apiVersion: workspace.devplane.io/v1alpha1
kind: Workspace
metadata:
  name: test-workspace
  namespace: workspaces
spec:
  userID: "localdev"
  userEmail: "dev@localhost"
  aiConfig:
    providers:
      - name: local
        endpoint: "http://host.docker.internal:11434"
        models:
          - llama3
EOF

# Watch it progress: Pending → Creating → Running
kubectl get workspace test-workspace -n workspaces -w
```

Once the workspace is `Running`, connect directly to the pod (bypassing the gateway):

```bash
kubectl port-forward -n workspaces pod/localdev-workspace-pod 7681:7681
```

Open **http://localhost:7681** in your browser for the terminal session.

### 2.7 Teardown

```bash
# Delete the KIND cluster (removes all resources)
kind delete cluster --name devplane

# Remove the workspace data volume from Part 1 (if used)
docker volume rm devplane-workspace-data
```

---

## Tips

- **Image not found in KIND**: Confirm you ran `kind load docker-image` after every `docker build`. KIND does not pull from the local Docker daemon automatically.
- **172.21.0.2 is wrong**: The Docker bridge IP may differ on your machine. Run `docker network inspect kind | jq '.[0].IPAM.Config[0].Gateway'` to find the correct IP and update `dex-values.yaml` accordingly.
- **Ollama on macOS**: `host.docker.internal` resolves to the macOS host from inside KIND nodes. Start Ollama on the host and set `workspace.ai.providers[0].endpoint=http://host.docker.internal:11434`.
- **Workspace stuck in `Creating`**: Check `kubectl describe pod localdev-workspace-pod -n workspaces` for image pull or scheduling errors.
