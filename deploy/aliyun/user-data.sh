#!/usr/bin/env bash
# Alibaba Cloud ECS (Ubuntu 24.04) user-data for Crewbase.
#
# Paste into "实例自定义数据 / User Data" when creating the instance (or run
# as root on a fresh instance). It:
#   1. mounts the ESSD data disk at /data (formats it only if it is blank)
#   2. installs Docker CE + Compose from the Aliyun mirror
#   3. configures a registry mirror (your ACR accelerator URL)
#   4. fetches Crewbase and starts the hardened docker-compose stack with
#      data on /data/crewbase
# No inbound port is opened: keep the security group to SSH (22) from your
# own IP only, or none at all if you use Workbench / Tailscale.
#
# Edit the variables below (or export them before running).
set -euo pipefail

CREWBASE_REPO=${CREWBASE_REPO:-https://github.com/hedeqiang/crewbase.git}
CREWBASE_REF=${CREWBASE_REF:-main}
# 容器镜像服务 ACR → 镜像工具 → 镜像加速器, e.g. https://xxxx.mirror.aliyuncs.com
REGISTRY_MIRROR=${REGISTRY_MIRROR:-}
# ESSD data disk device; empty = auto-detect the first unpartitioned, unmounted
# virtio disk other than the system disk.
DATA_DEVICE=${DATA_DEVICE:-}
DATA_MOUNT=${DATA_MOUNT:-/data}
APP_DIR=${APP_DIR:-/opt/crewbase}
TS_AUTHKEY=${TS_AUTHKEY:-}

log() { printf '==> %s\n' "$*"; }
export DEBIAN_FRONTEND=noninteractive

# ---------------------------------------------------------------- 1. ESSD data disk
detect_disk() {
	local root_disk d
	root_disk=$(lsblk -no PKNAME "$(findmnt -no SOURCE /)" 2>/dev/null || true)
	for d in $(lsblk -dno NAME,TYPE | awk '$2=="disk"{print $1}'); do
		[ "$d" = "$root_disk" ] && continue
		# skip disks that have partitions or are mounted
		if [ "$(lsblk -no NAME "/dev/$d" | wc -l)" -eq 1 ] && [ -z "$(lsblk -no MOUNTPOINT "/dev/$d" | tr -d '[:space:]')" ]; then
			echo "/dev/$d"
			return
		fi
	done
}
if ! mountpoint -q "$DATA_MOUNT"; then
	[ -n "$DATA_DEVICE" ] || DATA_DEVICE=$(detect_disk)
	if [ -n "$DATA_DEVICE" ]; then
		if ! blkid "$DATA_DEVICE" >/dev/null 2>&1; then
			log "formatting blank data disk $DATA_DEVICE (ext4)"
			mkfs.ext4 -q -L crewbase-data "$DATA_DEVICE"
		fi
		mkdir -p "$DATA_MOUNT"
		UUID=$(blkid -s UUID -o value "$DATA_DEVICE")
		grep -q "UUID=$UUID" /etc/fstab || echo "UUID=$UUID $DATA_MOUNT ext4 defaults,nofail,noatime 0 2" >>/etc/fstab
		mount "$DATA_MOUNT"
		log "data disk $DATA_DEVICE mounted at $DATA_MOUNT"
	else
		log "no data disk found; using $DATA_MOUNT on the system disk (attach an ESSD for production)"
		mkdir -p "$DATA_MOUNT"
	fi
fi
install -d -m 0700 -o 65532 -g 65532 "$DATA_MOUNT/crewbase"

# ---------------------------------------------------------------- 2. Docker CE (Aliyun mirror)
if ! command -v docker >/dev/null 2>&1; then
	log "installing Docker CE from mirrors.aliyun.com"
	apt-get update -y
	apt-get install -y ca-certificates curl gnupg git
	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/ubuntu/gpg | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
	chmod a+r /etc/apt/keyrings/docker.gpg
	. /etc/os-release
	echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://mirrors.aliyun.com/docker-ce/linux/ubuntu ${VERSION_CODENAME} stable" \
		>/etc/apt/sources.list.d/docker.list
	apt-get update -y
	apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
command -v git >/dev/null 2>&1 || apt-get install -y git

# ---------------------------------------------------------------- 3. registry mirror
if [ -n "$REGISTRY_MIRROR" ]; then
	log "configuring registry mirror $REGISTRY_MIRROR"
	mkdir -p /etc/docker
	cat >/etc/docker/daemon.json <<JSON
{
  "registry-mirrors": ["$REGISTRY_MIRROR"],
  "log-driver": "local",
  "live-restore": true,
  "no-new-privileges": true
}
JSON
else
	log "REGISTRY_MIRROR not set: Docker Hub pulls may be slow or fail from mainland China"
fi
systemctl enable docker >/dev/null
systemctl restart docker

# ---------------------------------------------------------------- 4. Crewbase
if [ -d "$APP_DIR/.git" ]; then
	git -C "$APP_DIR" fetch --depth 1 origin "$CREWBASE_REF" && git -C "$APP_DIR" checkout -q FETCH_HEAD
else
	git clone --depth 1 --branch "$CREWBASE_REF" "$CREWBASE_REPO" "$APP_DIR"
fi
cd "$APP_DIR/deploy"
if [ ! -f .env ]; then
	cp .env.example .env
	cat >>.env <<ENV
GOPROXY=https://goproxy.cn,direct
NPM_REGISTRY=https://registry.npmmirror.com
ALPINE_MIRROR=mirrors.aliyun.com
TZ=Asia/Shanghai
ENV
	[ -n "$TS_AUTHKEY" ] && sed -i "s|^TS_AUTHKEY=.*|TS_AUTHKEY=$TS_AUTHKEY|" .env
	chmod 600 .env
fi
# DeepSeek / Qwen / Kimi presets (keys in .env).
[ -f crewbase.patch.yml.orig ] || cp crewbase.patch.yml crewbase.patch.yml.orig
cp aliyun/crewbase.patch.yml crewbase.patch.yml
# Data volume on the ESSD.
cat >docker-compose.override.yml <<YML
volumes:
  crewbase-data:
    driver: local
    driver_opts: { type: none, o: bind, device: $DATA_MOUNT/crewbase }
YML

PROFILE_ARGS=()
[ -n "$TS_AUTHKEY" ] && PROFILE_ARGS=(--profile tailscale)
log "building and starting Crewbase (first build takes several minutes)"
docker compose "${PROFILE_ARGS[@]}" up -d --build

cat <<DONE

Crewbase is running (no inbound port opened).
  1. Put model keys into $APP_DIR/deploy/.env (DEEPSEEK_API_KEY / DASHSCOPE_API_KEY /
     MOONSHOT_API_KEY), then: cd $APP_DIR/deploy && docker compose up -d
  2. Token:   docker compose -f $APP_DIR/deploy/docker-compose.yml exec crewbase cat /data/token
  3. Checks:  docker compose -f $APP_DIR/deploy/docker-compose.yml exec crewbase crew doctor
  4. Access:  Tailscale sidecar (TS_AUTHKEY) or SSH tunnel to the host; see docs/deploy.md.
DONE
