#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# otc-entrypoint: Off The Cloud in a container (beta). It does at every start
# what scripts/install.sh and systemd do on a Raspberry Pi, as root under
# tini (PID 1):
#
#   run            (the default) check the volumes and ports, fetch the
#                  models, start MariaDB, bring the database and config up to
#                  this image, then run the device and watch both
#   set-password   the owner password, from stdin, while the device has none:
#                  docker exec -i otc otc-entrypoint set-password < file
#   dump           the nightly database copy, now: docker exec otc otc-entrypoint dump
#   restore [day]  load a night's copy (the newest by default) into a storage
#                  folder without a database, with the device stopped:
#                  docker compose run --rm otc restore
#
# The device runs as otc (uid 7310), unprivileged, as on a Pi; MariaDB as
# mysql (7311). Root never trusts what otc can write: nothing under
# /var/lib/otc or /mnt/storage is sourced or followed through a symbolic
# link, the release marker is checked before it is compared, and files there
# are written as otc.
#
# With OTC_MIRROR=1 (docker/compose.mirror.yaml) /mnt/storage is a mirror of
# two disks: otc-mirror builds or assembles it and mounts it there before
# anything reads the storage, watches it while the device runs and stops it
# after MariaDB. Without a mounted mirror the container doesn't start: it
# never falls back to the folder underneath.
set -uo pipefail

OTC_UID=7310
MYSQL_UID=7311
STORAGE=/mnt/storage
STATE=/var/lib/otc
CONF=/etc/otc
DATADIR=$STORAGE/mysql
MODELS=$STATE/models
DUMPS=$STORAGE/db-dumps
MARKER=$STORAGE/.otc-release
ENV_FILE=$CONF/otc-install.env
INI=$CONF/otc_dev.ini
LIVE_INI=/etc/otc_dev.ini
SETUP_WINDOW=$CONF/setup-window
MODE_FILE=$CONF/storage-mode
UPDATE_LOG=$CONF/update-log/update.log
SHARE=/usr/share/otc
RELEASE=$(cat "$SHARE/release")
MIRROR_TOOL=/usr/local/sbin/otc-mirror

# install.sh's pinned models (its step 6): the image carries none of them.
MODEL_HF_REPO=https://huggingface.co/anakhiu/ram-plus-onnx-int8/resolve/main
OPENCV_ZOO_RAW=https://github.com/opencv/opencv_zoo/raw/main/models
MODEL_LIST=(
    "ram_plus_swin_large_14m.int8.onnx $MODEL_HF_REPO/ram_plus_int8.onnx 44836da6724dcbdd7446632599c190426af96bd47d4457d47d876b019e10f8bd"
    "tag_list_4585_thresholds.txt $MODEL_HF_REPO/ram_tag_list_threshold.txt b6f81d0de1bc7f9c251af512c413eae976c77dbfbadf700d4e3d90510f4c6447"
    "face_detection_yunet_2023mar.onnx $OPENCV_ZOO_RAW/face_detection_yunet/face_detection_yunet_2023mar.onnx 8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4"
    "face_recognition_sface_2021dec_int8.onnx $OPENCV_ZOO_RAW/face_recognition_sface/face_recognition_sface_2021dec_int8.onnx 2b0e941e6f16cc048c20aee0c8e31f569118f65d702914540f7bfdc14048d78a"
)

DB_PID=
APP_PID=
APP_STARTED=0
TAIL_PID=
DL_PID=
LAST_DUMP=
RESTORE_DIR=
MIRROR_ARRAY=
MIRROR_DEV1=
MIRROR_DEV2=
MIRROR_LABEL1=
MIRROR_LABEL2=
MIRROR_UUID=
MIRROR_MOUNTED=0
WATCH_PID=
WATCH_STARTED=0

log() { echo "[otc] $*"; }

# die stops what this process started (the device, then MariaDB) and exits.
# Never called from a subshell: there it would only end the subshell.
die() {
    echo "[otc] ERROR: $*" >&2
    stop_all
    exit 1
}

# alive: the process exists and is not a zombie waiting to be reaped.
alive() {
    local s
    [ -n "$1" ] && s=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
    s=${s##*) }
    [ "${s%% *}" != Z ]
}

# as_otc runs a command as the otc user, with no way back to root.
as_otc() {
    HOME=$STATE USER=otc LOGNAME=otc setpriv --reuid "$OTC_UID" --regid "$OTC_UID" --init-groups --no-new-privs "$@"
}

# pause N: a sleep that a stop signal interrupts at once.
pause() {
    sleep "$1" &
    wait $! 2>/dev/null
}

# ---------------------------------------------------------------------------
# Settings from the environment
# ---------------------------------------------------------------------------
check_port() {
    [[ "$2" =~ ^[0-9]{1,5}$ ]] && (( 10#$2 >= 1 && 10#$2 <= 65535 )) \
        || die "$1 must be a port number from 1 to 65535, not \"$2\""
}
read_ports() {
    HTTP_PORT=${OTC_HTTP_PORT:-8080}
    check_port OTC_HTTP_PORT "$HTTP_PORT"
    HTTP_PORT=$((10#$HTTP_PORT))
    # The apps' encrypted home-network port: the device's own default is
    # the HTTP port + 363 (lantls.Port).
    LAN_TLS_PORT=${OTC_LAN_TLS_PORT:-$((HTTP_PORT + 363))}
    check_port OTC_LAN_TLS_PORT "$LAN_TLS_PORT"
    LAN_TLS_PORT=$((10#$LAN_TLS_PORT))
    DB_PORT=${OTC_DB_PORT:-3316}
    check_port OTC_DB_PORT "$DB_PORT"
    DB_PORT=$((10#$DB_PORT))
    if [ "$HTTP_PORT" = "$LAN_TLS_PORT" ] || [ "$HTTP_PORT" = "$DB_PORT" ] || [ "$LAN_TLS_PORT" = "$DB_PORT" ]; then
        die "OTC_HTTP_PORT, OTC_LAN_TLS_PORT and OTC_DB_PORT must be three different ports"
    fi
}

# ---------------------------------------------------------------------------
# 0. The two-disk mirror (OTC_MIRROR=1), before anything reads the storage
# ---------------------------------------------------------------------------
# mirror_prepare [--no-create]: otc-mirror builds, assembles or adopts the
# mirror of /dev/otc-disk1 and /dev/otc-disk2 and mounts it on $STORAGE.
# What it mounted comes back on stdout as key=value lines, each checked,
# never evaluated; what it says for people comes on stderr, and when it
# refuses, its last line is the reason the container stops with.
mirror_prepare() {
    local out err rc n k v line msg=()
    [ "${OTC_MIRROR:-0}" = 1 ] || return 0
    # A volume of its own for the database would be hidden under the
    # mirror, and a new database made on the mirror instead.
    mountpoint -q "$DATADIR" \
        && die "$DATADIR is a volume of its own, which the disk mirror would hide: remove that line from compose.yaml (with the mirror, the database lives on it)"
    log "preparing the disk mirror on ${OTC_MIRROR_DISK1:-?} and ${OTC_MIRROR_DISK2:-?}"
    err=$(mktemp) || die "could not create a temporary file"
    out=$("$MIRROR_TOOL" prepare "$@" 2>"$err")
    rc=$?
    while IFS= read -r line || [ -n "$line" ]; do msg+=("$line"); done < "$err"
    rm -f "$err"
    n=${#msg[@]}
    if [ "$rc" != 0 ]; then
        [ "$n" -gt 1 ] && printf '[otc-mirror] %s\n' "${msg[@]:0:n-1}" >&2
        [ "$n" -gt 0 ] && die "${msg[n-1]}"
        die "the disk mirror is not ready (otc-mirror stopped with exit $rc)"
    fi
    [ "$n" -gt 0 ] && printf '[otc-mirror] %s\n' "${msg[@]}"
    while IFS='=' read -r k v; do
        case "$k" in
            array) [[ "$v" =~ ^md[A-Za-z0-9_]{1,32}$ ]] && MIRROR_ARRAY=$v ;;
            dev1) [[ "$v" =~ ^[0-9]{1,4}:[0-9]{1,7}$ ]] && MIRROR_DEV1=$v ;;
            dev2) [[ "$v" =~ ^[0-9]{1,4}:[0-9]{1,7}$ ]] && MIRROR_DEV2=$v ;;
            label1) [[ "$v" =~ ^[^[:cntrl:]]{1,255}$ ]] && MIRROR_LABEL1=$v ;;
            label2) [[ "$v" =~ ^[^[:cntrl:]]{1,255}$ ]] && MIRROR_LABEL2=$v ;;
            uuid) [[ "$v" =~ ^[0-9a-f]{8}(:[0-9a-f]{8}){3}$ ]] && MIRROR_UUID=$v ;;
        esac
    done <<< "$out"
    [ -n "$MIRROR_ARRAY" ] || die "otc-mirror didn't say which array it mounted"
    MIRROR_MOUNTED=1
    # One disk may be /dev/null in .env (gone, until its replacement is named).
    [ -n "$MIRROR_DEV1" ] || [ -n "$MIRROR_DEV2" ] || die "otc-mirror didn't say which disks the mirror is on"
    [ -n "$MIRROR_UUID" ] || die "otc-mirror didn't say which mirror it mounted"
}

# Where this device's files are, kept in $MODE_FILE once it starts: "folder"
# (the storage volume) or "mirror <its UUID>". Dropping the mirror from .env
# by mistake (or adding it to a device that has its files in the folder)
# would otherwise start an empty device on the other one, and a device whose
# mirror isn't on the disks named never starts over on a new one by itself.
# Deleting the file is how to choose the other storage on purpose.
storage_mode_check() {
    local mode='' uuid=''
    if [ -e "$MODE_FILE" ] || [ -L "$MODE_FILE" ]; then
        [ -f "$MODE_FILE" ] && [ ! -L "$MODE_FILE" ] || die "$MODE_FILE isn't a plain file: delete it"
        read -r mode uuid < "$MODE_FILE"
    fi
    case "${OTC_MIRROR:-0}:$mode" in
        1:mirror)
            [[ "$uuid" =~ ^[0-9a-f]{8}(:[0-9a-f]{8}){3}$ ]] || die "$MODE_FILE names no mirror: delete it"
            export OTC_MIRROR_UUID=$uuid ;;
        1:folder)
            die "this device keeps its files in the storage folder, not on a disk mirror: remove the mirror lines (COMPOSE_FILE, OTC_MIRROR_*) from .env. Moving the files onto a mirror isn't supported yet; to start over with a new, empty device on the mirror, move the storage folder's contents somewhere else and delete storage-mode in the device's config folder" ;;
        1:)
            # The folder under the mirror, before it is mounted there.
            [ -d "$DATADIR/otc" ] \
                && die "the storage folder already holds a device's files: moving them onto a disk mirror isn't supported yet. Remove the mirror lines from .env to go on with them, or empty the folder to start a new device on the mirror" ;;
        0:mirror)
            die "this device keeps its files on its two-disk mirror: put the mirror lines back in .env (COMPOSE_FILE=compose.yaml:compose.mirror.yaml, OTC_MIRROR_DISK1 and OTC_MIRROR_DISK2; docs/docker.md). To start over with a new, empty device in the storage folder instead, delete storage-mode in the device's config folder (the mirror stays as it is)" ;;
        0:folder|0:) ;;
        *) die "$MODE_FILE says '$mode': delete it" ;;
    esac
}

storage_mode_record() {
    local want=folder
    [ -n "$MIRROR_ARRAY" ] && want="mirror $MIRROR_UUID"
    [ -f "$MODE_FILE" ] && [ "$(head -c 128 "$MODE_FILE")" = "$want" ] && return 0
    { printf '%s\n' "$want" > "$MODE_FILE.tmp" && chmod 0644 "$MODE_FILE.tmp" && mv -f "$MODE_FILE.tmp" "$MODE_FILE"; } \
        || die "could not write $MODE_FILE"
}

# The mirror's watcher (its state, a rebuild's progress, growing onto bigger
# disks, in the storage log): a root child of this script, started again by
# supervise when it stops, at most every 10 s.
start_watch() {
    env --default-signal "$MIRROR_TOOL" watch --array "$MIRROR_ARRAY" &
    WATCH_PID=$!
    WATCH_STARTED=$(date +%s)
}

check_watch() {
    local rc
    [ -n "$MIRROR_ARRAY" ] || return 0
    if [ -n "$WATCH_PID" ] && ! alive "$WATCH_PID"; then
        wait "$WATCH_PID" 2>/dev/null
        rc=$?
        WATCH_PID=
        log "WARNING: the disk mirror's watcher stopped (exit $rc): starting it again"
    fi
    if [ -z "$WATCH_PID" ] && [ $(( $(date +%s) - WATCH_STARTED )) -ge 10 ]; then
        start_watch
    fi
}

stop_watch() {
    local i
    [ -n "$WATCH_PID" ] || return 0
    kill -TERM "$WATCH_PID" 2>/dev/null
    for ((i = 0; i < 10; i++)); do
        alive "$WATCH_PID" || break
        pause 1
    done
    kill -KILL "$WATCH_PID" 2>/dev/null
    wait "$WATCH_PID" 2>/dev/null
    WATCH_PID=
}

# mirror_release, once MariaDB and the device have stopped: the mirror
# unmounted and its array stopped, each step best effort (an array still
# busy is adopted at the next start).
mirror_release() {
    [ "$MIRROR_MOUNTED" = 1 ] || return 0
    MIRROR_MOUNTED=0
    "$MIRROR_TOOL" release --array "$MIRROR_ARRAY" 2>&1 | sed 's/^/[otc-mirror] /'
}

# ---------------------------------------------------------------------------
# 1. What the device needs from the host: refused with the fix, or warned
# ---------------------------------------------------------------------------
# awk reads every line rather than grep -q stopping at the first match:
# with pipefail, the writer cut off by that early stop (SIGPIPE) would turn
# a busy port into a free one on a server with many listeners.
port_busy() {
    ss -Hltn 2>/dev/null | awk -v p="$1" '{ n = split($4, a, ":"); if (a[n] == p) f = 1 } END { exit !f }'
}

preflight() {
    local p where fs swap

    if [ "${OTC_ALLOW_EPHEMERAL:-0}" != 1 ]; then
        for p in "$STORAGE" "$STATE" "$CONF"; do
            mountpoint -q "$p" || die "$p is not a volume: mount a folder of this server there (docker/compose.yaml), or what it holds is lost when the container is recreated"
        done
    fi
    # Decrypted photos and videos being processed are written there, and
    # they must stay in memory. Without it the device falls back to /dev/shm,
    # which Docker makes 64 MB: too small for a video.
    [ "$(stat -f -c %T /tmp)" = tmpfs ] \
        || die "/tmp is not a RAM filesystem: add tmpfs: [\"/tmp:size=4g,mode=1777\"] to the service (docker/compose.yaml)"
    # MariaDB needs a local filesystem's locks and syncs.
    where=$DATADIR
    [ -d "$where" ] || where=$STORAGE
    fs=$(stat -f -c %T "$where")
    case "$fs" in
        fuse*|nfs*|cifs|smb*)
            die "the database can't live on $where, a $fs filesystem (network or FUSE): keep the folder on a local disk, or mount a local folder at $DATADIR (docker/compose.yaml)" ;;
    esac
    # The device shares the server's network: a port another program holds
    # would leave it without a listener, and its own loopback calls going to
    # that program.
    for p in "OTC_HTTP_PORT:$HTTP_PORT" "OTC_LAN_TLS_PORT:$LAN_TLS_PORT" "OTC_DB_PORT:$DB_PORT"; do
        port_busy "${p#*:}" && die "port ${p#*:} is already in use on this server: set ${p%%:*} to a free port"
    done
    # The HEIC decoder (goheif's libde265) is built for SSE4.1 on amd64.
    if [ "$(uname -m)" = x86_64 ] && ! grep -qw sse4_1 /proc/cpuinfo; then
        die "this processor lacks SSE4.1, which the image's photo decoder needs"
    fi

    # As the Pi's otc.service: no core files (LimitCORE=0) - a crash would
    # write decrypted memory to the working directory, which is a volume here
    # - and 65535 open files (LimitNOFILE).
    ulimit -c 0
    p=$(ulimit -Hn)
    if [ "$p" = unlimited ] || [ "$p" -ge 65535 ]; then ulimit -n 65535; else ulimit -n "$p"; fi

    [ "$(ulimit -l)" = unlimited ] \
        || log "WARNING: locked memory is limited to $(ulimit -l) KB, so decrypted data could be swapped out: set ulimits: memlock: -1 (docker/compose.yaml)"
    if [ "$(wc -l < /proc/swaps)" -gt 1 ]; then
        swap=$(cat /sys/fs/cgroup/memory.swap.max 2>/dev/null || echo unknown)
        [ "$swap" = 0 ] \
            || log "WARNING: this server has swap and the container may use it: set memswap_limit to the same value as mem_limit, so decrypted data never reaches a disk"
    fi
    if [ -r /proc/device-tree/model ] && tr -d '\0' < /proc/device-tree/model | grep -q 'Raspberry Pi'; then
        log "On a Raspberry Pi, use the Off The Cloud image instead: it builds and watches the disk mirror (https://off-the.cloud)"
    fi
}

# ---------------------------------------------------------------------------
# 2. Directories (install.sh's, not recursive)
# ---------------------------------------------------------------------------
# foreign: the directory belongs to someone other than root (a new mount) or
# uid, and holds something - another install's files, which are not taken
# over silently.
foreign() {
    local owner
    owner=$(stat -c %u "$1")
    [ "$owner" != 0 ] && [ "$owner" != "$2" ] && [ -n "$(ls -A "$1" 2>/dev/null | head -n 1)" ]
}

own_dir() {
    [ -L "$1" ] && die "$1 is a symbolic link: refusing to use it"
    mkdir -p "$1" || die "could not create $1"
    chown -h "$2" "$1" && chmod "$3" "$1" || die "could not set the owner of $1"
}

prepare_dirs() {
    foreign "$STORAGE" "$OTC_UID" \
        && die "$STORAGE holds files of user $(stat -c %u "$STORAGE"), not of $OTC_UID: use an empty folder. In the container the files belong to uid $OTC_UID and the database to uid $MYSQL_UID; moving another install's files in is not supported in the beta"
    if [ -d "$DATADIR" ] && [ ! -L "$DATADIR" ] && foreign "$DATADIR" "$MYSQL_UID"; then
        die "$DATADIR holds files of user $(stat -c %u "$DATADIR"), not of $MYSQL_UID (mysql): if its owner was changed by hand, give it back on this server with chown -R $MYSQL_UID:$MYSQL_UID <your folder>/storage/mysql; otherwise use an empty folder"
    fi
    # Others may pass through the storage root to MariaDB's datadir, but
    # not list it (install.sh).
    own_dir "$STORAGE" otc:otc 0751
    own_dir "$STORAGE/unencrypted" otc:otc 0750
    own_dir "$DATADIR" mysql:mysql 0700
    own_dir "$STATE" otc:otc 0755
    own_dir "$STATE/users" otc:otc 0755
    own_dir "$STATE/log" otc:otc 0755
    own_dir "$MODELS" otc:otc 0755
    # Root's: the config, the identity and the update log, which the device
    # reads but must not rewrite.
    own_dir "$CONF" root:otc 0750
    own_dir "$CONF/update-log" root:root 0755
}

# ---------------------------------------------------------------------------
# 3. Models: downloaded as otc, moved into place only once their hash matches
# ---------------------------------------------------------------------------
# download_failed waits before giving up: Docker starts the container again
# at once after a run longer than 10 s, so a model that keeps failing (a
# hash that no longer matches, a host that cuts the transfer) would
# otherwise be downloaded again and again, 870 MB each time. A stop signal
# still ends the wait.
download_failed() {
    log "ERROR: $1 - trying again in an hour (or restart the container)"
    pause 3600
    die "$1"
}

fetch_model() {
    local name=$1 url=$2 sha=$3 dest part size secs=0
    dest=$MODELS/$name
    part=$dest.part
    [ -f "$dest" ] && [ ! -L "$dest" ] && return 0
    log "downloading $name"
    as_otc rm -f -- "$part"
    as_otc curl -fsSL --retry 5 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$part" "$url" &
    DL_PID=$!
    # Progress in the container's log every 15 s, for the large one.
    while alive "$DL_PID"; do
        pause 1
        secs=$((secs + 1))
        if [ $((secs % 15)) = 0 ]; then
            size=$(stat -c %s "$part" 2>/dev/null || echo 0)
            log "  $name: $((size >> 20)) MB"
        fi
    done
    wait "$DL_PID" || { DL_PID=; as_otc rm -f -- "$part"; download_failed "could not download $url"; }
    DL_PID=
    if [ "$(as_otc sha256sum "$part" | cut -d' ' -f1)" != "$sha" ]; then
        as_otc rm -f -- "$part"
        download_failed "$url does not match its pinned hash"
    fi
    as_otc mv -f -- "$part" "$dest" || die "could not move $name into place"
}

fetch_models() {
    local m
    for m in "${MODEL_LIST[@]}"; do
        # shellcheck disable=SC2086
        fetch_model $m
    done
    # The tag list comes with the image (install.sh takes it from the release).
    as_otc sh -c 'gunzip < "$1" > "$2.tmp" && mv -f "$2.tmp" "$2"' sh "$SHARE/tag_list_4585.txt.gz" "$MODELS/tag_list_4585.txt" \
        || die "could not unpack the tag list"
}

# ---------------------------------------------------------------------------
# 4. MariaDB
# ---------------------------------------------------------------------------
db_failed() {
    tail -n 30 "$DATADIR/mariadb.err" >&2 2>/dev/null
    die "$1"
}

# The memory the device is sized to, in MB, as it reads it itself
# (container.MemorySizing): the container's memory limit, else
# OTC_MEMORY_GB, else 8 GB - never more than the machine has. Only the
# container's own cgroup is read here; the device also follows a limit set
# on a cgroup above it.
device_memory_mb() {
    local machine limit raw gb
    machine=$(awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo 2>/dev/null)
    limit=""
    if raw=$(cat /sys/fs/cgroup/memory.max 2>/dev/null) || raw=$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null); then
        case "$raw" in
            ''|max|*[!0-9]*) ;;
            *) [ "${#raw}" -lt 19 ] && limit=$((raw / 1048576)) ;; # v1 says "no limit" with ~2^63
        esac
    fi
    gb=$(awk -v v="${OTC_MEMORY_GB:-}" 'BEGIN { if (v ~ /^[0-9]+(\.[0-9]+)?$/ && v + 0 > 0) print int(v * 1024) }')
    local mem=${machine:-0}
    [ -n "$limit" ] && [ "$limit" -gt 0 ] && { [ "$mem" -eq 0 ] || [ "$limit" -lt "$mem" ]; } && mem=$limit
    if [ -n "$gb" ]; then
        { [ "$mem" -eq 0 ] || [ "$gb" -lt "$mem" ]; } && mem=$gb
    elif [ -z "$limit" ]; then
        { [ "$mem" -eq 0 ] || [ 8192 -lt "$mem" ]; } && mem=8192
    fi
    echo "$mem"
}

# MariaDB's buffer pool for that memory, in MB, as on a Raspberry Pi
# (scripts/install.sh, release 143): 256 with 4 GB, 768 with 8 GB, 1024 from
# 12 GB; under 3 GB MariaDB's own 128.
buffer_pool_mb() {
    local mem
    mem=$(device_memory_mb)
    if [ "$mem" -lt 3000 ]; then
        echo 128
    elif [ "$mem" -lt 6144 ]; then
        echo 256
    elif [ "$mem" -lt 12288 ]; then
        echo 768
    else
        echo 1024
    fi
}

start_mariadb() {
    local i out pool
    install -d -o mysql -g mysql -m 0755 /run/mysqld || die "could not create /run/mysqld"
    if [ ! -d "$DATADIR/mysql" ]; then
        log "creating the database in $DATADIR"
        out=$(mariadb-install-db --user=mysql --datadir="$DATADIR" --auth-root-authentication-method=socket --skip-test-db 2>&1) \
            || { echo "$out" | tail -n 30 >&2; die "could not create the database"; }
    fi
    pool=$(buffer_pool_mb)
    log "MariaDB's buffer pool: $pool MB"
    env --default-signal mariadbd --user=mysql --port="$DB_PORT" --innodb-buffer-pool-size="${pool}M" &
    DB_PID=$!
    # Recovering after a power cut can take minutes on slow disks: wait as
    # long as MariaDB is at it, up to the 15 minutes Debian's own service
    # gives it.
    for ((i = 1; ; i++)); do
        mariadb-admin ping >/dev/null 2>&1 && break
        alive "$DB_PID" || db_failed "MariaDB stopped while starting (its log is $DATADIR/mariadb.err; a second container on the same folder is refused this way)"
        [ "$i" -ge 900 ] && db_failed "MariaDB did not come up within 15 minutes"
        [ $((i % 30)) = 0 ] && log "MariaDB is still starting ($i s; its log is $DATADIR/mariadb.err)"
        pause 1
    done
    # A datadir from an older MariaDB is brought up to this one; on a
    # current one this only says there is nothing to do.
    out=$(mariadb-upgrade 2>&1) || { echo "$out" | tail -n 30 >&2; die "could not upgrade the database to this MariaDB"; }
    case "$out" in
        *"no need to run"*) ;;
        *) log "the database was upgraded to $(mariadbd --version | awk '{print $3}')" ;;
    esac
}

stop_mariadb() {
    [ -n "$DB_PID" ] || return 0
    if alive "$DB_PID"; then
        mariadb-admin shutdown >/dev/null 2>&1 || kill -TERM "$DB_PID" 2>/dev/null
    fi
    wait "$DB_PID" 2>/dev/null
    DB_PID=
}

# ---------------------------------------------------------------------------
# 5. Identity, made once (install.sh), kept in the config volume
# ---------------------------------------------------------------------------
# The env file is root's, but it is read line by line, never sourced.
load_identity() {
    local k v changed=0
    DEVICE_UUID='' BRIDGE_SECRET='' OTC_DB_PASS=''
    if [ -f "$ENV_FILE" ]; then
        while IFS='=' read -r k v; do
            case "$k" in
                DEVICE_UUID) [[ "$v" =~ ^[0-9a-fA-F-]{16,64}$ ]] && DEVICE_UUID=$v ;;
                BRIDGE_SECRET) [[ "$v" =~ ^[0-9a-fA-F]{32,128}$ ]] && BRIDGE_SECRET=$v ;;
                OTC_DB_PASS) [[ "$v" =~ ^[A-Za-z0-9]{16,128}$ ]] && OTC_DB_PASS=$v ;;
            esac
        done < "$ENV_FILE"
    fi
    [ -n "$DEVICE_UUID" ] || { DEVICE_UUID=$(cat /proc/sys/kernel/random/uuid); changed=1; }
    [ -n "$BRIDGE_SECRET" ] || { BRIDGE_SECRET=$(openssl rand -hex 24); changed=1; }
    [ -n "$OTC_DB_PASS" ] || { OTC_DB_PASS=$(openssl rand -base64 24 | tr -d '=+/'); changed=1; }
    [ "$changed" = 1 ] || return 0
    (
        umask 077
        {
            echo "# Generated $(date -u +%FT%TZ) by otc-entrypoint."
            echo "# Keep this file private: it holds this device's database password and bridge secret."
            echo "DEVICE_UUID=$DEVICE_UUID"
            echo "BRIDGE_SECRET=$BRIDGE_SECRET"
            echo "OTC_DB_PASS=$OTC_DB_PASS"
        } > "$ENV_FILE.tmp" && mv -f "$ENV_FILE.tmp" "$ENV_FILE"
    ) || die "could not write $ENV_FILE"
}

# ---------------------------------------------------------------------------
# 6. Database user, schema and migrations
# ---------------------------------------------------------------------------
# install.sh's grants (see there for why the device's user holds them on
# every database), at every start: the password in the config always
# matches, a recovered database included.
apply_grants() {
    mysql <<SQL || die "could not set up the database user"
CREATE DATABASE IF NOT EXISTS otc;
CREATE USER IF NOT EXISTS 'otc'@'localhost' IDENTIFIED BY '${OTC_DB_PASS}';
ALTER USER 'otc'@'localhost' IDENTIFIED BY '${OTC_DB_PASS}';
GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';
GRANT ALL PRIVILEGES ON *.* TO 'otc'@'localhost' WITH GRANT OPTION;
FLUSH PRIVILEGES;
SQL
}

load_schema() {
    if ! mysql otc -N -B -e 'SHOW TABLES LIKE "files"' 2>/dev/null | grep -q files; then
        log "loading the database schema"
        tail -n +10 "$SHARE/db.sql" | mysql otc || die "could not load the database schema"
    fi
}

# The marker on the storage volume: the release whose migrations last ran
# on this database. Written by otc; only a plain number is believed.
read_marker() {
    local v=''
    [ -f "$MARKER" ] && [ ! -L "$MARKER" ] && v=$(head -c 16 "$MARKER" 2>/dev/null | head -n 1)
    [[ "$v" =~ ^[0-9]{1,9}$ ]] && printf '%s' "$((10#$v))"
}

write_marker() {
    printf '%s\n' "$RELEASE" | as_otc sh -c 'cat > "$1.tmp" && mv -f "$1.tmp" "$1"' sh "$MARKER" \
        || die "could not write $MARKER"
}

# install.sh's own schema block (taken out between its marks when the image
# was built), run on the primary database and every user's, logged where
# Settings > Logs > Updates reads.
run_migrations() {
    local from=$1 rc
    echo "=== container image $RELEASE: database migrations (last run by ${from:-no release}) $(date -u +%Y-%m-%dT%H:%M:%SZ) ===" \
        | tee -a "$UPDATE_LOG"
    (
        set -euo pipefail
        # shellcheck disable=SC1091
        source "$SHARE/schema-migrations.sh"
    ) 2>&1 | tee -a "$UPDATE_LOG"
    rc=${PIPESTATUS[0]}
    if [ "$rc" -ne 0 ]; then
        echo "=== failed (exit $rc) ===" | tee -a "$UPDATE_LOG"
        die "the database migrations failed: see Settings > Logs > Updates ($UPDATE_LOG)"
    fi
    echo "=== done ===" | tee -a "$UPDATE_LOG"
}

# An image older than the storage's last one is refused before MariaDB
# starts: neither MariaDB nor the database can be taken back.
check_downgrade() {
    local marker
    marker=$(read_marker)
    if [ -n "$marker" ] && [ "$marker" -gt "$RELEASE" ]; then
        [ "${OTC_ALLOW_DOWNGRADE:-0}" = 1 ] \
            || die "this storage was last used by release $marker, newer than this image ($RELEASE): use that image or a newer one. Its database may not work with older code; OTC_ALLOW_DOWNGRADE=1 starts anyway"
        log "WARNING: starting release $RELEASE on a database last used by release $marker (OTC_ALLOW_DOWNGRADE=1)"
    fi
}

# migrate [always]: the migrations, when this image's release is not the
# one that last ran them (or always, after a restore).
migrate() {
    local marker
    marker=$(read_marker)
    if [ "$marker" != "$RELEASE" ] || [ "${1:-}" = always ]; then
        run_migrations "$marker"
        write_marker
    fi
}

# The identity row, only into an empty settings table: a database that has
# one (a recovered volume, a restored copy) keeps it. Local-only, named otc,
# like the setup wizard's "Continue without an account".
insert_identity() {
    mysql otc <<SQL || die "could not store the device's identity"
INSERT INTO settings (device_uuid, subdomain, bridge_secret)
SELECT '${DEVICE_UUID}', 'otc', '${BRIDGE_SECRET}'
WHERE NOT EXISTS (SELECT 1 FROM settings);
SQL
}

# ---------------------------------------------------------------------------
# 7. Config: install.sh's template, rewritten at every start
# ---------------------------------------------------------------------------
ini_get() {
    awk -v sec="[$2]" -v key="$3" '
        /^[[:space:]]*\[/ { s = $0; gsub(/[[:space:]]/, "", s); next }
        s == sec && index($0, "=") {
            k = substr($0, 1, index($0, "=") - 1); gsub(/[[:space:]]/, "", k)
            if (k == key) {
                v = substr($0, index($0, "=") + 1); sub(/^[[:space:]]+/, "", v); sub(/[[:space:]]+$/, "", v)
                print v; exit
            }
        }' "$1" 2>/dev/null
}

# kept: a value from the previous config worth keeping (the bridge switch,
# the owner's own settings), only when it is plain text.
kept() {
    local v
    v=$(ini_get "$INI" "$1" "$2")
    [[ "$v" =~ ^[A-Za-z0-9._:/,-]*$ ]] && printf '%s' "$v"
}

write_config() {
    local bridge_addr bridge_default proxies extra='' k v level size
    bridge_addr=$(kept otc bridge-addr)
    bridge_default=${OTC_BRIDGE_DEFAULT:-$(kept otc bridge-default)}
    bridge_default=${bridge_default:-off-the.cloud}
    [[ "$bridge_default" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] \
        || die "OTC_BRIDGE_DEFAULT must be a host name, not \"$bridge_default\""
    proxies=${OTC_TRUSTED_PROXIES:-}
    [[ "$proxies" =~ ^[0-9a-fA-F.:/,\ ]*$ ]] \
        || die "OTC_TRUSTED_PROXIES must be addresses or ranges (CIDR) separated by commas"
    for k in demo demo-password low-memory disable-mlock update-repo update-releases; do
        v=$(kept otc "$k")
        [ -z "$v" ] || extra+="$k=$v"$'\n'
    done
    level=$(kept logger level)
    size=$(kept logger max_log_size_mb)
    (
        umask 077
        cat > "$INI.tmp" <<EOF
# Written by otc-entrypoint at every start, from the container's environment
# (docker/compose.yaml); only the bridge setting and a few of the owner's own
# lines are kept from the last one.
[otc]
bridge-addr=$bridge_addr
bridge-default=$bridge_default
storage-path=$STORAGE/
unenc-storage-path=$STORAGE/unencrypted/
max-thumbnail-width-px=1000
shared-link-ttl-hours=168
wifi-watchdog=off
${extra}
[logger]
log_file=/var/log/otc/otc.log
max_log_size_mb=${size:-10}
level=${level:-info}

[otc-api]
base-url=otc/
static=/var/www/
port=$HTTP_PORT
lan-tls-port=$LAN_TLS_PORT
trusted-proxies=$proxies
ssl-port=443
ssl-cert=
ssl-key=

[mysql]
user=otc
pass=$OTC_DB_PASS
port=$DB_PORT
db=otc

[tagger]
model-path=$MODELS/ram_plus_swin_large_14m.int8.onnx
tags-path=$MODELS/tag_list_4585.txt
thresholds-path=$MODELS/tag_list_4585_thresholds.txt
tags-per-image=10
max-images-search=30

[faces]
detector-model-path=$MODELS/face_detection_yunet_2023mar.onnx
recognizer-model-path=$MODELS/face_recognition_sface_2021dec_int8.onnx
EOF
    ) && chown root:otc "$INI.tmp" && chmod 0640 "$INI.tmp" && mv -f "$INI.tmp" "$INI" \
        || die "could not write $INI"
}

# The device reads /etc/otc_dev.ini (cfg.Init), which the bridge runner
# replaces in place: a copy of the volume's, refreshed before every start.
install_live_config() {
    install -m 0640 -o root -g otc "$INI" "$LIVE_INI" || die "could not install $LIVE_INI"
}

# ---------------------------------------------------------------------------
# 8. Version and release key, which the update check reads
# ---------------------------------------------------------------------------
install_release_files() {
    install -m 0644 -o root -g root "$SHARE/release" "$CONF/version" \
        && install -m 0644 -o root -g root "$SHARE/release-signing.pub" "$CONF/release-signing.pub" \
        || die "could not write $CONF/version"
}

# ---------------------------------------------------------------------------
# 9. Owner password
# ---------------------------------------------------------------------------
vault_empty() {
    [ "$(mysql otc -N -B -e 'SELECT COUNT(*) FROM vault' 2>/dev/null)" = 0 ]
}

# init_password: the password on stdin, set as the first sign-in would
# (bin/otc.go's init-owner-password, as install.sh runs it); one already
# set is left alone.
init_password() {
    ( cd "$STATE" && as_otc /usr/bin/otc dev init-owner-password )
}

setup_password() {
    local until=''
    if [ -n "${OTC_OWNER_PASSWORD_FILE:-}" ]; then
        if [ ! -r "$OTC_OWNER_PASSWORD_FILE" ]; then
            log "WARNING: OTC_OWNER_PASSWORD_FILE ($OTC_OWNER_PASSWORD_FILE) can't be read"
        elif vault_empty; then
            log "setting the owner password from OTC_OWNER_PASSWORD_FILE"
            init_password < "$OTC_OWNER_PASSWORD_FILE" || die "could not set the owner password from $OTC_OWNER_PASSWORD_FILE"
        else
            log "WARNING: the device has its owner password: remove OTC_OWNER_PASSWORD_FILE and its secret from the compose file"
        fi
    fi
    # Until when the web app may set the first password (OTC_SETUP_UNTIL,
    # Unix seconds): 30 minutes from the first start, kept so a restart
    # doesn't extend it.
    if [ -f "$SETUP_WINDOW" ]; then
        until=$(head -c 16 "$SETUP_WINDOW" | head -n 1)
    elif vault_empty; then
        until=$(( $(date +%s) + 1800 ))
        echo "$until" > "$SETUP_WINDOW"
    fi
    if [[ "$until" =~ ^[0-9]{1,12}$ ]]; then
        export OTC_SETUP_UNTIL=$until
        if vault_empty; then
            if [ "$until" -gt "$(date +%s)" ]; then
                log "open http://<this server>:$HTTP_PORT from your home network before $(date -u -d "@$until" '+%H:%M UTC') to choose the owner password"
            else
                log "no owner password yet: docker exec -i otc otc-entrypoint set-password < <a file holding it>"
            fi
        fi
    fi
}

# ---------------------------------------------------------------------------
# 10. The device's process, and what it asks the root side for
# ---------------------------------------------------------------------------
# start_app runs the device in a process group of its own, so stopping it
# stops the users' processes it started too (systemd's control group does
# that on a Pi). Its OOM score is raised so the kernel picks it before
# MariaDB; lowering it back would need a privilege it doesn't have.
# (A background job of this script starts with SIGINT ignored; env puts
# every signal back to its default, as systemd starts a service.)
start_app() {
    local mirror=()
    # The mirror the device watches and names in its alerts: its array, and
    # each disk's major:minor and name (the device's environment only).
    [ -z "$MIRROR_ARRAY" ] || mirror=(
        OTC_MIRROR_ARRAY="$MIRROR_ARRAY" OTC_MIRROR_DEV1="$MIRROR_DEV1" OTC_MIRROR_DEV2="$MIRROR_DEV2"
        OTC_MIRROR_LABEL1="$MIRROR_LABEL1" OTC_MIRROR_LABEL2="$MIRROR_LABEL2"
    )
    install_live_config
    (
        cd "$STATE" || exit 1
        echo 500 > /proc/self/oom_score_adj
        exec setsid env --default-signal HOME="$STATE" USER=otc LOGNAME=otc "${mirror[@]}" \
            setpriv --reuid "$OTC_UID" --regid "$OTC_UID" --init-groups --no-new-privs /usr/bin/otc dev
    ) &
    APP_PID=$!
    APP_STARTED=$(date +%s)
    log "device started (release $RELEASE, port $HTTP_PORT)"
}

# stop_app: SIGTERM to the device's whole group, SIGKILL to what is left
# after a minute.
stop_app() {
    local i
    [ -n "$APP_PID" ] || return 0
    kill -TERM -- "-$APP_PID" 2>/dev/null
    for ((i = 0; i < 60; i++)); do
        kill -0 -- "-$APP_PID" 2>/dev/null || break
        pause 1
    done
    kill -KILL -- "-$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
    APP_PID=
}

stop_all() {
    [ "$BASHPID" = "$$" ] || return 0
    stop_watch
    [ -z "$DL_PID" ] || kill -TERM "$DL_PID" 2>/dev/null
    stop_app
    [ -z "$TAIL_PID" ] || kill -TERM "$TAIL_PID" 2>/dev/null
    stop_mariadb
    mirror_release
}

# trigger NAME: the device asked for something with $STATE/NAME. Only a
# plain file counts; anything else the otc user put there (a FIFO would
# block the runner reading it, a directory would survive its removal and
# trigger it every 2 s) is removed as otc.
trigger() {
    local f=$STATE/$1
    [ -e "$f" ] || [ -L "$f" ] || return 1
    [ -f "$f" ] && [ ! -L "$f" ] && return 0
    log "WARNING: $f is not a plain file: removed"
    as_otc rm -rf -- "$f"
    return 1
}

# Settings > Bridge access: the same root runner a Pi uses. It rewrites
# /etc/otc_dev.ini; the change is kept in the volume, and the device
# restarted when there is one (its systemctl calls fail harmlessly here).
# The time limit covers a request swapped for a FIFO after trigger looked:
# nothing else is watched while the runner runs, and a stop signal waits
# for it.
run_bridge_runner() {
    local before
    before=$(sha256sum < "$LIVE_INI")
    log "bridge access: applying the device's request"
    timeout 60 /usr/local/bin/otc-bridge-runner 2>&1 | grep -v 'systemctl: command not found' | sed 's/^/[otc-bridge] /'
    [ "$(sha256sum < "$LIVE_INI")" != "$before" ] || return 1
    install -m 0640 -o root -g otc "$LIVE_INI" "$INI" || log "WARNING: could not keep the new bridge setting in $INI"
    return 0
}

# Settings > Restart device restarts the device's process; MariaDB and the
# container stay up. A request never becomes a loop: one from before this
# start is removed at start, and a new one waits until the device has run
# two minutes, rather than being dropped while the owner is told the device
# is restarting (the device's own check reads the server's uptime, which
# is rarely that short).
handle_restart_request() {
    local up=$(( $(date +%s) - APP_STARTED ))
    [ "$up" -ge 120 ] || return 0
    rm -f "$STATE/reboot.request"
    log "restarting the device, as its owner asked from Settings"
    pause 3
    stop_app
    start_app
}

nightly_dump() {
    local today
    [ "$(date +%H)" = 03 ] || return 0
    today=$(date +%F)
    [ "$LAST_DUMP" = "$today" ] && return 0
    LAST_DUMP=$today
    dump_databases || log "WARNING: the nightly database copy failed"
}

supervise() {
    local rc now t exits=() recent
    while :; do
        pause 2
        if ! alive "$DB_PID"; then
            wait "$DB_PID" 2>/dev/null
            DB_PID=
            tail -n 30 "$DATADIR/mariadb.err" >&2 2>/dev/null
            die "MariaDB stopped: stopping the device"
        fi
        if ! alive "$APP_PID"; then
            wait "$APP_PID"
            rc=$?
            log "the device stopped (exit $rc)"
            stop_app
            now=$(date +%s)
            recent=()
            for t in "${exits[@]}" "$now"; do
                [ $((now - t)) -lt 120 ] && recent+=("$t")
            done
            exits=("${recent[@]}")
            [ "${#exits[@]}" -lt 5 ] || die "the device stopped 5 times in 2 minutes: see its log in Settings > Logs, or $STATE/log/otc.log"
            pause 3
            start_app
            continue
        fi
        check_watch
        if trigger bridge.request && run_bridge_runner; then
            log "restarting the device with its new bridge setting"
            stop_app
            start_app
        fi
        trigger reboot.request && handle_restart_request
        nightly_dump
    done
}

# ---------------------------------------------------------------------------
# Database copies
# ---------------------------------------------------------------------------
# user_databases: the additional users' databases that exist. Their names
# come from a table the device writes, so only the shape it generates
# reaches root's command line (install.sh's rule).
user_databases() {
    local db
    for db in $(mysql otc -N -B -e 'SELECT db_name FROM users' 2>/dev/null); do
        [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]] || continue
        mysql -N -B -e "SHOW DATABASES LIKE '$db'" 2>/dev/null | grep -qxF -- "$db" && echo "$db"
    done
}

# newest_complete: the newest day whose copy holds every database (its
# .complete mark), for a restore and for the pruning to keep.
newest_complete() {
    local day
    for day in $(as_otc ls -1 "$DUMPS" 2>/dev/null | grep -E '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' | sort -r); do
        as_otc test -f "$DUMPS/$day/.complete" && { echo "$day"; return 0; }
    done
    return 1
}

# dump_databases: a consistent copy of every database into
# db-dumps/<day>/, written by otc (0600), keeping the last 7 days. Only a
# run that copied every database marks the day .complete.
dump_databases() {
    local day dir db old keep ok=0
    day=$(date +%F)
    dir=$DUMPS/$day
    as_otc mkdir -p -m 0700 "$DUMPS" "$dir" || return 1
    as_otc rm -f -- "$dir/.complete"
    for db in otc $(user_databases); do
        mariadb-dump --single-transaction --hex-blob --databases "$db" | gzip -c \
            | as_otc sh -c 'umask 077; cat > "$1"' sh "$dir/$db.sql.gz.part"
        if [ "${PIPESTATUS[0]}${PIPESTATUS[1]}${PIPESTATUS[2]}" = 000 ]; then
            as_otc mv -f -- "$dir/$db.sql.gz.part" "$dir/$db.sql.gz" || ok=1
        else
            as_otc rm -f -- "$dir/$db.sql.gz.part"
            log "WARNING: could not copy the database $db"
            ok=1
        fi
    done
    [ "$ok" = 0 ] && { as_otc touch -- "$dir/.complete" || ok=1; }
    # A week of failing nights must not leave nothing to restore: the
    # newest complete copy stays, however old.
    keep=$(newest_complete)
    for old in $(as_otc ls -1 "$DUMPS" | grep -E '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' | sort -r | tail -n +8); do
        [ "$old" = "$keep" ] || as_otc rm -rf -- "${DUMPS:?}/$old"
    done
    [ "$ok" = 0 ] && log "database copy written to $dir"
    return "$ok"
}

# restore_dir [day]: the folder of the night's copy to restore (the newest
# complete one by default), checked before anything is created.
restore_dir() {
    local day=${1:-}
    if [ -z "$day" ]; then
        day=$(newest_complete) \
            || die "no complete database copy found in $DUMPS (restore YYYY-MM-DD loads an incomplete one)"
    fi
    [[ "$day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "no database copy found in $DUMPS"
    as_otc test -f "$DUMPS/$day/otc.sql.gz" || die "no copy of the database otc in $DUMPS/$day"
    as_otc test -f "$DUMPS/$day/.complete" \
        || log "WARNING: the copy of $day is not complete: a user whose database it lacks will have none"
    RESTORE_DIR=$DUMPS/$day
}

# restore_databases: that copy into a database created for it. Every
# user's database account is made again from the users table, as
# provisioning made it.
restore_databases() {
    local dir=$RESTORE_DIR f db pass
    log "restoring the database copy in $dir"
    # The copies are written by otc: without --sandbox the client would run
    # a \! (shell) or source line in one as root.
    for f in otc $(as_otc ls -1 "$dir" | sed -n 's/^\(otc_[0-9a-f]\{32\}\)\.sql\.gz$/\1/p'); do
        as_otc gunzip -c -- "$dir/$f.sql.gz" | mysql --sandbox || die "could not load the copy of $f"
    done
    mysql otc -N -B -e 'SELECT db_name, db_pass FROM users' 2>/dev/null | while read -r db pass; do
        [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]] && [[ "$pass" =~ ^[0-9a-f]+$ ]] || continue
        as_otc test -f "$dir/$db.sql.gz" \
            || echo "[otc] WARNING: the copy has no database $db (a user's): that user's device won't start without it" >&2
        mysql <<SQL || echo "[otc] WARNING: could not restore the database account of $db" >&2
CREATE USER IF NOT EXISTS '$db'@'localhost' IDENTIFIED BY '$pass';
ALTER USER '$db'@'localhost' IDENTIFIED BY '$pass';
GRANT ALL PRIVILEGES ON \`$db\`.* TO '$db'@'localhost';
FLUSH PRIVILEGES;
SQL
    done
}

# ---------------------------------------------------------------------------
# Subcommands
# ---------------------------------------------------------------------------
on_stop() {
    log "stopping"
    stop_all
    exit 0
}

# prepare: everything up to a running, current database and config.
prepare() {
    preflight
    prepare_dirs
    check_downgrade
    start_mariadb
    load_identity
    apply_grants
}

finish_prepare() {
    migrate "${1:-}"
    insert_identity
    write_config
    install_release_files
    install_live_config
}

cmd_run() {
    trap on_stop TERM INT
    log "Off The Cloud release $RELEASE (beta container)"
    storage_mode_check
    mirror_prepare
    [ -z "$MIRROR_ARRAY" ] || start_watch
    preflight
    prepare_dirs
    storage_mode_record
    check_downgrade
    fetch_models
    start_mariadb
    load_identity
    apply_grants
    load_schema
    finish_prepare
    # A switch the device asked for just before the last stop still
    # happens, as a Pi's path unit would at boot; a restart request
    # doesn't.
    trigger reboot.request && rm -f "$STATE/reboot.request"
    trigger bridge.request && run_bridge_runner
    setup_password
    [ -d "$DUMPS/$(date +%F)" ] && LAST_DUMP=$(date +%F)
    if [ "${OTC_LOG_STDOUT:-0}" = 1 ]; then
        ( cd / && exec setpriv --reuid "$OTC_UID" --regid "$OTC_UID" --init-groups --no-new-privs \
            tail -n 0 -F "$STATE/log/otc.log" 2>/dev/null ) &
        TAIL_PID=$!
    fi
    start_app
    supervise
}

cmd_restore() {
    trap on_stop TERM INT
    storage_mode_check
    mirror_prepare --no-create
    [ -d "$DATADIR/otc" ] && die "$DATADIR already holds a database: a copy is restored into a storage folder without one (move $DATADIR aside first)"
    restore_dir "${1:-}"
    prepare
    storage_mode_record
    restore_databases
    finish_prepare always
    log "restored: start the device again (docker compose up -d)"
    stop_all
}

cmd_set_password() {
    mariadb-admin ping >/dev/null 2>&1 || die "the device is not running: start it, then run this with docker exec"
    vault_empty || { log "the device already has its owner password: change it in Settings"; exit 0; }
    init_password || die "could not set the owner password"
}

cmd_dump() {
    mariadb-admin ping >/dev/null 2>&1 || die "the device is not running: start it, then run this with docker exec"
    dump_databases || exit 1
}

read_ports
case "${1:-run}" in
    run) cmd_run ;;
    set-password) cmd_set_password ;;
    dump) cmd_dump ;;
    restore) cmd_restore "${2:-}" ;;
    *)
        echo "usage: otc-entrypoint [run | set-password | dump | restore [YYYY-MM-DD]]" >&2
        exit 2 ;;
esac
