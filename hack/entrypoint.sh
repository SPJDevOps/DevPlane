#!/usr/bin/env bash
# Workspace pod entrypoint.
# $HOME=/workspace (uid 1000 in /etc/passwd) which is the mounted PVC.
# All user data written here persists across pod restarts.
set -euo pipefail

# ── Required environment variables ───────────────────────────────────────────
# Fail fast with a clear message if the operator failed to inject these.
: "${AI_PROVIDERS_JSON:?AI_PROVIDERS_JSON must be set by the operator (spec.aiConfig.providers)}"

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
# Global config location: ~/.config/opencode/opencode.json
# Config reference: https://opencode.ai/docs/configuration/overview
mkdir -p "${HOME}/.config/opencode"
python3 -c "
import json, os, sys
providers = json.loads(os.environ['AI_PROVIDERS_JSON'])
cfg = {'\$schema': 'https://opencode.ai/config.json', 'provider': {}}
for p in providers:
    cfg['provider'][p['name']] = {
        'npm': '@ai-sdk/openai-compatible',
        'name': p['name'],
        'options': {'baseURL': p['endpoint'] + '/v1', 'apiKey': 'no-key-required'},
        'models': {m: {'name': m} for m in p['models']}
    }
print(json.dumps(cfg, indent=2))
" > "${HOME}/.config/opencode/opencode.json"

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
# ── PATH ──────────────────────────────────────────────────────────────────────
export PATH="/usr/local/go/bin:${HOME}/go/bin:$PATH"
export GOPATH="${HOME}/go"

# ── History ───────────────────────────────────────────────────────────────────
export HISTFILE="${HOME}/.zsh_history"
export HISTSIZE=10000
export SAVEHIST=10000
setopt SHARE_HISTORY HIST_IGNORE_DUPS HIST_IGNORE_SPACE

# ── Useful zsh options ────────────────────────────────────────────────────────
setopt AUTO_CD
setopt CORRECT
setopt GLOB_COMPLETE
setopt NO_BEEP

# ── Plugins ───────────────────────────────────────────────────────────────────
source /usr/share/zsh-autosuggestions/zsh-autosuggestions.zsh
source /usr/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh

# ── Starship prompt ───────────────────────────────────────────────────────────
eval "$(starship init zsh)"
ZSHRC
fi

# ── Starship config (bootstrapped once; user can edit afterwards) ─────────────
if [ ! -f "${HOME}/.config/starship.toml" ]; then
  mkdir -p "${HOME}/.config"
  cat > "${HOME}/.config/starship.toml" <<'STARSHIP'
format = "$username$directory$git_branch$git_status$golang$nodejs$python$cmd_duration$line_break$character"

[username]
show_always = true
format  = "[$user]($style)@workspace "
style_user = "bold green"
style_root = "bold red"

[directory]
format            = "[$path]($style) "
style             = "bold blue"
truncation_length = 4
truncate_to_repo  = true

[git_branch]
format = "[($branch)]($style) "
style  = "bold purple"

[git_status]
format    = '[$all_status$ahead_behind]($style) '
style     = "bold yellow"
ahead     = "↑${count}"
behind    = "↓${count}"
diverged  = "↕"
staged    = "+${count}"
modified  = "~${count}"
untracked = "?${count}"
deleted   = "-${count}"

[golang]
format = "[Go $version]($style) "
style  = "bold cyan"

[nodejs]
format = "[Node $version]($style) "
style  = "bold green"

[python]
format = "[Py $version]($style) "
style  = "bold yellow"

[cmd_duration]
min_time = 2000
format   = "[took $duration]($style) "
style    = "bold yellow"

[character]
success_symbol = "[>](bold green)"
error_symbol   = "[>](bold red)"
STARSHIP
fi

# ── tmux config (bootstrapped once; user can edit afterwards) ─────────────────
if [ ! -f "${HOME}/.tmux.conf" ]; then
  cat > "${HOME}/.tmux.conf" <<'TMUXCONF'
# ── Terminal / Colors ─────────────────────────────────────────────────────────
set -g default-terminal "screen-256color"
set -ga terminal-overrides ",xterm-256color:Tc"

# ── General ───────────────────────────────────────────────────────────────────
set -g mouse on
set -g history-limit 10000
set -g escape-time 10
set -g focus-events on
set -g base-index 1
setw -g pane-base-index 1
setw -g automatic-rename on

# ── Prefix: Ctrl-Space ────────────────────────────────────────────────────────
unbind C-b
set -g prefix C-Space
bind C-Space send-prefix

# ── Splits (keep current path) ────────────────────────────────────────────────
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"

# ── Reload config ─────────────────────────────────────────────────────────────
bind r source-file ~/.tmux.conf \; display 'Config reloaded!'

# ── Pane borders (Tokyo Night palette) ────────────────────────────────────────
set -g pane-border-style        'fg=#3b4261'
set -g pane-active-border-style 'fg=#7aa2f7'

# ── Status bar ────────────────────────────────────────────────────────────────
set -g status on
set -g status-interval 5
set -g status-position bottom
set -g status-style          'bg=#1a1b26 fg=#a9b1d6'

set -g status-left-length 30
set -g status-left  '#[bg=#7aa2f7,fg=#1a1b26,bold] #S #[bg=#1a1b26,fg=#7aa2f7] '

set -g status-right-length 40
set -g status-right '#[fg=#565f89] %Y-%m-%d #[fg=#a9b1d6]%H:%M '

set -g window-status-current-format '#[bg=#7aa2f7,fg=#1a1b26,bold] #I:#W '
set -g window-status-format         '#[fg=#565f89] #I:#W '
TMUXCONF
fi

# ── Welcome message ───────────────────────────────────────────────────────────
_providers_summary=$(python3 -c "
import json, os
providers = json.loads(os.environ.get('AI_PROVIDERS_JSON', '[]'))
for p in providers:
    models = ', '.join(p.get('models', []))
    print(f'    - {p[\"name\"]}: {models}')
" 2>/dev/null || echo "    (not configured)")

cat > /tmp/welcome.txt <<EOF

  ╔══════════════════════════════════════════════════════╗
  ║           DevPlane  —  Development Workspace         ║
  ╚══════════════════════════════════════════════════════╝

  User:  ${USER_ID:-unknown}
  AI providers:
${_providers_summary}

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
