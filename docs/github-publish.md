# Publishing `main` to GitHub

This runbook covers how to get commits from a trusted workstation or automation onto `origin` for [SPJDevOps/DevPlane](https://github.com/SPJDevOps/DevPlane). It complements the existing GitHub Actions workflows (CI, image build, Helm release), which already run **after** commits exist on GitHub.

## What already happens on GitHub

| Trigger | Workflow | Purpose |
|--------|----------|---------|
| Push / PR to `main` | [ci.yml](../.github/workflows/ci.yml) | Lint, unit + integration tests, coverage gate |
| Push to `main` or `v*` tag | [build-images.yml](../.github/workflows/build-images.yml) | Build and push images to `ghcr.io` |
| Push `v*` tag | [release-chart.yml](../.github/workflows/release-chart.yml) | Package Helm chart, GitHub Release, `gh-pages` index |

None of these jobs can publish commits that exist **only** in an internal mirror or an AI/agent workspace that has no GitHub credentials. Someone (or a credentialed runner) must `git push` first.

## Minimum manual path (credentialed machine)

From a clone that has the commits you intend to publish:

1. **Confirm branch and remotes**
   ```bash
   git status
   git remote -v
   git fetch origin
   git log --oneline origin/main..HEAD
   ```
2. **Push to GitHub**
   ```bash
   git push origin main
   ```
3. **Verify** in the GitHub Actions tab that CI and image workflows ran for the new commit.

If `git push` fails with authentication errors, configure one of the options below and retry.

### HTTPS with a personal access token (PAT)

- Create a **fine-grained** or **classic** PAT with `contents: write` (and `packages: write` if you also push images outside Actions).
- Prefer **SSH** for day-to-day pushes if your org allows it; rotate PATs on a schedule.
- Store the token in the OS credential helper or `~/.netrc` — never commit it.

### SSH (deploy key or user key)

- **User SSH key**: add your public key to GitHub → *Settings → SSH and GPG keys*. Use remote URL `git@github.com:SPJDevOps/DevPlane.git`.
- **Deploy key** (read/write) on the repo: suitable for a single automation host; pair with a dedicated Unix user on that host.

### Protected branches

If `main` is protected:

- Allow your user or a **machine user** / GitHub App installation to bypass or satisfy required reviews.
- Ensure required status checks include the jobs you care about (`CI`, etc.).

## Automation patterns

### 1. GitHub-hosted runner (default)

Pushes that land on `main` already trigger CI and builds using `GITHUB_TOKEN`. No extra secret is required for *downstream* workflows.

Use a **classic** or fine-grained PAT stored as a repository secret only when a workflow must act **across repos** or use APIs the default token cannot access.

### 2. `workflow_dispatch` manual CI

The CI workflow accepts manual runs from *Actions → CI → Run workflow*. Use this to re-validate `main` (or a chosen branch, if you extend inputs) without an empty commit.

### 3. Self-hosted / air-gapped runners

If corporate policy forbids outbound GitHub from developer laptops but permits a **single** egress path:

- Register a [self-hosted runner](https://docs.github.com/actions/hosting-your-own-runners) in a DMZ or build zone.
- Run `git push` from a job on that runner using a PAT or deploy key in **Actions secrets**, *or* mirror bundles from the secure zone into the runner workspace before push.

Automated agents (including sandboxed coding agents) often **cannot** reach `github.com` or store long-lived tokens. Treat them as **editors only**: produce commits locally, then a human or approved CI system pushes.

### 4. Offline transfer (no direct push from secure enclave)

If the secure network cannot talk to GitHub at all:

1. On the disconnected side: `git bundle create devplane-main.bundle main` or export patches.
2. Transfer the artifact over your approved path (USB, cross-domain transfer, ticket attachment workflow).
3. On a connected machine: `git fetch devplane-main.bundle main:incoming && git merge incoming` (or apply patches), then `git push origin main`.

## Org constraints checklist

- [ ] Can this environment reach `github.com:443` (or your GitHub Enterprise host)?
- [ ] Are PATs / SSH keys allowed by security policy, and where may they live?
- [ ] Is `main` protected, and who is allowed to push or merge?
- [ ] Do image/chart releases require tags (`v*`) after `main` is green?

## Related documentation

- [local-development.md](./local-development.md) — full stack and tests on a workstation
- [deployment.md](./deployment.md) — Helm install and cluster operations
