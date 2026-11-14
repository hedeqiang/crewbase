#!/usr/bin/env bash
# AWS EC2 user-data for Crewbase (Amazon Linux 2023 or Ubuntu 24.04).
#
# Launch with:
#   - a security group with NO inbound rules (use SSM Session Manager or
#     Tailscale to get in; add SSH from your own IP only if you must)
#   - an instance profile with AmazonSSMManagedInstanceCore (optional)
#   - a second EBS volume (gp3) for data, e.g. /dev/sdf (shows up as nvme1n1)
#
# It mounts the data volume at /data, installs Docker + Compose, fetches
# Crewbase and starts the hardened docker-compose stack with data on
# /data/crewbase.
set -euo pipefail

CREWBASE_REPO=${CREWBASE_REPO:-https://github.com/hedeqiang/crewbase.git}
CREWBASE_REF=${CREWBASE_REF:-main}
DATA_DEVICE=${DATA_DEVICE:-}
DATA_MOUNT=${DATA_MOUNT:-/data}
APP_DIR=${APP_DIR:-/opt/crewbase}
TS_AUTHKEY=${TS_AUTHKEY:-}
COMPOSE_VERSION=${COMPOSE_VERSION:-v2.39.2}

log() { printf '==> %s\n' "$*"; }

# ---------------------------------------------------------------- data volume
detect_disk() {
	local root_disk d
	root_disk=$(lsblk -no PKNAME "$(findmnt -no SOURCE /)" 2>/dev/null || true)
	for d in $(lsblk -dno NAME,TYPE | awk '$2=="disk"{print $1}'); do
		[ "$d" = "$root_disk" ] && continue
		if [ "$(lsblk -no NAME "/dev/$d" | wc -l)" -eq 1 ] && [ -z "$(lsblk -no MOUNTPOINT "/dev/$d" | tr -d '[:space:]')" ]; then
			echo "/dev/$d"
			return
		fi
	done
}
if ! mountpoint -q "$DATA_MOUNT"; then
	[ -n "$DATA_DEVICE" ] || DATA_DEVICE=$(detect_disk)
	if [ -n "$DATA_DEVICE" ]; then
		blkid "$DATA_DEVICE" >/dev/null 2>&1 || mkfs.ext4 -q -L crewbase-data "$DATA_DEVICE"
		mkdir -p "$DATA_MOUNT"
		UUID=$(blkid -s UUID -o value "$DATA_DEVICE")
		grep -q "UUID=$UUID" /etc/fstab || echo "UUID=$UUID $DATA_MOUNT ext4 defaults,nofail,noatime 0 2" >>/etc/fstab
		mount "$DATA_MOUNT"
		log "data volume $DATA_DEVICE mounted at $DATA_MOUNT"
	else
		log "no data volume found; using $DATA_MOUNT on the root volume"
		mkdir -p "$DATA_MOUNT"
	fi
fi
install -d -m 0700 -o 65532 -g 65532 "$DATA_MOUNT/crewbase"

# ---------------------------------------------------------------- docker
. /etc/os-release
if ! command -v docker >/dev/null 2>&1; then
	case "$ID" in
	amzn)
		dnf install -y docker git
		;;
	ubuntu | debian)
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -y
		apt-get install -y ca-certificates curl git
		install -m 0755 -d /etc/apt/keyrings
		curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o /etc/apt/keyrings/docker.asc
		echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$ID ${VERSION_CODENAME} stable" \
			>/etc/apt/sources.list.d/docker.list
		apt-get update -y
		apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
		;;
	*)
		echo "unsupported distribution: $ID" >&2
		exit 1
		;;
	esac
fi
if ! docker compose version >/dev/null 2>&1; then
	log "installing docker compose $COMPOSE_VERSION"
	ARCH=$(uname -m)
	mkdir -p /usr/local/lib/docker/cli-plugins
	curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-${ARCH}" \
		-o /usr/local/lib/docker/cli-plugins/docker-compose
	chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi
if ! docker buildx version >/dev/null 2>&1 && [ "$ID" = amzn ]; then
	dnf install -y docker-buildx-plugin >/dev/null 2>&1 || log "buildx not available; the build uses the classic builder"
fi
mkdir -p /etc/docker
[ -f /etc/docker/daemon.json ] || echo '{"log-driver":"local","live-restore":true,"no-new-privileges":true}' >/etc/docker/daemon.json
systemctl enable docker >/dev/null
systemctl restart docker

# ---------------------------------------------------------------- crewbase
if [ -d "$APP_DIR/.git" ]; then
	git -C "$APP_DIR" fetch --depth 1 origin "$CREWBASE_REF" && git -C "$APP_DIR" checkout -q FETCH_HEAD
else
	git clone --depth 1 --branch "$CREWBASE_REF" "$CREWBASE_REPO" "$APP_DIR"
fi
cd "$APP_DIR/deploy"
if [ ! -f .env ]; then
	cp .env.example .env
	[ -n "$TS_AUTHKEY" ] && sed -i "s|^TS_AUTHKEY=.*|TS_AUTHKEY=$TS_AUTHKEY|" .env
	chmod 600 .env
fi
cat >docker-compose.override.yml <<YML
volumes:
  crewbase-data:
    driver: local
    driver_opts: { type: none, o: bind, device: $DATA_MOUNT/crewbase }
YML
PROFILE_ARGS=()
[ -n "$TS_AUTHKEY" ] && PROFILE_ARGS=(--profile tailscale)
log "building and starting Crewbase"
docker compose "${PROFILE_ARGS[@]}" up -d --build

cat <<DONE

Crewbase is running; no inbound port was opened.
  Keys:    $APP_DIR/deploy/.env, then docker compose up -d (in $APP_DIR/deploy)
  Token:   docker compose -f $APP_DIR/deploy/docker-compose.yml exec crewbase cat /data/token
  Access:  Tailscale sidecar (TS_AUTHKEY), or publish on host loopback with
             docker compose -f docker-compose.yml -f docker-compose.loopback.yml up -d
           and forward it with SSM (no SSH, no inbound rule):
             aws ssm start-session --target <instance-id> \\
               --document-name AWS-StartPortForwardingSession \\
               --parameters '{"portNumber":["7788"],"localPortNumber":["7788"]}'
DONE
