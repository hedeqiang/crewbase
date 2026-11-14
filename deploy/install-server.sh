#!/usr/bin/env bash
# Crewbase headless server installer (Linux + systemd). Idempotent: running
# it again upgrades the binaries and rewrites the unit, and keeps data,
# token, patch and env files.
#
#   sudo ./deploy/install-server.sh                 # build from this checkout (needs Go 1.27)
#   sudo ./deploy/install-server.sh --bin-dir DIR   # use prebuilt DIR/crewd and DIR/crew
#   sudo ./deploy/install-server.sh --with-docker   # let crewd use the Docker sandbox
#
# What it does:
#   - creates the system user `crewbase` (no login shell, no password)
#   - data in /var/lib/crewbase (0700), config in /etc/crewbase
#   - installs /usr/local/bin/crewd and /usr/local/bin/crew
#   - installs and starts crewbase.service: `crewd --profile server`
#   - opens NO inbound ports; the API listens on 127.0.0.1:7788 only
set -euo pipefail

PREFIX=/usr/local/bin
DATA_DIR=/var/lib/crewbase
CONF_DIR=/etc/crewbase
SVC_USER=crewbase
UNIT=/etc/systemd/system/crewbase.service
BIN_DIR=""
WITH_DOCKER=0
NO_START=0

usage() {
	sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
	echo "Options: --bin-dir DIR  --with-docker  --data-dir DIR  --no-start  -h|--help"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--bin-dir) BIN_DIR=${2:?--bin-dir needs a directory}; shift 2 ;;
	--with-docker) WITH_DOCKER=1; shift ;;
	--data-dir) DATA_DIR=${2:?--data-dir needs a directory}; shift 2 ;;
	--no-start) NO_START=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
	esac
done

log() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "install-server.sh supports Linux only (use the desktop app on macOS)"
[ "$(id -u)" -eq 0 ] || die "run as root (sudo $0)"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd "$SCRIPT_DIR/.." && pwd)

# ---------------------------------------------------------------- binaries
TMP_BIN=""
cleanup() { if [ -n "$TMP_BIN" ]; then rm -rf "$TMP_BIN"; fi; }
trap cleanup EXIT

if [ -z "$BIN_DIR" ]; then
	command -v go >/dev/null 2>&1 || die "Go not found: install Go 1.27+ or pass --bin-dir with prebuilt crewd and crew"
	[ -f "$REPO_DIR/go.mod" ] || die "not in a Crewbase checkout; pass --bin-dir"
	TMP_BIN=$(mktemp -d)
	log "building crewd and crew from $REPO_DIR"
	(cd "$REPO_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$TMP_BIN/crewd" ./apps/crewd \
		&& CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$TMP_BIN/crew" ./apps/cli)
	BIN_DIR=$TMP_BIN
fi
for b in crewd crew; do
	[ -x "$BIN_DIR/$b" ] || die "$BIN_DIR/$b not found or not executable"
done
log "installing binaries to $PREFIX"
install -m 0755 "$BIN_DIR/crewd" "$PREFIX/crewd.new" && mv -f "$PREFIX/crewd.new" "$PREFIX/crewd"
install -m 0755 "$BIN_DIR/crew" "$PREFIX/crew.new" && mv -f "$PREFIX/crew.new" "$PREFIX/crew"

# ---------------------------------------------------------------- user and dirs
if ! id "$SVC_USER" >/dev/null 2>&1; then
	log "creating system user $SVC_USER"
	NOLOGIN=$(command -v nologin || echo /usr/sbin/nologin)
	useradd --system --home-dir "$DATA_DIR" --no-create-home --shell "$NOLOGIN" --user-group "$SVC_USER"
else
	log "user $SVC_USER exists"
fi
install -d -m 0700 -o "$SVC_USER" -g "$SVC_USER" "$DATA_DIR"
install -d -m 0750 -o root -g "$SVC_USER" "$CONF_DIR"

if [ ! -f "$CONF_DIR/crewbase.patch.yml" ]; then
	log "writing $CONF_DIR/crewbase.patch.yml"
	cat >"$CONF_DIR/crewbase.patch.yml" <<'PATCH'
# Crewbase server patch (merged by id over `crewd --profile server`).
# Inspect the result with: crew dump-config --profile server
# Apply changes with: systemctl restart crewbase

# Audit log: append-only, hash-chained (docs/06 §10).
- id: audit
  name: '@crewbase/audit'
  config:
    dir: /var/lib/crewbase

# Model providers. Keys come from /etc/crewbase/env through env: references.
# - id: llm-openai
#   name: '@crewbase/llm-openai'
#   config:
#     providers:
#       - { id: deepseek, apiKeyRef: 'env:DEEPSEEK_API_KEY', models: [deepseek-chat, deepseek-reasoner] }
#       - { id: qwen, apiKeyRef: 'env:DASHSCOPE_API_KEY', models: [qwen-plus, qwen-max] }
#       - { id: moonshot, apiKeyRef: 'env:MOONSHOT_API_KEY', models: [kimi-k2-0905-preview] }
PATCH
	chown root:"$SVC_USER" "$CONF_DIR/crewbase.patch.yml"
	chmod 0640 "$CONF_DIR/crewbase.patch.yml"
else
	log "keeping existing $CONF_DIR/crewbase.patch.yml"
fi
if [ ! -f "$CONF_DIR/env" ]; then
	log "writing $CONF_DIR/env (model API keys go here)"
	cat >"$CONF_DIR/env" <<'ENV'
# Environment for crewbase.service (root:crewbase 0640). Referenced from the
# patch as env:NAME; never put keys in the patch file itself.
DEEPSEEK_API_KEY=
DASHSCOPE_API_KEY=
MOONSHOT_API_KEY=
ENV
	chown root:"$SVC_USER" "$CONF_DIR/env"
	chmod 0640 "$CONF_DIR/env"
fi

# ---------------------------------------------------------------- docker sandbox
SUPP_GROUPS=""
if [ "$WITH_DOCKER" -eq 1 ]; then
	command -v docker >/dev/null 2>&1 || die "--with-docker: docker CLI not found"
	getent group docker >/dev/null || die "--with-docker: group docker does not exist"
	warn "membership in the docker group is root-equivalent on this host; crewd only uses it to start sandbox containers"
	usermod -aG docker "$SVC_USER"
	SUPP_GROUPS="docker"
elif ! id -nG "$SVC_USER" | tr ' ' '\n' | grep -qx docker; then
	warn "no Docker sandbox: sandbox-mode commands will be refused (fail closed). Re-run with --with-docker to enable it."
else
	SUPP_GROUPS="docker"
fi

# ---------------------------------------------------------------- systemd unit
log "writing $UNIT"
RW_PATHS="$DATA_DIR"
cat >"$UNIT.new" <<UNITEOF
# Managed by deploy/install-server.sh; local overrides: systemctl edit crewbase
[Unit]
Description=Crewbase multi-agent workbench (server profile)
Documentation=https://github.com/hedeqiang/crewbase/blob/main/docs/deploy.md
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
${SUPP_GROUPS:+SupplementaryGroups=$SUPP_GROUPS}
Environment=CREWBASE_HOME=$DATA_DIR
Environment=HOME=$DATA_DIR
EnvironmentFile=-$CONF_DIR/env
ExecStart=$PREFIX/crewd --profile server --root $DATA_DIR --addr 127.0.0.1:7788 --patch $CONF_DIR/crewbase.patch.yml
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=30
UMask=0077
LimitNOFILE=65536

# Hardening
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=$RW_PATHS
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
# RestrictNamespaces is not set: Chromium's sandbox needs user namespaces.
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
UNITEOF
mv -f "$UNIT.new" "$UNIT"
systemctl daemon-reload
systemctl enable crewbase.service >/dev/null

if [ "$NO_START" -eq 0 ]; then
	log "(re)starting crewbase.service"
	systemctl restart crewbase.service
	for _ in $(seq 1 30); do
		[ -s "$DATA_DIR/token" ] && break
		sleep 1
	done
	systemctl --no-pager --lines=0 status crewbase.service || true
fi

# ---------------------------------------------------------------- checks
# Nothing here opens a port. Warn if 7788 is somehow reachable beyond loopback.
if command -v ss >/dev/null 2>&1; then
	if ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E ':7788$' | grep -vqE '^(127\.0\.0\.1|\[::1\]):7788$'; then
		warn "port 7788 is listening on a non-loopback address; check $CONF_DIR/crewbase.patch.yml (gateway addr)"
	fi
fi

cat <<DONE

Crewbase is installed.
  service   systemctl status crewbase   |  journalctl -u crewbase -f
  data      $DATA_DIR (token, sessions, audit)
  config    $CONF_DIR/crewbase.patch.yml, $CONF_DIR/env (API keys)
  checks    sudo -u $SVC_USER CREWBASE_HOME=$DATA_DIR crew doctor

No inbound port was opened. The API listens on 127.0.0.1:7788 only.

Pair a device / get the token:
  1. Read the desktop token (treat it like a password):
       sudo cat $DATA_DIR/token
  2. Reach the API over an outbound/private channel, never a public port:
       SSH tunnel (from your laptop):
         ssh -N -L 7788:127.0.0.1:7788 <user>@<this-host>
         then open http://127.0.0.1:7788 and paste the token
       Tailscale (on this host, tailnet-only):
         tailscale serve --bg 7788
  3. CLI on this host:
       sudo -u $SVC_USER CREWBASE_HOME=$DATA_DIR crew agents
DONE
