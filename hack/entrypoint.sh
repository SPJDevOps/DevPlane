#!/usr/bin/env bash
# Workspace pod entrypoint.
# $HOME=/workspace (uid 1000 in /etc/passwd) which is the mounted PVC.
# All user data written here persists across pod restarts.
set -euo pipefail

# ── SSH / kube / gitconfig permissions ───────────────────────────────────────
# The PVC does not guarantee Unix permissions are preserved, so fix them on
# every start. SSH will silently ignore keys with wrong permissions.
if [ -d "${HOME}/.ssh" ]; then
  chmod 700 "${HOME}/.ssh"
  find "${HOME}/.ssh" -type f                 -exec chmod 600 {} \;
  find "${HOME}/.ssh" -name "*.pub"           -exec chmod 644 {} \;
  find "${HOME}/.ssh" -name "authorized_keys" -exec chmod 644 {} \;
  find "${HOME}/.ssh" -name "known_hosts"     -exec chmod 644 {} \;
fi
if [ -f "${HOME}/.kube/config" ]; then
  chmod 600 "${HOME}/.kube/config"
fi

# ── Custom CA certificates ────────────────────────────────────────────────────
# If a custom CA ConfigMap is mounted, concatenate all certs into a single
# bundle file and set env vars so Go, curl, Python, and Node.js trust them.
if [ "${CUSTOM_CA_MOUNTED:-}" = "true" ] && [ -d /etc/ssl/certs/custom ]; then
  cat /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/custom/*.crt \
      /etc/ssl/certs/custom/*.pem 2>/dev/null > /tmp/ca-bundle.crt || true
  export SSL_CERT_FILE=/tmp/ca-bundle.crt
  export REQUESTS_CA_BUNDLE=/tmp/ca-bundle.crt
  export NODE_EXTRA_CA_CERTS=/tmp/ca-bundle.crt
fi

# ── opencode ──────────────────────────────────────────────────────────────────
# Rewritten on every start so changes to env vars (e.g. new LLM endpoint)
# are always reflected without manual intervention.
# Config reference: https://github.com/opencode-ai/opencode
cat > "${HOME}/.opencode.json" <<EOF
{
  "providers": {
    "local": {
      "endpoint": "${OPENAI_BASE_URL}/v1",
      "apiKey": "no-key-required"
    }
  },
  "agents": {
    "coder": {
      "model": "local/${MODEL_NAME}",
      "maxTokens": 8192
    }
  }
}
EOF

# ── Git identity ──────────────────────────────────────────────────────────────
if [ -n "${USER_EMAIL:-}" ]; then
  git config --global user.email "${USER_EMAIL}"
fi
if [ -n "${USER_ID:-}" ]; then
  git config --global user.name "${USER_ID}"
fi

# ── zsh config (bootstrapped once; user can edit afterwards) ─────────────────
if [ ! -f "${HOME}/.zshrc" ]; then
  cat > "${HOME}/.zshrc" <<'ZSHRC'
export PATH="/usr/local/go/bin:${HOME}/go/bin:$PATH"
export GOPATH="${HOME}/go"
export HISTFILE="${HOME}/.zsh_history"
export HISTSIZE=10000
export SAVEHIST=10000
setopt SHARE_HISTORY
PS1='%F{green}%n%f@workspace:%F{blue}%~%f%# '
ZSHRC
fi

# ── Welcome message ───────────────────────────────────────────────────────────
cat > /tmp/welcome.txt <<EOF

  ╔══════════════════════════════════════════════════════╗
  ║           DevPlane  —  Development Workspace         ║
  ╚══════════════════════════════════════════════════════╝

  User:     ${USER_ID:-unknown}
  AI model: ${MODEL_NAME:-not configured}
  Endpoint: ${OPENAI_BASE_URL:-not configured}

  Tools: kubectl  helm  k9s  go  node  python3  git  opencode
  Type 'opencode' to start the AI assistant.

EOF

# ── tmux session ──────────────────────────────────────────────────────────────
# Create a detached session running a startup script that prints the welcome
# message and then drops into zsh.  If a session already exists (e.g. browser
# tab reconnect after network blip) we skip creation and just re-attach below.
if ! tmux has-session -t workspace 2>/dev/null; then
  tmux new-session -d -s workspace \
    'cat /tmp/welcome.txt; exec zsh'
fi

# ── ttyd ──────────────────────────────────────────────────────────────────────
# exec replaces this process; ttyd serves the tmux attach command over
# WebSocket on port 7681.  --writable allows keyboard input from the browser.
exec ttyd --port 7681 --writable tmux attach-session -t workspace
