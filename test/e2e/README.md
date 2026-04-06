# E2E and integration test plan

This directory implements **Phase 3** automated checks against a **real Kubernetes API** (not envtest). Use it to catch regressions in the operator reconcile path and—optionally—the gateway → workspace → terminal path.

## What is covered

| Layer | Test | Needs |
|-------|------|--------|
| Operator | `TestWorkspaceReconciliation` | Running operator, CRDs, StorageClass that can bind PVCs |
| Operator | `TestWorkspaceDeletion`, `TestWorkspaceInvalidSpec` | Same |
| Gateway + IdP | `TestGatewayWorkspaceAPISmoke` | Deployed gateway, valid OIDC **ID token** (`E2E_ID_TOKEN`) |

Source: [`e2e_test.go`](./e2e_test.go).

## Prerequisites

- Kubernetes **1.27+** with `kubectl` and a working kubeconfig (`KUBECONFIG` or `~/.kube/config`).
- **Workspace CRD** installed (`kubectl apply -f deploy/helm/workspace-operator/crds/` or `make install`).
- **Operator** running and watching the namespace you test against (default tests use namespace `e2e-workspaces`).
- For full pod scheduling: a default **StorageClass** (or one that satisfies workspace PVCs) and enough quota for small test PVCs/pods.
- **No in-cluster LLM is required** for the operator tests; AI endpoints in the spec can point at a non-existent Service as long as the pod becomes Ready (image pull + ttyd).

## Install from Helm (recommended validation path)

Aligns with production installs and [chart upgrade notes](../../deploy/helm/workspace-operator/UPGRADING.md).

```bash
helm upgrade --install workspace-operator ../../deploy/helm/workspace-operator \
  -n workspace-operator-system --create-namespace \
  -f my-values.yaml \
  --atomic --timeout 5m
```

Ensure the operator is configured to reconcile workspaces in `e2e-workspaces` (or change `e2eNamespace` in tests — currently fixed in code). Typically the operator watches all namespaces with Workspace CRs; confirm RBAC allows the operator to manage resources in the test namespace.

After install, follow post-install checks in the rendered **`NOTES.txt`** (rollout status, port-forward health checks).

## How to run

```bash
# From repo root — integration tests only (default unit test suite excludes -tags e2e)
make test-e2e
# equivalent:
go test -v -tags e2e ./test/e2e/... -timeout 10m
```

### Gateway smoke (OIDC → `/api/workspace` → `ttydReady`)

Requires a **real** token the gateway accepts (same audience/issuer as configured).

```bash
export E2E_GATEWAY_URL=https://devplane.example.com   # no trailing slash
export E2E_ID_TOKEN=<OIDC id_token for a test user>
make gateway-smoke
```

## Limitations (explicit)

- **Not run in default CI** unless you add a cluster job: tests need a live API server and (for reconciliation) a working operator.
- **Kind/minikube**: supported in principle; ensure storage and image pull policy (`IfNotPresent` / preloaded images) match your setup.
- **Gateway smoke** is not a substitute for a browser WebSocket test; it validates HTTP auth + workspace status polling only. Deeper terminal checks use `wscat` manually (see main [README](../../README.md)).
- **Fake OIDC**: the Go tests do not start a mock IdP; use Dex/Keycloak in a dev cluster or skip gateway tests.

## References

- Deployment guide: [docs/deployment.md](../../docs/deployment.md) (upgrade, rollback, observability).
- Helm chart: [deploy/helm/workspace-operator](../../deploy/helm/workspace-operator/).
