# Helm chart upgrades

Use this file for **chart-specific** release notes and value changes. For full procedures (CRDs, rollback, observability), see the repository [deployment guide](../../docs/deployment.md) (section *Upgrade & Rollback*).

## General procedure

```bash
helm upgrade <release-name> <chart> \
  -n <namespace> \
  -f my-values.yaml \
  --atomic \
  --timeout 5m
```

**CRDs:** Helm does not upgrade CRDs in `crds/` automatically on upgrade. When the `Workspace` CRD schema changes, apply `crds/` manually **before** `helm upgrade`:

```bash
kubectl apply -f crds/
```

**Rollback:** `helm rollback` does not revert CRD changes; restore the previous CRD YAML if needed.

---

## `workspace-operator` chart

### 1.1.7

- **Default resource requests/limits** for `operator` and `gateway` were raised to production-oriented values (see `values.yaml` comments). If your namespace has a **small ResourceQuota**, compare quotas to the new defaults and lower `operator.resources` / `gateway.resources` in your values file if pods stay `Pending`.
- Chart `version` is **1.1.7**; container images still default to **`appVersion`** in `Chart.yaml` (pin `operator.image.tag` / `gateway.image.tag` / `workspace.image.tag` explicitly in production).

### Earlier releases

No prior breaking value renames are recorded here; always diff `values.yaml` between chart versions when upgrading across multiple minors.
