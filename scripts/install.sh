#!/usr/bin/env bash
# OTC one-line installer (issue #38) — turns a fresh Debian/Ubuntu-family
# machine into a running OTC device: MariaDB, a native Go toolchain, ONNX
# Runtime, the RAM++ tagging model, the database schema, app config, a
# systemd service, and the otc binary itself — built from source, live, on
# this machine, using the exact same versions/URLs Makefile.pi already
# automates over SSH from a dev machine (see that file for the authoritative,
# more granular version, e.g. to re-run just one step, or to add a RAID1
# array across two disks via its `raid`/`raid-watch` targets — this script
# deliberately skips that, since it assumes you already have network+login
# access, i.e. this isn't the headless flash-an-SD-card flow that issue #38's
# pre-built image + first-boot WiFi wizard covers).
#
# Usage: log into any Debian/Ubuntu box (Raspberry Pi OS included) and run
#
#   curl -fsSL https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/install.sh | sudo bash -s -- <subdomain>
#
# <subdomain> (optional) is this device's bridge subdomain, e.g. "pit" for
# pit.off-the.cloud — a name you and your friends' clients use to reach this
# device through the bridge relay. Omit it and the script prompts for one.
#
# Safe to re-run: it retries whatever step failed, and picks up new code
# (an upgrade) on every run — device identity (DEVICE_UUID/BRIDGE_SECRET/DB
# password) is only ever generated once, on the very first run.
set -euo pipefail

log() { echo "[otc-install] $*"; }
die() { echo "[otc-install] ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 0. Preflight
# ---------------------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "needs root — re-run as: curl ... | sudo bash -s -- [subdomain]"
command -v apt-get >/dev/null 2>&1 || die "only Debian/Ubuntu-family distros are supported (no apt-get found)"

case "$(uname -m)" in
    aarch64) ARCH=arm64; ORT_ARCH=aarch64 ;;
    x86_64)  ARCH=amd64; ORT_ARCH=x64 ;;
    *) die "unsupported architecture $(uname -m) (need arm64 or amd64)" ;;
esac

REPO_URL=https://github.com/alonsovidales/otc.git
RAW_BASE=https://raw.githubusercontent.com/alonsovidales/otc/main
SRC_DIR=/opt/otc-src
GO_VERSION=1.26.1
ONNXRUNTIME_VERSION=1.24.3
MODEL_DIR=/usr/local/models
MODEL_ONNX=$MODEL_DIR/ram_plus_swin_large_14m.int8.onnx
MODEL_TAGS=$MODEL_DIR/tag_list_4585.txt
MODEL_THRESHOLDS=$MODEL_DIR/tag_list_4585_thresholds.txt
MODEL_HF_REPO=https://huggingface.co/anakhiu/ram-plus-onnx-int8/resolve/main
BRIDGE_ADDR=off-the.cloud
BRIDGE_CONNECTIONS=5
STORAGE_PATH=/mnt/storage/
UNENC_PATH=/mnt/storage/unencrypted/
ENVIRONMENT=dev
HTTP_PORT=8080
ENV_FILE=/etc/otc/otc-install.env

SUBDOMAIN="${1:-}"
if [ -z "$SUBDOMAIN" ]; then
    read -rp "Choose a subdomain for this device (e.g. 'pit' for pit.$BRIDGE_ADDR): " SUBDOMAIN
fi
[[ "$SUBDOMAIN" =~ ^[a-z0-9-]+$ ]] || die "subdomain must be lowercase letters/digits/hyphens only"

# ---------------------------------------------------------------------------
# 1. OS packages
# ---------------------------------------------------------------------------
log "[1/9] apt-get update + base packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y mariadb-server build-essential git curl wget rsync ca-certificates ffmpeg

log "[2/9] otc service account"
id otc >/dev/null 2>&1 || useradd -r -m -d /home/otc -s /usr/sbin/nologin otc
for g in dialout video plugdev gpio i2c spi; do
    getent group "$g" >/dev/null 2>&1 && usermod -aG "$g" otc || true
done

# ---------------------------------------------------------------------------
# 2. Toolchain + runtime deps
# ---------------------------------------------------------------------------
log "[3/9] Go $GO_VERSION ($ARCH)"
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go$GO_VERSION "; then
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/go.tar.gz" "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/go.tar.gz"
    rm -rf "$tmp"
fi

log "[4/9] ONNX Runtime $ONNXRUNTIME_VERSION ($ORT_ARCH)"
if [ ! -f /opt/onnxruntime/lib/libonnxruntime.so ]; then
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/ort.tgz" "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}.tgz"
    tar -xzf "$tmp/ort.tgz" -C "$tmp"
    rm -rf /opt/onnxruntime
    mv "$tmp/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}" /opt/onnxruntime
    rm -rf "$tmp"
fi

log "[5/9] RAM++ tagging model (~870MB, only downloaded once)"
mkdir -p "$MODEL_DIR"
if [ ! -f "$MODEL_ONNX" ] || [ ! -f "$MODEL_TAGS" ] || [ ! -f "$MODEL_THRESHOLDS" ]; then
    curl -fL --retry 5 --retry-delay 2 -o "$MODEL_ONNX" "$MODEL_HF_REPO/ram_plus_int8.onnx"
    curl -fL --retry 5 --retry-delay 2 -o "$MODEL_THRESHOLDS" "$MODEL_HF_REPO/ram_tag_list_threshold.txt"
    curl -fsSL "$RAW_BASE/models/models/tag_list_4585.txt.gz" | gunzip > "$MODEL_TAGS"
fi
chown -R otc:otc "$MODEL_DIR"

# ---------------------------------------------------------------------------
# 3. Fetch source, build the binary, install the web bundle
# ---------------------------------------------------------------------------
log "[6/9] Fetch source + build the otc binary"
if [ -d "$SRC_DIR/.git" ]; then
    git -C "$SRC_DIR" fetch --depth 1 origin main
    git -C "$SRC_DIR" reset --hard origin/main
else
    git clone --depth 1 "$REPO_URL" "$SRC_DIR"
fi

# proto/generated/*.go is `make pb`'s protoc output, gitignored (not hand-
# edited, not committed) — fetch the prebuilt package rather than requiring
# a full protoc + plugin toolchain just to build this one Go package here.
mkdir -p "$SRC_DIR/proto/generated"
curl -fsSL "https://github.com/alonsovidales/otc/releases/latest/download/otc-proto-generated.tar.gz" \
    | tar -xzf - -C "$SRC_DIR/proto/generated"
(
    cd "$SRC_DIR"
    export CGO_ENABLED=1
    export CGO_CFLAGS="-I/opt/onnxruntime/include"
    export CGO_LDFLAGS="-L/opt/onnxruntime/lib -lonnxruntime"
    /usr/local/go/bin/go build -o /usr/bin/otc ./bin/otc.go
)

log "[7/9] Web app (prebuilt bundle — no Node.js needed on this machine)"
mkdir -p /var/www
tmp=$(mktemp -d)
if curl -fsSL -o "$tmp/web-dist.tar.gz" "https://github.com/alonsovidales/otc/releases/latest/download/otc-web-dist.tar.gz"; then
    tar -xzf "$tmp/web-dist.tar.gz" -C /var/www
else
    log "no published web bundle found, falling back to Node.js build from source"
    command -v npm >/dev/null 2>&1 || die "npm not found and no prebuilt web bundle is available — install Node.js/npm and re-run"
    ( cd "$SRC_DIR/web" && npm ci && npm run build )
    cp -a "$SRC_DIR/web/dist/." /var/www/
fi
rm -rf "$tmp"
chown -R otc:otc /var/www

log "[8/9] Runtime directories"
mkdir -p /var/log/otc /etc/otc /var/lib/otc "$STORAGE_PATH" "$UNENC_PATH"
chown otc:otc /var/log/otc /var/www /var/lib/otc "$STORAGE_PATH" "$UNENC_PATH"
chmod 755 /var/log/otc

# ---------------------------------------------------------------------------
# 4. Database + config (device identity generated once, first run only)
# ---------------------------------------------------------------------------
log "[9/9] Database, config, and the systemd service"
systemctl enable --now mariadb
for i in $(seq 1 60); do
    mysqladmin ping >/dev/null 2>&1 && break
    if [ "$i" -eq 60 ]; then die "mariadb did not come up within 60s — check 'systemctl status mariadb'"; fi
    sleep 1
done

if [ -f "$ENV_FILE" ]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
else
    mkdir -p "$(dirname "$ENV_FILE")"
    DEVICE_UUID=$(command -v uuidgen >/dev/null 2>&1 && uuidgen || cat /proc/sys/kernel/random/uuid)
    BRIDGE_SECRET=$(openssl rand -hex 24)
    OTC_DB_PASS=$(openssl rand -base64 24 | tr -d '=+/')
    {
        echo "# Generated $(date -u +%FT%TZ) by scripts/install.sh."
        echo "# Keep this file out of git — it holds this device's DB password and bridge secret."
        echo "DEVICE_UUID=$DEVICE_UUID"
        echo "BRIDGE_SECRET=$BRIDGE_SECRET"
        echo "OTC_DB_PASS=$OTC_DB_PASS"
    } > "$ENV_FILE"
    chmod 600 "$ENV_FILE"
fi

mysql -e "
CREATE DATABASE IF NOT EXISTS otc;
CREATE USER IF NOT EXISTS 'otc'@'localhost' IDENTIFIED BY '${OTC_DB_PASS}';
ALTER USER 'otc'@'localhost' IDENTIFIED BY '${OTC_DB_PASS}';
GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';
FLUSH PRIVILEGES;
"
if mysql otc -N -B -e 'SHOW TABLES LIKE "files"' 2>/dev/null | grep -q files; then
    log "schema already present, skipping table creation"
else
    tail -n +10 "$SRC_DIR/db/db.sql" | mysql otc
fi
mysql otc -e "
INSERT INTO settings (device_uuid, subdomain, bridge_secret)
SELECT '${DEVICE_UUID}', '${SUBDOMAIN}.${BRIDGE_ADDR}', '${BRIDGE_SECRET}'
WHERE NOT EXISTS (SELECT 1 FROM settings);
"

cat > "/etc/otc_${ENVIRONMENT}.ini" <<EOF
[otc]
bridge-addr=$BRIDGE_ADDR
bridge-connections=$BRIDGE_CONNECTIONS
storage-path=$STORAGE_PATH
unenc-storage-path=$UNENC_PATH
max-thumbnail-width-px=1000
shared-link-ttl-hours=168

[logger]
log_file=/var/log/otc/otc.log
max_log_size_mb=10
level=info

[otc-api]
base-url=otc/
static=/var/www/
port=$HTTP_PORT
ssl-port=443
ssl-cert=
ssl-key=

[mysql]
user=otc
pass=$OTC_DB_PASS
port=3306
db=otc

[tagger]
model-path=$MODEL_ONNX
tags-path=$MODEL_TAGS
thresholds-path=$MODEL_THRESHOLDS
tags-per-image=10
max-images-search=5
EOF

cat > /etc/systemd/system/otc.service <<EOF
[Unit]
Description=Off The Cloud service
Wants=network-online.target
After=network-online.target mariadb.service

[Service]
Type=simple
User=otc
Group=otc
WorkingDirectory=/var/lib/otc
ExecStart=/usr/bin/otc $ENVIRONMENT
Restart=on-failure
RestartSec=3
LimitNOFILE=65535
RuntimeDirectory=otc
StateDirectory=otc
LogsDirectory=otc
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable otc.service
systemctl restart otc.service

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
echo ""
echo "=========================================================="
echo " OTC installed and running."
echo " Local web UI: http://${IP:-<this-machine>}:$HTTP_PORT/"
echo " Bridge address: ${SUBDOMAIN}.${BRIDGE_ADDR}"
echo " First 'Sign In' sets your password permanently — see README.md."
echo " Device identity/secrets: $ENV_FILE (never share or commit it)."
echo ""
echo " No RAID/multi-disk storage or WiFi-AP setup here — single disk,"
echo " already-networked machines only. For a RAID1 array across two"
echo " disks, see Makefile.pi's 'raid'/'raid-watch' targets instead."
echo "=========================================================="
