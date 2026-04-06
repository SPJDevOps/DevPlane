# Versioned releases (charts, images, GitHub)

This document is the **single runbook** for cutting DevPlane releases: semver tags, Helm `Chart.yaml`, container images on GHCR, GitHub Releases, and promoting artifacts to enterprise mirrors.

Related automation:

| Trigger | Workflow | Outcome |
|--------|----------|---------|
| Push to `main` | [build-images.yml](../.github/workflows/build-images.yml) | Images: `latest`, `sha-<short>` |
| Push `v*` tag | [build-images.yml](../.github/workflows/build-images.yml) | Images: `X.Y.Z`, `X.Y`, `X`, `latest` (semver patterns) |
| Push `v*` tag | [release-chart.yml](../.github/workflows/release-chart.yml) | Helm `.tgz`, GitHub Release asset, `gh-pages` index |

Pushing commits to GitHub is covered in [github-publish.md](./github-publish.md).

---

## Versioning model

| Artifact | Format | Meaning |
|----------|--------|---------|
| **Git tag** | `vX.Y.Z` | Triggers image build + chart package. Use [Semantic Versioning](https://semver.org/) with a `v` prefix. |
| **Helm chart `version`** | `X.Y.Z` (no `v`) | Chart package version (what `helm install --version` selects). Bumps when chart templates, default values, or CRDs packaged in the chart change. |
| **`appVersion` in `Chart.yaml`** | `X.Y.Z` (quoted string, no `v`) | Default **container image tag** when `values.yaml` leaves `operator.image.tag` / `gateway.image.tag` / `workspace.image.tag` empty. Should match the images built for that release. |
| **GHCR images** | `ghcr.io/spjdevops/devplane/<name>:X.Y.Z` | Same semver as `appVersion` for a normal release. |

**Unified release (typical):** `Chart.yaml` `version`, `appVersion`, and Git tag `vX.Y.Z` all share the same `X.Y.Z`. After you push tag `v1.2.0`, Actions builds images `…:1.2.0` and chart-releaser publishes chart `1.2.0` defaulting to those images.

**Chart-only hotfix:** bump only `version` (e.g. `1.2.1`) when templates or defaults change but binaries stay on the previous build. Keep `appVersion` at the last shipped image (e.g. `1.2.0`). Before tagging, run `ALLOW_CHART_APP_MISMATCH=1 make release-verify` so the check documents intent (see below).

---

## Cadence

- **Default:** release **when ready** — no fixed calendar. Prefer small, frequent semver bumps over batching unrelated changes.
- **Patch (`X.Y.Z+1`):** bugfixes, doc-only chart tweaks, security patches.
- **Minor (`X.Y+1.0`):** additive behaviour, new Helm values, non-breaking CRD additions.
- **Major (`X+1.0.0`):** breaking Helm values, breaking CRD or API changes, or explicit product milestones.

`main` should stay green (CI passing) at the tip; tags point at known-good commits.

---

## Maintainer checklist (full release)

Complete these **in order** on a clean branch based on `main` (or merge to `main` first, then tag from the merge commit).

1. **Changelog** — Move items from `## [Unreleased]` in [CHANGELOG.md](../CHANGELOG.md) into `## [X.Y.Z] — YYYY-MM-DD` with sections *Added* / *Changed* / *Fixed* as needed ([Keep a Changelog](https://keepachangelog.com/en/1.0.0/)).
2. **Helm** — Edit [deploy/helm/workspace-operator/Chart.yaml](../deploy/helm/workspace-operator/Chart.yaml):
   - `version:` = chart semver `X.Y.Z`
   - `appVersion:` = same `X.Y.Z` (string) for a unified release
3. **Verify** — `make release-verify` (passes when `version` matches `appVersion`, or set `ALLOW_CHART_APP_MISMATCH=1` for chart-only; see Makefile).
4. **CI** — Open PR if required by branch protection; wait for CI green.
5. **Tag** — `git tag -a vX.Y.Z -m "Release vX.Y.Z"` and `git push origin vX.Y.Z` (or equivalent signed tag policy your org uses).
6. **Watch Actions** — Confirm [build-images.yml](../.github/workflows/build-images.yml) and [release-chart.yml](../.github/workflows/release-chart.yml) succeed for the tag.
7. **GitHub Release notes** — chart-releaser creates the release and attaches the chart `.tgz`. Edit the release description:
   - Paste the **CHANGELOG** section for `X.Y.Z` (users rely on human-readable notes).
   - Optionally add image list:
     - `ghcr.io/spjdevops/devplane/workspace-operator:X.Y.Z`
     - `ghcr.io/spjdevops/devplane/workspace-gateway:X.Y.Z`
     - `ghcr.io/spjdevops/devplane/workspace:X.Y.Z`
8. **Consumers** — Update README example pins if you maintain literal version examples (optional follow-up PR).

---

## Enterprise / air-gapped mirror promotion

Images are public on GHCR; many teams **copy** them into a private registry before install.

1. Pull by **immutable tag** `X.Y.Z` (or digest for stricter policy).
2. Retag and push to your registry; set `operator.image`, `gateway.image`, and `workspace.image` in Helm values accordingly.
3. Install from your mirror Helm repo or vendor the chart `.tgz` from the GitHub Release asset.

See [README.md](../README.md) (*Air-gapped clusters*) and [deployment.md](./deployment.md) for cluster operations.

---

## Discoverability

- **Charts:** Helm repo hosted via GitHub Pages — `helm repo add devplane https://spjdevops.github.io/DevPlane` (see [README](../README.md)).
- **Tags:** [GitHub Releases](https://github.com/SPJDevOps/DevPlane/releases) list `v*` tags and chart packages.

---

## Coordination with CI smoke

Coordinate with chart/image smoke tests ([DEV-29](/DEV/issues/DEV-29)) so tagged artifacts remain covered by automation where practical; this runbook does not duplicate workflow YAML.
