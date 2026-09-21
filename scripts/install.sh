#!/usr/bin/env bash
# OTC one-line installer (issue #38) — turns a fresh Debian/Ubuntu-family
# machine into a running OTC device: RAID1 storage, MariaDB, a native Go
# toolchain, ONNX Runtime, the RAM++ tagging model, the database schema, app
# config, the RAID status-LED watcher, a systemd service, and the otc binary
# itself — built from source, live, on this machine, using the exact same
# versions/URLs Makefile.pi already automates over SSH from a dev machine
# (see that file for the authoritative, more granular version, e.g. to
# re-run just one step).
#
# Deliberately NOT installed here: network-setup.service (the first-boot
# WiFi/AP watcher, scripts/network_setup.py). It can take down the very
# network connection this script is most likely being run over - see the
# warning on Makefile.pi's `network-setup` target - so it stays an explicit,
# deliberate step rather than something a one-line installer does to you.
#
# Usage: log into any Debian/Ubuntu box (Raspberry Pi OS included) and run
#
#   curl -fsSL https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/install.sh | sudo bash -s -- <subdomain>
#
# <subdomain> (optional) is this device's bridge subdomain, e.g. "pit" for
# pit.off-the.cloud — a name you and your friends' clients use to reach this
# device through the bridge relay. Omit it and the script prompts for one
# (only works if you saved the script locally first — see below, a piped
# `curl | bash` has no terminal left over for a prompt to read from).
#
# Disaster recovery ("the Pi died, I swapped it and kept the two data
# disks"): this script builds a RAID1 array on OTC_DISK1/OTC_DISK2 (default
# /dev/sda + /dev/sdb) the exact same way Makefile.pi's `raid`/`mariadb`
# targets do, including pointing MariaDB's datadir onto it — so re-running
# this script against a freshly re-imaged machine with the SAME two data
# disks re-attached reassembles the existing array (via `mdadm --assemble
# --scan`, tried before ever considering a wipe) and picks the recovered
# database/files back up automatically, no manual mdadm steps needed. A
# genuinely new device with blank disks needs one extra confirmation before
# they get wiped — see OTC_RAID_CONFIRM_WIPE below.
#
# Env vars (all optional):
#   OTC_DISK1 / OTC_DISK2       block devices for the RAID1 pair (default
#                               /dev/sda / /dev/sdb, matching Makefile.pi)
#   OTC_SKIP_RAID=1             single-disk device (e.g. SD-card-only test
#                               box) — uses $MOUNT_POINT as a plain directory
#                               on the root filesystem instead of RAID
#   OTC_RAID_CONFIRM_WIPE=yes   required to build a *fresh* array (wipes
#                               both disks) when no existing one is found —
#                               a curl|bash pipe has no terminal left to ask
#                               "type yes to continue" interactively, so this
#                               is the non-interactive equivalent of that
#                               confirmation. Never needed for the recovery
#                               case above, since an existing array is found
#                               and reassembled instead of wiped.
#
# Safe to re-run: it retries whatever step failed, and picks up new code
# (an upgrade) on every run — device identity (DEVICE_UUID/BRIDGE_SECRET/DB
# password) is only ever generated once, on the very first run (or the first
# run after a re-image, since that file doesn't live on the RAID array —
# see the Database + config section below for why that's still safe against
# a recovered database).
set -euo pipefail

log() { echo "[otc-install] $*"; }
die() { echo "[otc-install] ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 0. Preflight
# ---------------------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "needs root — re-run as: curl ... | sudo bash -s -- [subdomain]"
command -v apt-get >/dev/null 2>&1 || die "only Debian/Ubuntu-family distros are supported (no apt-get found)"

# If a previous run got partway through and then died (a killed `curl | bash`,
# a failed step, a reboot mid-install, ...) there will be pieces of OTC on this
# box — the binary, the database, the models — but NO /etc/otc/.install-complete
# marker, because that's only written at the very end. The script is fully
# idempotent so re-running it is the right fix, but warn loudly first so nobody
# is surprised by a box that already has an `otc` binary and a MariaDB schema.
# A box is "partially installed" if it has OTC artifacts (the source tree,
# the binary, or the config dir) but the /etc/otc/.install-complete marker
# the script writes only when a run reaches the very end is absent. A fully
# completed install always has the marker, so this only fires on a box where
# a previous run died mid-way (or one that was hand-assembled).
if { [ -d /opt/otc-src ] || [ -e /usr/bin/otc ] || [ -d /etc/otc ]; } && [ ! -e /etc/otc/.install-complete ]; then
    echo "[otc-install] NOTE: this machine already has OTC artifacts but no /etc/otc/.install-complete marker,"
    echo "[otc-install]       which means a previous install run did not finish. Re-running now to complete/repair it"
    echo "[otc-install]       (this is safe — every step is idempotent). If you're SURE you want a from-scratch"
    echo "[otc-install]       install instead, stop and wipe the box first."
    echo ""
fi

case "$(uname -m)" in
    aarch64) ARCH=arm64; ORT_ARCH=aarch64; PROTOC_ARCH=aarch_64 ;;
    x86_64)  ARCH=amd64; ORT_ARCH=x64; PROTOC_ARCH=x86_64 ;;
    *) die "unsupported architecture $(uname -m) (need arm64 or amd64)" ;;
esac

REPO_URL=https://github.com/alonsovidales/otc.git
RAW_BASE=https://raw.githubusercontent.com/alonsovidales/otc/main
SRC_DIR=/opt/otc-src
GO_VERSION=1.26.1
ONNXRUNTIME_VERSION=1.24.3
PROTOC_VERSION=29.3
MODEL_DIR=/usr/local/models
MODEL_ONNX=$MODEL_DIR/ram_plus_swin_large_14m.int8.onnx
MODEL_TAGS=$MODEL_DIR/tag_list_4585.txt
MODEL_THRESHOLDS=$MODEL_DIR/tag_list_4585_thresholds.txt
MODEL_HF_REPO=https://huggingface.co/anakhiu/ram-plus-onnx-int8/resolve/main
# Issue #52: face recognition ("People" search), humans only - off by
# default (settings.face_recognition_enabled), but both models are small
# enough (~230KB + ~10MB) to just always fetch here rather than making that
# a second, deferred download the first time someone enables the feature.
# Official OpenCV Zoo models (MIT/Apache-2.0), designed as a matched pair -
# see face_recognition/face_recognition.go's package doc comment.
FACE_DETECTOR_ONNX=$MODEL_DIR/face_detection_yunet_2023mar.onnx
FACE_RECOGNIZER_ONNX=$MODEL_DIR/face_recognition_sface_2021dec_int8.onnx
OPENCV_ZOO_RAW=https://github.com/opencv/opencv_zoo/raw/main/models
BRIDGE_ADDR=off-the.cloud
STORAGE_PATH=/mnt/storage/
UNENC_PATH=/mnt/storage/unencrypted/
ENVIRONMENT=dev
HTTP_PORT=8080
ENV_FILE=/etc/otc/otc-install.env

# ---- RAID1 storage (see the "Disaster recovery" note up top) --------------
DISK1="${OTC_DISK1:-/dev/sda}"
DISK2="${OTC_DISK2:-/dev/sdb}"
RAID_DEV=/dev/md0
MOUNT_POINT=/mnt/storage
SKIP_RAID="${OTC_SKIP_RAID:-0}"

SUBDOMAIN="${1:-}"
if [ -z "$SUBDOMAIN" ]; then
    read -rp "Choose a subdomain for this device (e.g. 'pit' for pit.$BRIDGE_ADDR): " SUBDOMAIN
fi
[[ "$SUBDOMAIN" =~ ^[a-z0-9-]+$ ]] || die "subdomain must be lowercase letters/digits/hyphens only"

# ---------------------------------------------------------------------------
# 1. OS packages
# ---------------------------------------------------------------------------
log "[1/10] apt-get update + base packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update
# libopencv-dev + pkg-config (issue #52): face recognition builds against
# gocv, which needs OpenCV's real headers/libs at compile time, found via
# pkg-config - not just a runtime .so like ONNX Runtime below. mdadm: RAID1
# storage. unzip: extracting the protoc release archive below.
apt-get install -y mariadb-server build-essential git curl wget rsync ca-certificates ffmpeg libopencv-dev pkg-config mdadm unzip python3

# Issue #80: Tailscale, so the setup wizard can offer Funnel as an
# alternative to the bridge. Installed, not enabled - nothing joins a
# tailnet or is published publicly unless the owner asks for it during
# setup. Non-fatal: a device without it simply doesn't get the option.
if ! command -v tailscale >/dev/null 2>&1; then
    curl -fsSL https://tailscale.com/install.sh | sh \
        || echo "[otc-install] WARNING: Tailscale could not be installed - the Funnel option won't be offered"
fi
# Let the otc user drive the daemon without root. This is not a
# convenience: otc.service runs with NoNewPrivileges=true, so sudo from
# the service is impossible by construction, and an operator is
# Tailscale's own answer for exactly that. Set here because it is the
# last moment anything runs as root.
if command -v tailscale >/dev/null 2>&1; then
    tailscale set --operator=otc 2>/dev/null \
        || echo "[otc-install] NOTE: could not set the Tailscale operator yet - Settings will report it if Funnel is ever turned on"
fi

log "[2/10] otc service account"
id otc >/dev/null 2>&1 || useradd -r -m -d /home/otc -s /usr/sbin/nologin otc
for g in dialout video plugdev gpio i2c spi; do
    getent group "$g" >/dev/null 2>&1 && usermod -aG "$g" otc || true
done

# ---------------------------------------------------------------------------
# 2. RAID1 storage
# ---------------------------------------------------------------------------
log "[3/10] RAID1 storage ($MOUNT_POINT)"
mkdir -p "$MOUNT_POINT"
if [ "$SKIP_RAID" = "1" ]; then
    log "OTC_SKIP_RAID=1: using $MOUNT_POINT as a plain directory on this filesystem (no RAID)."
elif mountpoint -q "$MOUNT_POINT"; then
    log "$MOUNT_POINT already mounted, skipping."
else
    if [ ! -e "$RAID_DEV" ]; then
        # Disaster recovery: an array previously built by this same block
        # leaves its superblocks on DISK1/DISK2 themselves, so re-imaging
        # only the boot disk/SD card and re-attaching those same two data
        # disks should bring the array back with zero data loss and no
        # need to ever repeat --create. Try assembling before considering
        # a wipe - this is the step a from-scratch `curl | bash` run used
        # to skip entirely, forcing a manual `mdadm --assemble --scan` by
        # hand after the fact.
        log "No $RAID_DEV yet - checking for an existing array on $DISK1/$DISK2..."
        mdadm --assemble --scan 2>/dev/null || true
    fi
    if [ -e "$RAID_DEV" ]; then
        log "$RAID_DEV exists (existing array assembled) - skipping wipe."
        mountpoint -q "$MOUNT_POINT" || mount "$RAID_DEV" "$MOUNT_POINT" 2>/dev/null || true
    else
        [ "${OTC_RAID_CONFIRM_WIPE:-}" = "yes" ] || die "no existing RAID1 array found on $DISK1/$DISK2 (checked via mdadm --assemble --scan). If these are brand-new disks, re-run with OTC_RAID_CONFIRM_WIPE=yes to build a fresh array there (WIPES BOTH DISKS). For a single-disk device, use OTC_SKIP_RAID=1 instead. To point at different disks, set OTC_DISK1/OTC_DISK2."
        log "Building a fresh RAID1 array on $DISK1 + $DISK2 (WIPES BOTH DISKS)..."
        wipefs -a "$DISK1"
        wipefs -a "$DISK2"
        mdadm --create --verbose --run "$RAID_DEV" --level=1 --raid-devices=2 "$DISK1" "$DISK2"
        mkfs.ext4 -F "$RAID_DEV"
    fi
    mkdir -p /etc/mdadm
    mdadm --detail --scan | tee -a /etc/mdadm/mdadm.conf >/dev/null
    update-initramfs -u
    grep -q "$RAID_DEV" /etc/fstab || echo "$RAID_DEV   $MOUNT_POINT   ext4   defaults   0   0" >> /etc/fstab
    mountpoint -q "$MOUNT_POINT" || mount "$RAID_DEV" "$MOUNT_POINT"
fi
mkdir -p "$UNENC_PATH"

# ---------------------------------------------------------------------------
# 3. Toolchain + runtime deps
# ---------------------------------------------------------------------------
log "[4/10] Go $GO_VERSION ($ARCH)"
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go$GO_VERSION "; then
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/go.tar.gz" "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/go.tar.gz"
    rm -rf "$tmp"
fi

log "[5/10] ONNX Runtime $ONNXRUNTIME_VERSION ($ORT_ARCH)"
if [ ! -f /opt/onnxruntime/lib/libonnxruntime.so ]; then
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/ort.tgz" "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}.tgz"
    tar -xzf "$tmp/ort.tgz" -C "$tmp"
    rm -rf /opt/onnxruntime
    mv "$tmp/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}" /opt/onnxruntime
    rm -rf "$tmp"
fi

log "[6/10] RAM++ tagging model (~870MB, only downloaded once)"
mkdir -p "$MODEL_DIR"
if [ ! -f "$MODEL_ONNX" ] || [ ! -f "$MODEL_TAGS" ] || [ ! -f "$MODEL_THRESHOLDS" ]; then
    curl -fL --retry 5 --retry-delay 2 -o "$MODEL_ONNX" "$MODEL_HF_REPO/ram_plus_int8.onnx"
    curl -fL --retry 5 --retry-delay 2 -o "$MODEL_THRESHOLDS" "$MODEL_HF_REPO/ram_tag_list_threshold.txt"
    curl -fsSL "$RAW_BASE/models/models/tag_list_4585.txt.gz" | gunzip > "$MODEL_TAGS"
fi

log "[6/10] Face recognition models (issue #52, ~10MB total, only downloaded once)"
if [ ! -f "$FACE_DETECTOR_ONNX" ]; then
    curl -fL --retry 5 --retry-delay 2 -o "$FACE_DETECTOR_ONNX" "$OPENCV_ZOO_RAW/face_detection_yunet/face_detection_yunet_2023mar.onnx"
fi
if [ ! -f "$FACE_RECOGNIZER_ONNX" ]; then
    curl -fL --retry 5 --retry-delay 2 -o "$FACE_RECOGNIZER_ONNX" "$OPENCV_ZOO_RAW/face_recognition_sface/face_recognition_sface_2021dec_int8.onnx"
fi
chown -R otc:otc "$MODEL_DIR"

# ---------------------------------------------------------------------------
# 4. Fetch source, build the binary, install the web bundle
# ---------------------------------------------------------------------------
log "[7/10] Fetch source"
if [ -d "$SRC_DIR/.git" ]; then
    git -C "$SRC_DIR" fetch --depth 1 origin main
    git -C "$SRC_DIR" reset --hard origin/main
else
    git clone --depth 1 "$REPO_URL" "$SRC_DIR"
fi

log "[8/10] Generate proto/generated (protoc + protoc-gen-go)"
# proto/generated/*.go is `make pb`'s protoc output, gitignored (not
# hand-edited, not committed). Generated here from the exact
# proto/messages.proto just cloned above, rather than fetching a separately
# published release tarball - a manually-cut release can only ever be as
# fresh as the last time someone remembered to cut one, so it silently fell
# behind main's own proto changes (undefined: pb.X build failures the
# moment a field/message was added or changed since that last release).
# Generating locally removes that whole class of bug. Only the Go bindings
# are needed here (proto/messages.proto defines no `service`, so there's
# nothing for --go-grpc_out/ts-proto/swift to generate that this build
# would use anyway - those other outputs are for web/iOS/macOS, built
# elsewhere from a dev machine via `make pb`).
if ! command -v protoc >/dev/null 2>&1 || ! protoc --version | grep -q " ${PROTOC_VERSION}$"; then
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/protoc.zip" "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PROTOC_ARCH}.zip"
    rm -rf /opt/protoc
    mkdir -p /opt/protoc
    (cd /opt/protoc && unzip -q "$tmp/protoc.zip")
    ln -sf /opt/protoc/bin/protoc /usr/local/bin/protoc
    rm -rf "$tmp"
fi
# Pin protoc-gen-go to the same version as go.mod's protobuf runtime
# (google.golang.org/protobuf) so the plugin and the runtime library the
# generated code links against stay compatible.
PROTOC_GEN_GO_VERSION=$(awk '/google.golang.org\/protobuf /{print $2}' "$SRC_DIR/go.mod")
[ -n "$PROTOC_GEN_GO_VERSION" ] || die "couldn't find google.golang.org/protobuf's version in $SRC_DIR/go.mod"
GOBIN=/usr/local/bin PATH="/usr/local/go/bin:$PATH" /usr/local/go/bin/go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"
mkdir -p "$SRC_DIR/proto/generated"
PATH="/usr/local/bin:$PATH" protoc -I="$SRC_DIR/proto" \
    --go_out="$SRC_DIR/proto/generated" --go_opt=paths=source_relative \
    "$SRC_DIR/proto/messages.proto"

log "[8/10] Build the otc binary"
(
    cd "$SRC_DIR"
    export CGO_ENABLED=1
    export CGO_CFLAGS="-I/opt/onnxruntime/include"
    export CGO_LDFLAGS="-L/opt/onnxruntime/lib -lonnxruntime"
    /usr/local/go/bin/go build -o /usr/bin/otc ./bin/otc.go
)

log "[9/10] Web app (built from source — always current with the cloned repo)"
mkdir -p /var/www
# Build from the cloned source so the web bundle always matches the Go binary
# (a prebuilt release tarball can lag main by days/weeks — the "stale UI"
# problem this replaced). Install Node.js 22 if it isn't already present.
if ! command -v node >/dev/null 2>&1 || [ "$(node -v 2>/dev/null | sed 's/v//' | cut -d. -f1)" -lt 20 ] 2>/dev/null; then
    log "installing Node.js 22 (needed to build the web app)..."
    curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
    apt-get install -y nodejs
fi
( cd "$SRC_DIR/web" && npm ci && npm run build )
cp -a "$SRC_DIR/web/dist/." /var/www/
chown -R otc:otc /var/www

log "[9/10] Runtime directories"
# /var/lib/otc/users: issue #82's per-user config/identity directories -
# under StateDirectory=otc (still writable under ProtectSystem=full,
# unlike /etc itself), one subdirectory per spawned user, created by the
# supervisor itself on demand - this just makes sure the parent exists
# with the right ownership up front.
mkdir -p /var/log/otc /etc/otc /var/lib/otc /var/lib/otc/users "$STORAGE_PATH" "$UNENC_PATH"
chown otc:otc /var/log/otc /var/www /var/lib/otc /var/lib/otc/users "$STORAGE_PATH" "$UNENC_PATH"
chmod 755 /var/log/otc

# ---------------------------------------------------------------------------
# 5. Database + config (device identity generated once, first run only)
# ---------------------------------------------------------------------------
log "[10/10] Database, config, and the systemd service"
# Point MariaDB's datadir at the RAID array (mirrors Makefile.pi's `mariadb`
# target) before touching it any further below - a recovered array already
# holding a previous device's /mysql/mysql is used as-is (the rsync is
# skipped); a fresh/empty array is seeded from the package's own
# just-initialized datadir instead. Skipped entirely under OTC_SKIP_RAID=1,
# where MariaDB just keeps using its normal default datadir.
if [ "$SKIP_RAID" != "1" ]; then
    log "Pointing MariaDB's datadir at $MOUNT_POINT..."
    systemctl stop mariadb || true
    mkdir -p "$MOUNT_POINT/mysql"
    if [ ! -d "$MOUNT_POINT/mysql/mysql" ]; then
        rsync -aHAX --numeric-ids /var/lib/mysql/ "$MOUNT_POINT/mysql/"
    fi
    chown -R mysql:mysql "$MOUNT_POINT/mysql"
    if grep -q "^datadir" /etc/mysql/mariadb.conf.d/50-server.cnf; then
        sed -i "s#^datadir.*#datadir = $MOUNT_POINT/mysql#" /etc/mysql/mariadb.conf.d/50-server.cnf
    else
        echo "datadir = $MOUNT_POINT/mysql" >> /etc/mysql/mariadb.conf.d/50-server.cnf
    fi
fi
systemctl enable --now mariadb
for i in $(seq 1 60); do
    mysqladmin ping >/dev/null 2>&1 && break
    if [ "$i" -eq 60 ]; then die "mariadb did not come up within 60s — check 'systemctl status mariadb'"; fi
    sleep 1
done

# DEVICE_UUID/BRIDGE_SECRET/OTC_DB_PASS below are only ever generated once
# per boot disk. On a disaster-recovery re-image, $ENV_FILE is gone (it
# lives on the boot disk/SD card, not the RAID array) even though the
# database on the array is the real, previous one - that's fine: every
# statement below is ALTER USER / INSERT ... WHERE NOT EXISTS, so a fresh
# password here just gets (re)applied to the recovered otc@localhost user
# and written into this device's own ini, and a pre-existing `settings` row
# (device_uuid/subdomain/bridge_secret) is left completely untouched rather
# than overwritten with these newly generated values.
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

# A heredoc, not `mysql -e "..."`, because this SQL carries prose comments
# and a double-quoted shell string is a minefield for those: bash runs
# anything in backticks and lets a stray double quote end the string
# early. Both bit a fresh Raspberry Pi install - a backticked phrase made
# bash execute an SQL comment ("uuid: No such file or directory", from
# otc_<uuid> reading as a redirect), and a quoted phrase inside another
# comment split the rest into words, leaving mysql to print its usage
# instead of running anything.
#
# Unquoted on purpose: ${OTC_DB_PASS} below still has to expand. Inside a
# heredoc quotes are literal, so the only rule left is no backticks and no
# $(...) in the SQL. Blocks that need no expansion at all use <<'"'"'SQL'"'"'
# (quoted) instead, which forbids even that.
mysql <<SQL
CREATE DATABASE IF NOT EXISTS otc;
CREATE USER IF NOT EXISTS 'otc'@'localhost' IDENTIFIED BY '${OTC_DB_PASS}';
ALTER USER 'otc'@'localhost' IDENTIFIED BY '${OTC_DB_PASS}';
GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';
-- Issue #82: multiple users on one device - the primary instance
-- provisions/drops a whole database+MySQL user per additional user at
-- runtime (see dao/provisioning.go). This needs ALL PRIVILEGES (not a
-- narrower CREATE/DROP/CREATE USER/GRANT OPTION set), because MySQL
-- requires a grantor to already hold whatever privileges it hands off:
-- GRANT OPTION only lets you re-delegate privileges you have, not grant
-- arbitrary ones. Provisioning a new user's database ends with
-- GRANT ALL PRIVILEGES ON otc_<uuid>.* TO otc_<uuid>@localhost, which
-- otc@localhost itself must hold on every database it might ever create -
-- i.e. globally - or that grant fails with "Access denied ... to database
-- 'otc_<uuid>'" (hit exactly this live during testing before widening the
-- grant to what's below). WITH GRANT OPTION also covers the SELECT this
-- account needs on every other user's database for the Users panel's
-- storage-usage metric (dao.UserStorageUsageMB), rather than separately
-- storing/managing each user's own dedicated DB password just for that.
GRANT ALL PRIVILEGES ON *.* TO 'otc'@'localhost' WITH GRANT OPTION;
FLUSH PRIVILEGES;
SQL
if mysql otc -N -B -e 'SHOW TABLES LIKE "files"' 2>/dev/null | grep -q files; then
    log "schema already present, applying any new tables/columns since your last update"
else
    tail -n +10 "$SRC_DIR/db/db.sql" | mysql otc
fi
# ---------------------------------------------------------------------------
# Schema migrations, applied to one database.
#
# Every statement in here is IF-NOT-EXISTS/idempotent so it can run against
# a brand-new database (where db.sql already created everything) and against
# one being upgraded, which is this script's normal path - a fresh db.sql
# load only ever happens once, on a device's very first install.
#
# A function taking the database name, rather than a straight run of
# `mysql otc`, because the primary database is no longer the only one:
# issue #82's additional users each get their own otc_<uuid> database,
# created from the schema embedded in whichever binary provisioned them
# (see dao.ProvisionUserDatabase). Those databases were never migrated by
# anything - so an additional user created before a column was added stayed
# broken forever, and every future migration would have skipped them too.
# Found exactly that way on a live device: a sub-user logging
# "Unknown column 'notifications_started' in 'SELECT'" once a minute,
# its friend sync broken, with the primary instance perfectly healthy.
apply_schema_migrations() {
    local db="$1"
    # Issue #52: face recognition tables/column, added after the schema-or-skip
    # check above - an existing install re-running this script (this script's
    # normal upgrade path) needs these applied explicitly, since a fresh
    # db.sql run only happens once, on this device's very first install. Every
    # statement here is IF-NOT-EXISTS/idempotent, safe to run on a fresh
    # install too (where db.sql just created them already).
    mysql "$db" <<'SQL'
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS face_recognition_enabled TINYINT(1) NOT NULL DEFAULT 0;
    -- Issue #92: ends a friend's notification catch-up suppression once their
    -- pre-existing backlog is fully replayed (see social.go's
    -- updateFriendEvents) - defaulting to 0 on an upgrade is fine even for
    -- already-fully-synced friends, since the very next sync cycle finds
    -- nothing pending and flips it back on within one 120s tick.
    ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS notifications_started TINYINT(1) NOT NULL DEFAULT 0;
    CREATE TABLE IF NOT EXISTS people (
      id VARCHAR(36) NOT NULL,
      name VARCHAR(150) NOT NULL DEFAULT '',
      created DATETIME NOT NULL,
      cover_face_id VARCHAR(36) DEFAULT NULL,
      cohesion FLOAT DEFAULT NULL,
      PRIMARY KEY (id)
    ) ENGINE=InnoDB;
    ALTER TABLE people ADD COLUMN IF NOT EXISTS cover_face_id VARCHAR(36) DEFAULT NULL;
    ALTER TABLE people ADD COLUMN IF NOT EXISTS cohesion FLOAT DEFAULT NULL;
    CREATE TABLE IF NOT EXISTS faces (
      id VARCHAR(36) NOT NULL,
      hash VARCHAR(64) NOT NULL,
      person_id VARCHAR(36) NOT NULL,
      bbox_x INT NOT NULL,
      bbox_y INT NOT NULL,
      bbox_w INT NOT NULL,
      bbox_h INT NOT NULL,
      embedding BLOB NOT NULL,
      thumbnail MEDIUMBLOB NOT NULL,
      created DATETIME NOT NULL,
      PRIMARY KEY (id),
      KEY (hash),
      KEY (person_id)
    ) ENGINE=InnoDB;
SQL
    # Issue #73: full-library reprocess, same idempotent-upgrade reasoning as
    # the face recognition block above.
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS reprocess_state (
      id TINYINT NOT NULL DEFAULT 1,
      status VARCHAR(20) NOT NULL DEFAULT 'idle',
      total INT NOT NULL DEFAULT 0,
      processed INT NOT NULL DEFAULT 0,
      last_hash VARCHAR(64) NOT NULL DEFAULT '',
      started DATETIME DEFAULT NULL,
      updated DATETIME DEFAULT NULL,
      PRIMARY KEY (id)
    ) ENGINE=InnoDB;
    INSERT INTO reprocess_state (id) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM reprocess_state WHERE id = 1);
SQL
    # Issue #78: owner-facing notification timeline (bell icon).
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS notifications (
      uuid VARCHAR(64) NOT NULL,
      dt DATETIME NOT NULL,
      type VARCHAR(32) NOT NULL,
      actor_name VARCHAR(255) NOT NULL,
      actor_domain VARCHAR(128) NOT NULL,
      pub_uuid VARCHAR(64) DEFAULT NULL,
      comment_uuid VARCHAR(64) DEFAULT NULL,
      acknowledged TINYINT(1) NOT NULL DEFAULT 0,
      UNIQUE (uuid),
      INDEX USING BTREE (acknowledged),
      INDEX USING BTREE (dt)
    ) ENGINE=InnoDB;
SQL
    # Issue #115: image groups (albums) - see db.sql for why members are
    # keyed by hash and carry no FK.
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS image_groups (
      id VARCHAR(36) NOT NULL,
      name VARCHAR(150) NOT NULL,
      created DATETIME NOT NULL,
      PRIMARY KEY (id)
    ) ENGINE=InnoDB;
    CREATE TABLE IF NOT EXISTS image_group_files (
      group_id VARCHAR(36) NOT NULL,
      hash VARCHAR(64) NOT NULL,
      added DATETIME NOT NULL,
      PRIMARY KEY (group_id, hash),
      KEY (hash)
    ) ENGINE=InnoDB;
SQL
}

apply_schema_migrations otc

# Issue #82: multiple OTC "users" on one device - only ever has real rows
# on the primary instance (see supervisor/supervisor.go's package doc).
mysql otc <<'SQL'
CREATE TABLE IF NOT EXISTS users (
  uuid VARCHAR(64) NOT NULL,
  username VARCHAR(64) NOT NULL,
  port INT NOT NULL,
  db_name VARCHAR(64) NOT NULL,
  db_pass VARCHAR(128) NOT NULL,
  storage_path VARCHAR(255) NOT NULL,
  subdomain VARCHAR(128) NOT NULL,
  bridge_secret VARCHAR(128) NOT NULL,
  supervisor_token VARCHAR(128) NOT NULL,
  active TINYINT(1) NOT NULL DEFAULT 1,
  created DATETIME NOT NULL,
  UNIQUE (uuid),
  UNIQUE (username),
  UNIQUE (port)
) ENGINE=InnoDB;
-- Issue #89: no is_admin/promote column - the original device owner is
-- the only admin there will ever be. Drops it for any device that already
-- ran an earlier version of this script before #89 removed the concept.
ALTER TABLE users DROP COLUMN IF EXISTS is_admin;
-- Issue #103: local-only users (created without requesting bridge access).
-- Defaults to 1 so every user that predates this keeps the bridge access
-- it already has.
ALTER TABLE users ADD COLUMN IF NOT EXISTS bridge_access TINYINT(1) NOT NULL DEFAULT 1;
SQL
# Issue #82 follow-up: bring every additional user's own database up to
# the same schema. Done here rather than beside the primary's own call
# above because the `users` table this reads is only created just above.
# A user whose database has gone missing entirely is skipped with a
# warning rather than failing the whole install - the rest of the device
# is fine, and failing here would block the upgrade for everyone else on
# it.
if mysql otc -N -B -e 'SHOW TABLES LIKE "users"' 2>/dev/null | grep -q users; then
    for user_db in $(mysql otc -N -B -e 'SELECT db_name FROM users' 2>/dev/null); do
        if mysql -N -B -e "SHOW DATABASES LIKE '$user_db'" 2>/dev/null | grep -q "$user_db"; then
            log "  migrating additional user database $user_db"
            apply_schema_migrations "$user_db"
        else
            log "  WARNING: user database $user_db is in the users table but doesn't exist - skipping"
        fi
    done
fi

mysql otc <<SQL
INSERT INTO settings (device_uuid, subdomain, bridge_secret)
SELECT '${DEVICE_UUID}', '${SUBDOMAIN}.${BRIDGE_ADDR}', '${BRIDGE_SECRET}'
WHERE NOT EXISTS (SELECT 1 FROM settings);
SQL

cat > "/etc/otc_${ENVIRONMENT}.ini" <<EOF
[otc]
bridge-addr=$BRIDGE_ADDR
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

# Issue #52: face recognition ("People" search), humans only - off by
# default (toggle it from Settings in the app), regardless of this section
# being present. See face_recognition/face_recognition.go's package doc
# comment for what these two models are.
[faces]
detector-model-path=$FACE_DETECTOR_ONNX
recognizer-model-path=$FACE_RECOGNIZER_ONNX
EOF

# Issue #97 follow-up: the RAID status LEDs (scripts/raid_watch.py, driven
# over GPIO) are installed by Makefile.pi's `raid-watch` target and baked
# into the prebuilt image by build_image.sh - but this installer, the path
# most devices are actually set up through, never installed them at all.
# A device brought up with `curl | bash` had a perfectly healthy array and
# two dark LEDs, with nothing on the device even hinting why (no service,
# no script, no log). Same unit the other two paths install, so all three
# agree.
log "Status LEDs (raid-watch.service)"
# The GPIO binding is genuinely optional: raid_watch.py degrades to a
# dry-run when it can't import one (see its RPi.GPIO import), which is the
# right behaviour on any non-Pi box - so a missing package must never fail
# the whole install. Package name differs across Raspberry Pi OS releases.
apt-get install -y python3-rpi-lgpio 2>/dev/null \
    || apt-get install -y python3-rpi.gpio 2>/dev/null \
    || log "  (no RPi GPIO package available - raid-watch will run in dry-run mode)"

if [ -f "$SRC_DIR/scripts/raid_watch.py" ]; then
    install -m 0755 "$SRC_DIR/scripts/raid_watch.py" /usr/local/bin/raid_watch.py

    cat > /etc/systemd/system/raid-watch.service <<'EOF'
[Unit]
Description=RAID1 watcher + LED driver
After=multi-user.target mdadm.service

[Service]
Type=simple
ExecStart=/usr/bin/python3 /usr/local/bin/raid_watch.py
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable raid-watch.service
    # Installed (and restarted on an upgrade) even with OTC_SKIP_RAID=1 or
    # no array yet: it polls mdadm and simply reports no members until one
    # exists, so it costs nothing to have running and means the LEDs light
    # up on their own whenever disks do get attached.
    systemctl restart raid-watch.service
else
    log "  WARNING: $SRC_DIR/scripts/raid_watch.py missing - skipping the status LEDs"
fi

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
echo " Storage: $([ "$SKIP_RAID" = "1" ] && echo "$MOUNT_POINT (single disk, OTC_SKIP_RAID=1)" || echo "RAID1 on $DISK1 + $DISK2, mounted at $MOUNT_POINT")"
echo "=========================================================="

# Write the completion marker only after EVERY step (including the systemd
# enable + restart) has succeeded. A run that died mid-install (a killed
# `curl | bash`, a failed step, ...) never gets this marker, which the
# preflight check at the top of the script uses to warn you that you're
# re-running against a half-installed box. The marker is NOT load-bearing
# for correctness — the script is idempotent either way — it's purely a
# "did the last run get all the way to the end?" signal.
# Issue #94: record which release this install corresponds to, so the
# in-place updater knows where to start. A fresh install already has
# everything every release script would have applied, so it starts at the
# newest one rather than replaying history. Taken from the manifest that
# was just downloaded with the rest of the source - and if that can't be
# read, the file is left absent, which the updater reads as "the
# beginning" and is safe because every release script is idempotent.
if [ -f "$SRC_DIR/scripts/updates/VERSIONS" ]; then
    latest_release="$(grep -vE '^[[:space:]]*(#|$)' "$SRC_DIR/scripts/updates/VERSIONS" | tail -1 | cut -f1)"
    if [ -n "$latest_release" ]; then
        echo "$latest_release" > /etc/otc/version
        echo "[otc-install] Recorded release $latest_release in /etc/otc/version"
    fi
fi

touch /etc/otc/.install-complete
echo "[otc-install] Done. Marker at /etc/otc/.install-complete (remove it if you want the next run to treat this as a fresh install)."
