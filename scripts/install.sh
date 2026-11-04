#!/usr/bin/env bash
# OTC one-line installer (issue #38) — turns a fresh Debian/Ubuntu-family
# machine into a running OTC device: RAID1 storage, MariaDB, a native Go
# toolchain, ONNX Runtime, the RAM++ tagging model, the database schema, app
# config, the RAID status-LED watcher, a systemd service, and the otc binary
# itself — built from source, live, on this machine, from the signed
# release verified-install.sh checked (Makefile.pi runs that same installer
# over SSH from another computer).
#
# Deliberately NOT installed here: network-setup.service (the first-boot
# WiFi/AP watcher, scripts/network_setup.py). It can take down the very
# network connection this script is most likely being run over - see the
# warning on Makefile.pi's `network-setup` target - so it stays an explicit,
# deliberate step rather than something a one-line installer does to you.
#
# Usage: log into any Debian/Ubuntu box (Raspberry Pi OS included) and run
#
#   curl -fsSL https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/verified-install.sh | sudo bash -s -- <subdomain>
#
# (issue #160: that installs the newest signed release, running this file
# from its verified source; run directly, this file hands over to it.)
#
# <subdomain> (optional) is this device's bridge subdomain, e.g. "pit" for
# pit.off-the.cloud — a name you and your friends' clients use to reach this
# device through the bridge relay. Omit it and the script prompts for one
# (only works if you saved the script locally first — see below, a piped
# `curl | bash` has no terminal left over for a prompt to read from).
#
# Disaster recovery ("the Pi died, I swapped it and kept the two data
# disks"): this script builds a RAID1 array on OTC_DISK1/OTC_DISK2 (default
# /dev/sda + /dev/sdb) and points MariaDB's datadir onto it — so re-running
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
#   OTC_DEMO=1                  a server for a public demo device (anyone
#                               may sign in with its published password;
#                               every change is refused, websocket/demo.go):
#                               implies OTC_SKIP_RAID=1, leaves out the RAID
#                               watcher, which acts on the server's own
#                               disks, and keeps the config's `demo` and
#                               `demo-password` lines across re-runs. Demo
#                               mode itself is turned on by hand once the
#                               content is loaded: docs/demo-device.md.
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
# The setup wizard shows the install's progress in its owner's language
# (docs/i18n.md, "Setup wizard"): a machine-readable line just before the
# English one it stands for, which stays exactly as it is for older wizards
# and the log - "@step <token> [<args>]" before a "[n/10]" line, "@error
# <token> <args>" before an ERROR line, args a JSON object of strings. The
# wizard shows wiz.install.step.<token> / wiz.install.error.<token> only
# when that key's English with these args is the English line that follows.
wiz_args() {
    local out="" v
    while [ "$#" -ge 2 ]; do
        v="${2//\\/\\\\}"
        v="${v//\"/\\\"}"
        v="$(printf '%s' "$v" | tr -d '\000-\037')"
        out="${out:+$out,}\"$1\":\"$v\""
        shift 2
    done
    printf '{%s}' "$out"
}
wiz_step() { local t="$1"; shift; if [ "$#" -ge 2 ]; then echo "[otc-install] @step $t $(wiz_args "$@")"; else echo "[otc-install] @step $t"; fi; }
wiz_error() { local t="$1"; shift; echo "[otc-install] @error $t $(wiz_args "$@")" >&2; }

# ---------------------------------------------------------------------------
# 0. Preflight
# ---------------------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "needs root — re-run as: curl ... | sudo bash -s -- [subdomain]"
command -v apt-get >/dev/null 2>&1 || die "only Debian/Ubuntu-family distros are supported (no apt-get found)"

# Issue #160: only a signed release is ever installed. This file is run as
# part of one (verified-install.sh checks the release's signature, source
# and web app and sets OTC_VERIFIED_SRC); run any other way - straight from
# the branch, as `curl | bash` and older setup wizards do - it hands over
# to verified-install.sh, which installs the newest signed release instead.
if [ -z "${OTC_VERIFIED_SRC:-}" ]; then
    log "Handing over to the verified installer: only a signed release is installed"
    hv="$(mktemp)"
    curl -fsSL --retry 3 --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$hv" "${OTC_REPO_RAW:-https://raw.githubusercontent.com/alonsovidales/otc/main}/scripts/verified-install.sh" \
        || { wiz_error download_installer; die "could not download the verified installer"; }
    exec bash "$hv" "$@"
fi

# fetch_pinned URL DEST SHA256 - downloads to a temporary file and moves it
# into place only if it matches the pinned hash (issue #160): nothing
# downloaded here is run or loaded unchecked.
# The download goes to a directory of its own beside DEST, made by mktemp
# (0700, root's), and is renamed from there: curl never writes through a
# name someone else could have put in place first. DEST's directory is
# root's own (the models' included, see step 6), and the rename stays on
# one filesystem, so DEST is either the old file or the whole new one.
fetch_pinned() {
    local url="$1" dest="$2" want="$3" dir tmpd
    dir="$(dirname -- "$dest")"
    # A run killed mid-download (the wizard's unit restarting, a power
    # cut) leaves its partial behind - up to ~870MB for the model: the
    # leftovers of this function's earlier runs go first, and so do the
    # fixed-name and mktemp-style partials of earlier releases.
    rm -rf -- "$dir"/.otc-fetch.??????
    rm -f -- "$dest.part" "$dest".??????
    tmpd="$(mktemp -d "$dir/.otc-fetch.XXXXXX")" || { wiz_error download url "$url"; die "could not make a download directory in $dir"; }
    # Connect and stall timeouts: a transfer that stopped mid-way hung the
    # install for good. Under 1 byte/s for two minutes has truly stopped
    # (exit 28, retried); a slow but live link is never cut off.
    curl -fL --retry 5 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 \
        -o "$tmpd/download" "$url" || { rm -rf "$tmpd"; wiz_error download url "$url"; die "could not download $url"; }
    if [ "$(sha256sum "$tmpd/download" | awk '{print $1}')" != "$want" ]; then
        rm -rf "$tmpd"; wiz_error pinned_hash url "$url"; die "$url does not match its pinned hash"
    fi
    mv -Tf "$tmpd/download" "$dest"
    rmdir "$tmpd" 2>/dev/null || rm -rf "$tmpd"
}

# fetch_optional URL DEST SHA256 is fetch_pinned for a file the device can
# do without (On this day's quality model): a download that fails, or
# doesn't match its pinned hash, is a warning and leaves nothing behind.
fetch_optional() {
    local url="$1" dest="$2" want="$3" dir tmpd
    dir="$(dirname -- "$dest")"
    tmpd="$(mktemp -d "$dir/.otc-fetch.XXXXXX")" || { log "warning: could not make a download directory in $dir"; return 0; }
    if ! curl -fL --retry 3 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 \
        -o "$tmpd/download" "$url"; then
        rm -rf "$tmpd"
        log "warning: could not download $url - it is tried again by the next update"
        return 0
    fi
    if [ "$(sha256sum "$tmpd/download" | awk '{print $1}')" != "$want" ]; then
        rm -rf "$tmpd"
        log "warning: $url does not match its pinned hash - left out"
        return 0
    fi
    mv -Tf "$tmpd/download" "$dest"
    rmdir "$tmpd" 2>/dev/null || rm -rf "$tmpd"
}

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
# (/etc/otc itself exists on a fresh image: it carries the release key.)
if { [ -d /opt/otc-src ] || [ -e /usr/bin/otc ] || [ -e /etc/otc/otc-install.env ]; } && [ ! -e /etc/otc/.install-complete ]; then
    echo "[otc-install] NOTE: this machine already has OTC artifacts but no /etc/otc/.install-complete marker,"
    echo "[otc-install]       which means a previous install run did not finish. Re-running now to complete/repair it"
    echo "[otc-install]       (this is safe — every step is idempotent). If you're SURE you want a from-scratch"
    echo "[otc-install]       install instead, stop and wipe the box first."
    echo ""
fi

# Pinned by hash with their versions (issue #160): Go's from go.dev's own
# list, the rest checked once and fixed here.
case "$(uname -m)" in
    aarch64) ARCH=arm64; ORT_ARCH=aarch64; PROTOC_ARCH=aarch_64
             GO_SHA=a290581cfe4fe28ddd737dde3095f3dbeb7f2e4065cab4eae44dfc53b760c2f7
             ORT_SHA=15100fb88b4c692cdd6bf2cca5f4a26a3806cebca8136de6681e2aba4b2ea033
             PROTOC_SHA=6427349140e01f06e049e707a58709a4f221ae73ab9a0425bc4a00c8d0e1ab32 ;;
    x86_64)  ARCH=amd64; ORT_ARCH=x64; PROTOC_ARCH=x86_64
             GO_SHA=031f088e5d955bab8657ede27ad4e3bc5b7c1ba281f05f245bcc304f327c987a
             ORT_SHA=4c436a280d650f8bf32c921a2bf4de7c42cc32884c51c90e47de991708bbb5a4
             PROTOC_SHA=3e866620c5be27664f3d2fa2d656b5f3e09b5152b42f1bedbf427b333e90021a ;;
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
# default (settings.face_recognition_enabled, issue #178: the owner turns it
# on in the setup or in Settings), but both models are small enough (~230KB
# + ~10MB) to just always fetch here rather than making that a second,
# deferred download the first time someone enables the feature.
# Official OpenCV Zoo models (MIT/Apache-2.0), designed as a matched pair -
# see face_recognition/face_recognition.go's package doc comment.
FACE_DETECTOR_ONNX=$MODEL_DIR/face_detection_yunet_2023mar.onnx
FACE_RECOGNIZER_ONNX=$MODEL_DIR/face_recognition_sface_2021dec_int8.onnx
OPENCV_ZOO_RAW=https://github.com/opencv/opencv_zoo/raw/main/models
# On this day (Memories): NIMA, Google's Neural Image Assessment - idealo's
# MobileNet trained on the AVA ratings (Apache-2.0), exported to ONNX
# (~13MB) - scores how each photo looks. Optional: without it photos are
# measured by sharpness and exposure alone, so a download that fails is
# only a warning (fetch_optional), tried again by the next update.
QUALITY_ONNX=$MODEL_DIR/nima_mobilenet_aesthetic.onnx
QUALITY_ONNX_URL=https://github.com/alonsovidales/otc/releases/download/models/nima_mobilenet_aesthetic.onnx
QUALITY_ONNX_SHA=b35a4d6616b5597fb84d57cbcfbc7d5f3d80b694069d1182b66677903d0b71d1
# Smart search (beta): SigLIP 2 base (Google, Apache-2.0), ~415MB, isn't
# fetched here. The device downloads it itself, in the background at a
# capped speed, only once someone turns Smart search on (release 152,
# files_manager/smart_model.go): into the service's own state directory
# (/var/lib/otc/models/siglip2-base), unless the files are already where the
# config's [embedder] section says - EMBED_DIR, where release 151 put them.
EMBED_DIR=$MODEL_DIR/siglip2-base
ENVIRONMENT=dev
INI_FILE="/etc/otc_${ENVIRONMENT}.ini"
# otc_ini KEY: the [otc] value of KEY in the config an earlier run wrote,
# and whether the line is there at all (an empty value is a value: a
# device whose bridge was switched off, #182).
otc_ini() {
    [ -f "$INI_FILE" ] || return 1
    awk -F= -v key="$1" '
        /^[[:space:]]*\[/ { section = $0; gsub(/[[:space:]]/, "", section); next }
        section == "[otc]" && !found {
            k = $1; gsub(/[[:space:]]/, "", k)
            if (k == key) { v = substr($0, index($0, "=") + 1); sub(/^[[:space:]]+/, "", v); sub(/[[:space:]]+$/, "", v); print v; found = 1 }
        }
        END { exit !found }' "$INI_FILE"
}
# A bridge name kept from that config, only in the shape install.sh and
# the bridge runner write: it reaches the settings row's SQL below.
kept_bridge() {
    local v
    v="$(otc_ini "$1")" || return 1
    [[ "$v" =~ ^([a-z0-9]([a-z0-9.-]*[a-z0-9])?)?$ ]] || return 1
    printf '%s' "$v"
}
# Issue #124: OTC_BRIDGE_ADDR="" installs a local-only device - no bridge,
# no domain: reachable on the home network only (the setup wizard's
# "continue without an account"). Any other value is the bridge to use.
# Not given, a re-run keeps what the device has: the bridge it is on, or
# none if its owner switched the bridge off since (#182) - a re-run used
# to put it back on off-the.cloud.
if [ -z "${OTC_BRIDGE_ADDR+set}" ] && BRIDGE_ADDR="$(kept_bridge bridge-addr)"; then
    :
else
    BRIDGE_ADDR="${OTC_BRIDGE_ADDR-off-the.cloud}"
fi
# Issue #145: the bridge a local-only device joins if its owner switches
# the bridge on later, from Settings. Kept the same way.
if [ -z "${OTC_BRIDGE_DEFAULT:-}" ] && BRIDGE_DEFAULT="$(kept_bridge bridge-default)" && [ -n "$BRIDGE_DEFAULT" ]; then
    :
else
    BRIDGE_DEFAULT="${OTC_BRIDGE_DEFAULT:-${BRIDGE_ADDR:-off-the.cloud}}"
fi
STORAGE_PATH=/mnt/storage/
UNENC_PATH=/mnt/storage/unencrypted/
HTTP_PORT=8080
ENV_FILE=/etc/otc/otc-install.env

# ---- RAID1 storage (see the "Disaster recovery" note up top) --------------
DISK1="${OTC_DISK1:-/dev/sda}"
DISK2="${OTC_DISK2:-/dev/sdb}"
RAID_DEV=/dev/md0
MOUNT_POINT=/mnt/storage
DEMO="${OTC_DEMO:-0}"
[ "$DEMO" = "1" ] || DEMO=0
SKIP_RAID="${OTC_SKIP_RAID:-$DEMO}"
# Set by the setup wizard's "Recover": an existing array must be found and
# used; building a fresh one (which wipes the disks) is refused outright.
RECOVERY="${OTC_RECOVERY:-0}"

SUBDOMAIN="${1:-}"
if [ -z "$SUBDOMAIN" ] && [ -n "$BRIDGE_ADDR" ]; then
    read -rp "Choose a subdomain for this device (e.g. 'pit' for pit.$BRIDGE_ADDR): " SUBDOMAIN
fi
if [ -n "$BRIDGE_ADDR" ]; then
    [[ "$SUBDOMAIN" =~ ^[a-z0-9-]+$ ]] || die "subdomain must be lowercase letters/digits/hyphens only"
else
    # Local-only: the settings row still wants a name; "otc" is what the
    # device answers to on the home network (otc.local).
    SUBDOMAIN="${SUBDOMAIN:-otc}"
fi

# ---------------------------------------------------------------------------
# 1. OS packages
# ---------------------------------------------------------------------------
wiz_step packages
log "[1/10] apt-get update + base packages"
export DEBIAN_FRONTEND=noninteractive

# A Raspberry Pi has no battery-backed clock: until network time syncs it
# runs on whatever date it last saved, and apt then rejects every
# repository as "not live until <date>" (signed in its future) and the
# install dies here with code 100. Give NTP a minute, then fall back to
# the Date header of an HTTPS server whose certificate still verifies
# (a clock a day or two off doesn't break TLS). The clock only ever moves
# forward this way, so nobody on the path can push it into the past to
# replay old, validly signed package lists.
ensure_clock() {
    timedatectl set-ntp true 2>/dev/null || true
    for _ in $(seq 1 30); do
        [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null)" = "yes" ] && return 0
        sleep 2
    done
    local now url stamp remote
    now=$(date -u +%s)
    for url in https://deb.debian.org/ https://archive.raspberrypi.com/ https://github.com/; do
        stamp=$(curl -fsSI --max-time 10 "$url" 2>/dev/null | tr -d '\r' | awk 'tolower($1)=="date:"{sub(/^[^:]*: */,"");print;exit}')
        [ -n "$stamp" ] || continue
        remote=$(date -u -d "$stamp" +%s 2>/dev/null) || continue
        if [ "$remote" -gt "$now" ]; then
            date -u -s "@$remote" >/dev/null && log "clock was behind - set from $url to $(date -u)"
        fi
        return 0
    done
    log "WARNING: could not confirm the clock (now $(date -u)); apt may reject the repositories"
}
ensure_clock
apt-get update
# libopencv-dev + pkg-config (issue #52): face recognition builds against
# gocv, which needs OpenCV's real headers/libs at compile time, found via
# pkg-config - not just a runtime .so like ONNX Runtime below. mdadm: RAID1
# storage. unzip: extracting the protoc release archive below.
apt-get install -y mariadb-server build-essential git curl wget rsync ca-certificates ffmpeg libopencv-dev pkg-config mdadm unzip python3

# gocv (v0.40.0, go.mod) builds against OpenCV 4.10 or newer, and not 5.
# Debian 13 and Raspberry Pi OS ship 4.10, but Debian 12 and Ubuntu 24.04
# (Linux Mint 22 with it) ship 4.6, and the build failed on gocv's C++
# ("'ArucoDictionary' does not name a type", "'DataLayout' is not a member
# of 'cv::dnn'"). There the OpenCV a Raspberry Pi has is built from source
# into /usr/local, once: pkg-config finds it before the system's, and
# updates build against it too. Only the modules gocv compiles, and nothing
# fetched while it builds (ippicv and KleidiCV are downloads, left out).
OPENCV_MIN=4.10
OPENCV_SRC_VERSION=4.10.0
OPENCV_SRC_SHA=b2171af5be6b26f7a06b1229948bbb2bdaa74fcf5cd097e0af6378fce50a6eb9
opencv_ok() {
    local v
    v=$(pkg-config --modversion opencv4 2>/dev/null || true)
    [ -n "$v" ] && dpkg --compare-versions "$v" ge "$OPENCV_MIN" && dpkg --compare-versions "$v" lt 5
}
if ! opencv_ok; then
    wiz_step opencv version "$OPENCV_SRC_VERSION"
    log "[1/10] OpenCV $OPENCV_SRC_VERSION, built from source because the system's is too old (5 to 30 minutes)"
    log "this system's OpenCV: $(pkg-config --modversion opencv4 2>/dev/null || echo none), face recognition needs $OPENCV_MIN or newer"
    apt-get install -y cmake libjpeg-dev libpng-dev
    # The build happens in a directory mktemp makes (0700, root's): /var/tmp
    # is everyone's, and another account could make a fixed name there
    # first - then swap the sources after their hash was checked, and root
    # would build and install its code into /usr/local. Earlier runs'
    # directories (and the log of the fixed name before) go first, root's
    # only.
    find /var/tmp -maxdepth 1 -user root \( \( -type d \( -name otc-opencv -o -name 'otc-opencv.??????' \) \) \
        -o \( -type f -name otc-opencv-build.log \) \) -exec rm -rf -- {} + 2>/dev/null || true
    OPENCV_BUILD_DIR="$(mktemp -d /var/tmp/otc-opencv.XXXXXX)" || die "could not make a directory in /var/tmp to build OpenCV in"
    if [ "$(df -Pk "$OPENCV_BUILD_DIR" | awk 'NR == 2 {print $4}')" -lt $((2 * 1024 * 1024)) ]; then
        wiz_error opencv_space path "$OPENCV_BUILD_DIR"
        die "not enough free space in $OPENCV_BUILD_DIR to build OpenCV: it needs 2 GB"
    fi
    fetch_pinned "https://github.com/opencv/opencv/archive/refs/tags/$OPENCV_SRC_VERSION.tar.gz" "$OPENCV_BUILD_DIR/src.tar.gz" "$OPENCV_SRC_SHA"
    # A compiler per core, but one per GB of free memory at most: dnn's
    # sources are large enough to run a small machine out of memory.
    ocv_jobs=$(nproc)
    ocv_mem=$(awk '/^MemAvailable:/ {print int($2 / 1048576)}' /proc/meminfo)
    [ "$ocv_mem" -lt "$ocv_jobs" ] && ocv_jobs=$ocv_mem
    [ "$ocv_jobs" -ge 1 ] || ocv_jobs=1
    ocv_log="$OPENCV_BUILD_DIR/build.log"
    log "building OpenCV with $ocv_jobs jobs (full output in $ocv_log)"
    # The build's own output goes to the log; its progress shows here
    # every 10%, so a long build doesn't look stuck.
    if ! {
        tar -xzf "$OPENCV_BUILD_DIR/src.tar.gz" -C "$OPENCV_BUILD_DIR" >"$ocv_log" 2>&1 \
        && cmake -S "$OPENCV_BUILD_DIR/opencv-$OPENCV_SRC_VERSION" -B "$OPENCV_BUILD_DIR/build" \
            -D CMAKE_BUILD_TYPE=Release -D CMAKE_INSTALL_PREFIX=/usr/local \
            -D BUILD_LIST=core,imgproc,imgcodecs,videoio,highgui,objdetect,dnn,features2d,calib3d,photo,video \
            -D OPENCV_GENERATE_PKGCONFIG=ON -D BUILD_SHARED_LIBS=ON \
            -D BUILD_TESTS=OFF -D BUILD_PERF_TESTS=OFF -D BUILD_EXAMPLES=OFF -D BUILD_DOCS=OFF -D BUILD_opencv_apps=OFF \
            -D WITH_IPP=OFF -D WITH_KLEIDICV=OFF -D WITH_ADE=OFF -D WITH_GTK=OFF -D WITH_QT=OFF \
            -D WITH_FFMPEG=OFF -D WITH_GSTREAMER=OFF -D WITH_V4L=OFF -D WITH_1394=OFF -D WITH_VTK=OFF \
            -D WITH_TIFF=OFF -D WITH_WEBP=OFF -D WITH_OPENJPEG=OFF -D WITH_JASPER=OFF -D WITH_OPENEXR=OFF \
            -D WITH_AVIF=OFF -D WITH_GDAL=OFF -D WITH_GDCM=OFF >>"$ocv_log" 2>&1 \
        && cmake --build "$OPENCV_BUILD_DIR/build" -j "$ocv_jobs" 2>&1 | tee -a "$ocv_log" \
            | awk '/^\[ *[0-9]+%\]/ { n = $0; sub(/^\[ */, "", n); n = int(n / 10) * 10; if (n > shown) { shown = n; print "[otc-install] building OpenCV: " n "%"; fflush() } }' \
        && cmake --install "$OPENCV_BUILD_DIR/build" >>"$ocv_log" 2>&1
    }; then
        tail -n 30 "$ocv_log" >&2 || true
        wiz_error opencv_build version "$OPENCV_SRC_VERSION" path "$ocv_log"
        die "could not build OpenCV $OPENCV_SRC_VERSION - see $ocv_log"
    fi
    ldconfig
    # The sources and the build tree go now (about 2 GB); the log, in the
    # same directory, only once pkg-config finds what was installed - the
    # failure below points at it.
    rm -rf "$OPENCV_BUILD_DIR/src.tar.gz" "$OPENCV_BUILD_DIR/opencv-$OPENCV_SRC_VERSION" "$OPENCV_BUILD_DIR/build"
    if ! opencv_ok; then
        log "OpenCV built and installed, but pkg-config finds $(pkg-config --modversion opencv4 2>/dev/null || echo none), not $OPENCV_MIN or newer"
        wiz_error opencv_build version "$OPENCV_SRC_VERSION" path "$ocv_log"
        die "could not build OpenCV $OPENCV_SRC_VERSION - see $ocv_log"
    fi
    rm -rf "$OPENCV_BUILD_DIR"
    log "OpenCV $(pkg-config --modversion opencv4) installed in /usr/local"
fi

# Issue #80: Tailscale, so the setup wizard can offer Funnel as an
# alternative to the bridge. Installed, not enabled - nothing joins a
# tailnet or is published publicly unless the owner asks for it during
# setup. Non-fatal: a device without it simply doesn't get the option.
if ! command -v tailscale >/dev/null 2>&1; then
    # From Tailscale's apt repository, its signing key pinned by hash
    # (issue #160) - not their install script piped into a root shell.
    ts_dist=""; ts_code=""
    if [ -r /etc/os-release ]; then
        . /etc/os-release
        # A distro built on Ubuntu or Debian (Linux Mint, Pop!_OS) uses
        # that one's repository, under that one's release name: Mint 22
        # is "wilma", the Ubuntu it is built on "noble".
        case " ${ID:-} ${ID_LIKE:-} " in
            *" ubuntu "*) ts_dist=ubuntu; ts_code="${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}" ;;
            *" debian "*|*" raspbian "*) ts_dist=debian; ts_code="${DEBIAN_CODENAME:-${VERSION_CODENAME:-}}" ;;
        esac
    fi
    ts_key=/usr/share/keyrings/tailscale-archive-keyring.gpg
    if [ -n "$ts_dist" ] && [ -n "$ts_code" ] \
        && curl -fsSL -o "$ts_key.tmp" "https://pkgs.tailscale.com/stable/$ts_dist/$ts_code.noarmor.gpg" \
        && [ "$(sha256sum "$ts_key.tmp" | awk '{print $1}')" = 3e03dacf222698c60b8e2f990b809ca1b3e104de127767864284e6c228f1fb39 ]; then
        mv -f "$ts_key.tmp" "$ts_key"
        echo "deb [signed-by=$ts_key] https://pkgs.tailscale.com/stable/$ts_dist $ts_code main" > /etc/apt/sources.list.d/tailscale.list
        { apt-get update -qq && apt-get install -y tailscale; } \
            || echo "[otc-install] WARNING: Tailscale could not be installed - the Funnel option won't be offered"
    else
        rm -f "$ts_key.tmp"
        echo "[otc-install] WARNING: Tailscale's repository key could not be checked - the Funnel option won't be offered"
    fi
fi
# The package starts tailscaled at boot; it runs only once the owner turns
# Funnel on in Settings (otc-tailscale-runner starts it then - otc.service
# can't, it runs with NoNewPrivileges). Only root drives it (issue #204):
# the runner does everything Funnel needs, and a re-install of a device an
# older release made otc the operator of takes that away first. A device
# already serving through Funnel (a re-install) keeps it.
if command -v tailscale >/dev/null 2>&1; then
    bash "$OTC_VERIFIED_SRC/scripts/tailscale-runner/otc-tailscale-runner.sh" --drop-operator \
        || log "WARNING: tailscaled's operator could not be taken away from the otc user - the runner tries again when Funnel is turned on"
    if ! tailscale funnel status 2>/dev/null | grep -q ':8080'; then
        systemctl disable --now tailscaled >/dev/null 2>&1 || true
    fi
fi

# Issue #156: swap stays in RAM. The service's memory holds decrypted
# files and keys, and a page swapped to the SD card could keep them there:
# rpi-swap (Raspberry Pi OS 13) is set to zram only - its default writes
# idle pages to /var/swap - and dphys-swapfile (older images) is removed.
if [ -x /usr/lib/systemd/system-generators/rpi-swap-generator ] || [ -f /etc/rpi/swap.conf ]; then
    mkdir -p /etc/rpi/swap.conf.d
    printf '[Main]\nMechanism=zram\n' > /etc/rpi/swap.conf.d/90-otc-ram-only.conf
    systemctl stop rpi-zram-writeback.timer >/dev/null 2>&1 || true
fi
if command -v dphys-swapfile >/dev/null 2>&1; then
    dphys-swapfile swapoff >/dev/null 2>&1 || true
    dphys-swapfile uninstall >/dev/null 2>&1 || true
    systemctl disable dphys-swapfile >/dev/null 2>&1 || true
fi

# Issue #157 (release 59): SSH never accepts passwords on a device made
# from the image - its console account (otc-debug) has a published one. A
# fresh install is recorded as the latest release, so release scripts don't
# run on it: this has to be here too, not only in the image or in
# updates/59.sh. A machine of the owner's own (a Pi they set up, a laptop, a
# VPS) has no such account, and its SSH stays as they set it: turning off
# passwords and root there would lock out whoever logs in that way.
if id otc-debug >/dev/null 2>&1; then
    mkdir -p /etc/ssh/sshd_config.d
    cat > /etc/ssh/sshd_config.d/01-otc-keys-only.conf <<'SSHEOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
SSHEOF
    chmod 644 /etc/ssh/sshd_config.d/01-otc-keys-only.conf
    if systemctl is-active --quiet ssh 2>/dev/null && sshd -t 2>/dev/null; then systemctl reload ssh; fi
fi

# CVE-2026-98432 (release 161; owner, 2026-11-01: security updates and a
# night restart): until then a device never updated its system - the
# kernel and packages stayed as they were the day it was set up. Now the
# distribution's security updates install by themselves every day
# (unattended-upgrades: Debian's and Ubuntu's security archives, from the
# distribution's own 50unattended-upgrades, plus the Raspberry Pi's kernel
# and firmware, from the main component of its own archive), and when one
# needs a restart - a new kernel - the device restarts at 04:00
# (OTC_SECURITY_REBOOT=0 on a machine that shouldn't; a choice already
# made stays). apt waits for another apt instead of failing. And TCP
# packets carrying IPv4 options are dropped before the kernel's TCP code
# sees them: that bug needs them, and nothing OTC serves uses them (an
# nftables table of its own, otc-ipv4-options.service). The same as
# updates/corrected/161.sh, for the devices installed before
# (updater/os_security_test.go).
#
# Nothing in it waits for an answer, and it sets up all it can before it
# reports a failure (status 1): an update has no terminal, so debconf asks
# nothing, dpkg keeps a configuration file someone changed (confold) and
# takes the package's default for the rest (confdef) instead of asking on
# standard input and failing, an apt run cut short (a power cut) is
# finished first, apt waits up to 10 minutes for another apt (the daily
# one), and a failed install is tried once more with fresh lists. The
# IPv4-options filter needs no package where nft is there already.
otc_os_security() {
    local rc=0 conf=/etc/apt/apt.conf.d/52otc-security-updates reboot=true
    local apt_get=(apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600)
    export DEBIAN_FRONTEND=noninteractive
    dpkg --force-confdef --force-confold --configure -a || true
    # A device that hasn't asked in months has stale lists: ask first (an
    # offline device installs from what it has, or fails here).
    "${apt_get[@]}" update || true
    if ! "${apt_get[@]}" install unattended-upgrades nftables; then
        dpkg --force-confdef --force-confold --configure -a || true
        "${apt_get[@]}" update || true
        "${apt_get[@]}" install unattended-upgrades nftables \
            || { echo "WARNING: unattended-upgrades and nftables could not be installed"; rc=1; }
    fi
    cat > /etc/apt/apt.conf.d/20auto-upgrades <<'APTEOF' || rc=1
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APTEOF
    if [ "${OTC_SECURITY_REBOOT:-1}" = "0" ] || grep -q 'Automatic-Reboot "false"' "$conf" 2>/dev/null; then
        reboot=false
    fi
    cat > "$conf" <<APTEOF || rc=1
// Off The Cloud (scripts/install.sh, updates/161.sh): the system's security
// updates install by themselves every day, and when one needs a restart (a
// new kernel) the device restarts at 04:00. Debian's and Ubuntu's security
// archives come from the distribution's own 50unattended-upgrades; the
// Raspberry Pi's kernel and firmware from its own archive.
Unattended-Upgrade::Origins-Pattern {
        "origin=Raspberry Pi Foundation,codename=\${distro_codename},component=main";
};
Unattended-Upgrade::Automatic-Reboot "$reboot";
Unattended-Upgrade::Automatic-Reboot-WithUsers "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
DPkg::Lock::Timeout "600";
APTEOF
    mkdir -p /etc/otc || rc=1
    cat > /etc/otc/ipv4-options.nft <<'NFTEOF' || rc=1
#!/usr/sbin/nft -f
# Off The Cloud: TCP packets carrying IPv4 options never reach the kernel's
# TCP code (CVE-2026-98432 needs them; nothing the device serves uses them).
# Only this table is replaced.
table ip otc_ipv4_options
delete table ip otc_ipv4_options
table ip otc_ipv4_options {
	chain input {
		type filter hook input priority -10; policy accept;
		meta l4proto tcp ip hdrlength != 5 drop
	}
}
NFTEOF
    cat > /etc/systemd/system/otc-ipv4-options.service <<'UNITEOF' || rc=1
[Unit]
Description=Off The Cloud: drop TCP packets carrying IPv4 options (CVE-2026-98432)
DefaultDependencies=no
After=nftables.service
Before=network-pre.target shutdown.target
Wants=network-pre.target
Conflicts=shutdown.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/otc/ipv4-options.nft

[Install]
WantedBy=sysinit.target
UNITEOF
    systemctl daemon-reload || rc=1
    if [ -x /usr/sbin/nft ]; then
        { systemctl enable otc-ipv4-options.service && systemctl restart otc-ipv4-options.service; } \
            || { echo "WARNING: the IPv4-options filter could not be loaded"; rc=1; }
    else
        echo "WARNING: nft is not installed, so the IPv4-options filter can't be loaded yet"
        rc=1
    fi
    return "$rc"
}
log "[1/10] security updates and the IPv4-options filter"
otc_os_security || die "the system's security updates could not be set up (see the lines above) - run the installer again"

wiz_step account
log "[2/10] otc service account"
id otc >/dev/null 2>&1 || useradd -r -m -d /home/otc -s /usr/sbin/nologin otc
for g in dialout video plugdev gpio i2c spi; do
    getent group "$g" >/dev/null 2>&1 && usermod -aG "$g" otc || true
done

# ---------------------------------------------------------------------------
# 2. RAID1 storage
# ---------------------------------------------------------------------------

# The two disks must be two real disks that nothing else uses. The defaults
# are a Pi's two USB disks; on most x86 machines /dev/sda is the system disk
# itself. In use means anything on the disk is mounted - a partition, a
# logical volume (Ubuntu's root), a swap - but not an array built on it: a
# re-imaged device's members are exactly what recovery assembles.
# A recovery may have one disk: the wizard's Recover on a mirror that lost
# a disk names the one left twice (OTC_DISK1 = OTC_DISK2). That mirror is
# assembled on its own and runs degraded until a second disk is added; a
# fresh array is never built in that mode (below), so one disk is fine.
check_storage_disks() {
    local d where
    for d in "$DISK1" "$DISK2"; do
        if [ ! -b "$d" ]; then
            wiz_error disk_not_found disk "$d"
            die "$d is not a disk on this machine - set OTC_DISK1 and OTC_DISK2 to the two data disks (lsblk lists them)"
        fi
    done
    if [ "$(readlink -f "$DISK1")" = "$(readlink -f "$DISK2")" ] && [ "$RECOVERY" != "1" ]; then
        wiz_error disk_twice disk "$DISK1"
        die "OTC_DISK1 and OTC_DISK2 are both $DISK1 - the mirror needs two different disks"
    fi
    for d in "$DISK1" "$DISK2"; do
        # awk reads it all (no exit): under pipefail, lsblk on a broken
        # pipe would end the script here without a word.
        where=$(lsblk -nrpo TYPE,MOUNTPOINT "$d" 2>/dev/null | awk '$1 !~ /^raid/ && $2 != "" && !found {print $2; found = 1}' || true)
        if [ -n "$where" ]; then
            wiz_error disk_in_use disk "$d" where "$where"
            die "$d is in use (mounted at $where) - refusing to make it a storage disk. Set OTC_DISK1 and OTC_DISK2 to the two data disks (lsblk lists them)"
        fi
    done
}

# The storage's fstab line. nofail: a disk missing or slow at boot no
# longer stops the boot in emergency mode, with no network and no SSH;
# the device comes up without its storage (the service and MariaDB wait
# for it, below) - and up to 3 minutes are given for the mirror to appear
# (USB disks, md's own wait for a missing member). nofail also stops
# ordering the mount before local-fs.target, so what needs the storage
# says so itself: the RequiresMountsFor drop-ins after this step, without
# which MariaDB and the service could start on the empty directory under
# the mount point. storage_fstab_line add|keep: the md0 line gets the two
# options (any others it has are kept); with add, a device that has none
# - and nothing else mounted there - gets one.
STORAGE_MOUNT_OPTS="defaults,nofail,x-systemd.device-timeout=3min"
storage_fstab_line() {
    local tmp
    tmp="$(mktemp /etc/fstab.otc.XXXXXX)"
    awk -v dev="$RAID_DEV" -v mnt="$MOUNT_POINT" -v opts="$STORAGE_MOUNT_OPTS" -v add="$1" '
        /^[[:space:]]*#/ || NF < 4 { print; next }
        $1 == dev || $2 == mnt { taken = 1 }
        $1 == dev && $2 == mnt {
            o = $4
            if (o !~ /(^|,)nofail(,|$)/) o = o ",nofail"
            if (o !~ /(^|,)x-systemd\.device-timeout=/) o = o ",x-systemd.device-timeout=3min"
            if (o != $4) { $4 = o }
        }
        { print }
        END { if (!taken && add == "add") print dev "   " mnt "   ext4   " opts "   0   0" }' /etc/fstab > "$tmp" \
        || { rm -f "$tmp"; die "could not update /etc/fstab"; }
    if cmp -s "$tmp" /etc/fstab; then
        rm -f "$tmp"
        return 0
    fi
    chmod 644 "$tmp"
    sync "$tmp" 2>/dev/null || sync
    mv -Tf "$tmp" /etc/fstab
    systemctl daemon-reload
}

wiz_step storage path "$MOUNT_POINT"
log "[3/10] RAID1 storage ($MOUNT_POINT)"
mkdir -p "$MOUNT_POINT"
if [ "$SKIP_RAID" = "1" ]; then
    log "OTC_SKIP_RAID=1: using $MOUNT_POINT as a plain directory on this filesystem (no RAID)."
elif mountpoint -q "$MOUNT_POINT"; then
    log "$MOUNT_POINT already mounted, skipping."
    storage_fstab_line keep
else
    check_storage_disks
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
    if [ ! -e "$RAID_DEV" ]; then
        # Assembled without an mdadm.conf (a fresh image, or the setup
        # wizard's own recovery check), the array comes up as /dev/md127 or
        # similar, not md0. Take over any active array built on these disks
        # and bring it back as $RAID_DEV, which fstab, raid-watch and the
        # rest of the system name.
        for md in $(awk '/^md[0-9]+ *: *active/{print $1}' /proc/mdstat); do
            members=$(awk -v md="$md" '$1==md{for(i=3;i<=NF;i++)if($i~/\[[0-9]+\]/){sub(/\[[0-9]+\].*/,"",$i);print "/dev/"$i}}' /proc/mdstat)
            case " $(echo $members) " in
                *" $DISK1"*|*" $DISK2"*) ;;
                *) continue ;;
            esac
            log "Found the existing array as /dev/$md ($(echo $members)) - reassembling it as $RAID_DEV."
            umount "/dev/$md" 2>/dev/null || true
            mdadm --stop "/dev/$md"
            # --run: it was running, so it runs again - with the disks it
            # had. Without it a mirror down to one disk (a recovery from
            # the disk that is left) is only gathered, inactive, and the
            # mount below fails.
            # shellcheck disable=SC2086
            mdadm --assemble --run "$RAID_DEV" $members
            break
        done
    fi
    if [ -e "$RAID_DEV" ]; then
        log "$RAID_DEV exists (existing array assembled) - skipping wipe."
        mountpoint -q "$MOUNT_POINT" || mount "$RAID_DEV" "$MOUNT_POINT" 2>/dev/null || true
    else
        [ "$RECOVERY" = "1" ] && { wiz_error recovery_no_array disk1 "$DISK1" disk2 "$DISK2"; die "recovery was asked for, but no existing RAID1 array could be assembled from $DISK1/$DISK2 - nothing was wiped. Check both disks are connected and try again."; }
        # Last line of defence: a disk that still carries a RAID superblock
        # belongs to an array, and wiping it is never done implicitly.
        # mdadm --examine also succeeds on a plain MBR partition table (it
        # prints the partitions), which every PC formatter and the Pi
        # Imager's Erase leave behind: only an md superblock, which always
        # prints its Raid Level, counts. Not grep -q: under pipefail, grep
        # leaving early could fail mdadm on a broken pipe and let a real
        # member through.
        for d in "$DISK1" "$DISK2"; do
            if mdadm --examine "$d" 2>/dev/null | grep "Raid Level" >/dev/null; then
                wiz_error array_not_assembled disk "$d"
                die "$d still holds a RAID array that could not be assembled - refusing to wipe it. To really start over, run: mdadm --zero-superblock $d"
            fi
        done
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
    storage_fstab_line add
    mountpoint -q "$MOUNT_POINT" || mount "$RAID_DEV" "$MOUNT_POINT"
fi
# The storage's unencrypted directory is made in step 9, by the otc user
# itself (otc_dir).
# The service and MariaDB (its datadir is on the mirror, step 10) start
# only once the storage is mounted, and stop if it goes: never on the
# empty directory under the mount point, where new files would be written
# to the SD card and hidden once the disks are back. Where nothing is
# mounted there (OTC_SKIP_RAID) this asks for nothing but the root
# filesystem. Before MariaDB is (re)started below.
for unit in otc mariadb; do
    mkdir -p "/etc/systemd/system/$unit.service.d"
    printf '# Off The Cloud: start only with the storage mounted (fstab has nofail).\n[Unit]\nRequiresMountsFor=%s\n' "$MOUNT_POINT" \
        > "/etc/systemd/system/$unit.service.d/otc-storage.conf"
done
systemctl daemon-reload

# ---------------------------------------------------------------------------
# 3. Toolchain + runtime deps
# ---------------------------------------------------------------------------
wiz_step go version "$GO_VERSION" arch "$ARCH"
log "[4/10] Go $GO_VERSION ($ARCH)"
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go$GO_VERSION "; then
    tmp=$(mktemp -d)
    fetch_pinned "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" "$tmp/go.tar.gz" "$GO_SHA"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/go.tar.gz"
    rm -rf "$tmp"
fi

wiz_step onnx version "$ONNXRUNTIME_VERSION" arch "$ORT_ARCH"
log "[5/10] ONNX Runtime $ONNXRUNTIME_VERSION ($ORT_ARCH)"
if [ ! -f /opt/onnxruntime/lib/libonnxruntime.so ]; then
    tmp=$(mktemp -d)
    fetch_pinned "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}.tgz" "$tmp/ort.tgz" "$ORT_SHA"
    tar -xzf "$tmp/ort.tgz" -C "$tmp"
    rm -rf /opt/onnxruntime
    mv "$tmp/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}" /opt/onnxruntime
    rm -rf "$tmp"
fi

wiz_step tagging_model
log "[6/10] RAM++ tagging model (~870MB, only downloaded once)"
mkdir -p "$MODEL_DIR"
# Root's, read by the service: earlier installs handed the directory to
# otc, and root then downloaded into a directory another account could
# put names in. Taken back before anything is fetched into it; a model
# that is a symlink (none was ever installed as one) is fetched again.
chown root:root "$MODEL_DIR"
chmod 0755 "$MODEL_DIR"
for f in "$MODEL_ONNX" "$MODEL_THRESHOLDS" "$MODEL_TAGS" "$FACE_DETECTOR_ONNX" "$FACE_RECOGNIZER_ONNX" "$QUALITY_ONNX"; do
    if [ -L "$f" ]; then rm -f -- "$f"; fi
done
# Smart search's directory, when an earlier install made it: root's like
# the rest, never a symlink.
if [ -L "$EMBED_DIR" ]; then rm -f -- "$EMBED_DIR"; fi
# Each file is checked on its own: fetch_pinned only moves a file into place
# after its hash matches, so one that is present is already verified, and a
# failed small download must not cost another ~870MB.
if [ ! -f "$MODEL_ONNX" ]; then
    # The model's hash is the one Hugging Face lists for it (its LFS oid).
    fetch_pinned "$MODEL_HF_REPO/ram_plus_int8.onnx" "$MODEL_ONNX" 44836da6724dcbdd7446632599c190426af96bd47d4457d47d876b019e10f8bd
fi
if [ ! -f "$MODEL_THRESHOLDS" ]; then
    fetch_pinned "$MODEL_HF_REPO/ram_tag_list_threshold.txt" "$MODEL_THRESHOLDS" b6f81d0de1bc7f9c251af512c413eae976c77dbfbadf700d4e3d90510f4c6447
fi
if [ ! -f "$MODEL_TAGS" ]; then
    # The tag list ships in the verified release itself. Through a
    # temporary file, so a half-written list is never left in place (and
    # whatever an older install's otc-owned directory left under that
    # name goes first: the redirection would follow a symlink).
    rm -f -- "$MODEL_TAGS.tmp"
    gunzip < "$OTC_VERIFIED_SRC/models/models/tag_list_4585.txt.gz" > "$MODEL_TAGS.tmp" \
        || { rm -f "$MODEL_TAGS.tmp"; wiz_error tag_list; die "could not unpack the tag list"; }
    mv -f "$MODEL_TAGS.tmp" "$MODEL_TAGS"
fi

wiz_step face_models
log "[6/10] Face recognition models (issue #52, ~10MB total, only downloaded once)"
if [ ! -f "$FACE_DETECTOR_ONNX" ]; then
    fetch_pinned "$OPENCV_ZOO_RAW/face_detection_yunet/face_detection_yunet_2023mar.onnx" "$FACE_DETECTOR_ONNX" 8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4
fi
if [ ! -f "$FACE_RECOGNIZER_ONNX" ]; then
    fetch_pinned "$OPENCV_ZOO_RAW/face_recognition_sface/face_recognition_sface_2021dec_int8.onnx" "$FACE_RECOGNIZER_ONNX" 2b0e941e6f16cc048c20aee0c8e31f569118f65d702914540f7bfdc14048d78a
fi
# On this day's photo quality model: optional (see QUALITY_ONNX).
if [ ! -f "$QUALITY_ONNX" ]; then
    fetch_optional "$QUALITY_ONNX_URL" "$QUALITY_ONNX" "$QUALITY_ONNX_SHA"
fi
# Read-only to the service (it only loads them). -R changes a symlink
# itself, never what it points to, and chmod -R skips symlinks.
chown -R root:root "$MODEL_DIR"
chmod -R u=rwX,go=rX "$MODEL_DIR"

# ---------------------------------------------------------------------------
# 4. Fetch source, build the binary, install the web bundle
# ---------------------------------------------------------------------------
wiz_step source version "${OTC_RELEASE_VERSION:-?}"
log "[7/10] Source of release ${OTC_RELEASE_VERSION:-?} (verified)"
# The signed release's own source (verified-install.sh), not a clone of
# the branch - the same way updates stage it.
mkdir -p "$SRC_DIR"
rsync -a --delete --exclude '.git' "$OTC_VERIFIED_SRC/" "$SRC_DIR/" || { wiz_error stage_source; die "could not stage the source"; }

wiz_step proto
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
    fetch_pinned "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PROTOC_ARCH}.zip" "$tmp/protoc.zip" "$PROTOC_SHA"
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

wiz_step build
log "[8/10] Build the otc binary"
(
    cd "$SRC_DIR"
    export CGO_ENABLED=1
    export CGO_CFLAGS="-I/opt/onnxruntime/include"
    export CGO_LDFLAGS="-L/opt/onnxruntime/lib -lonnxruntime"
    /usr/local/go/bin/go build -o /usr/bin/otc ./bin/otc.go
)

wiz_step web version "${OTC_RELEASE_VERSION:-?}"
log "[9/10] Web app (release ${OTC_RELEASE_VERSION:-?}, verified)"
# The release's own prebuilt bundle, checked against the signed manifest
# by verified-install.sh - the same one updates install. No Node or npm on
# the device (issue #160: they were fetched with a piped installer).
# Unpacked into a directory of its own under /var/otc-web (root's) and
# switched in by renaming the /var/www symlink over the old one - never
# written file by file into what the device is serving, and never by root
# into a tree another account owns. Same as update.sh's web_switch, which
# also keeps the previous one to roll back to.
WEB_ROOT=/var/www
WEB_DIR=/var/otc-web
web_failed() { [ -z "${web_new:-}" ] || rm -rf -- "$web_new"; wiz_error "$1"; die "$2"; }
mkdir -p "$WEB_DIR" && chown root:root "$WEB_DIR" && chmod 0755 "$WEB_DIR" || web_failed install_web "could not install the web app"
web_new="$(mktemp -d "$WEB_DIR/${OTC_RELEASE_VERSION:-web}.XXXXXX")" || web_failed install_web "could not install the web app"
tar --no-same-owner -xzf "$OTC_VERIFIED_WEB" -C "$web_new" || web_failed unpack_web "could not unpack the web app"
# The service's (a development `make web` copies into it as otc).
chown -R otc:otc "$web_new" || web_failed install_web "could not install the web app"
chmod 0755 "$web_new" || web_failed install_web "could not install the web app"
web_prev=""
if [ -L "$WEB_ROOT" ]; then
    web_prev="$(readlink -f "$WEB_ROOT" || true)"
elif [ -d "$WEB_ROOT" ]; then
    # Once per device: the directory earlier installs wrote into moves
    # beside the new one, and /var/www becomes the symlink.
    web_prev="$WEB_DIR/old.$(date -u +%Y%m%d%H%M%S)"
    mv -T "$WEB_ROOT" "$web_prev" || web_failed install_web "could not install the web app"
fi
sync
ln -sfn "$web_new" "$WEB_DIR/.www.$$" || web_failed install_web "could not install the web app"
mv -Tf "$WEB_DIR/.www.$$" "$WEB_ROOT" || { rm -f "$WEB_DIR/.www.$$"; web_failed install_web "could not install the web app"; }
sync
# The one served and the one before it stay; older ones go.
for d in "$WEB_DIR"/*; do
    [ -d "$d" ] && [ ! -L "$d" ] || continue
    [ "$d" -ef "$web_new" ] && continue
    [ -n "$web_prev" ] && [ "$d" -ef "$web_prev" ] && continue
    rm -rf -- "$d"
done

wiz_step directories
log "[9/10] Runtime directories"
# otc_dir PATH MODE: a directory of the otc user's inside a directory that
# user owns (/var/lib/otc, the storage root). Root never makes, chowns or
# chmods a name there by following it: the otc user can put a symlink in
# its place (a compromised old device can leave one on its disks for a
# recovery), and a re-run of this script then handed whatever it pointed
# at - /var/lib/dpkg/info, say - to the otc user. Anything there that isn't
# a directory is removed (rm never follows a symlink), a missing directory
# is made by the otc user itself, one root made in an earlier install is
# handed over without following anything (chown -h), and the mode is set as
# the otc user, who can only ever change what is already its own.
otc_dir() {
    local dir="${1%/}" mode="$2"
    if [ -L "$dir" ] || { [ -e "$dir" ] && [ ! -d "$dir" ]; }; then
        log "  $dir was not a directory: removed, and made again as one"
        rm -f -- "$dir"
    fi
    if [ ! -d "$dir" ]; then
        runuser -u otc -- mkdir -p -- "$dir" || die "could not make $dir"
    fi
    if [ "$(stat -c %U -- "$dir")" != otc ]; then
        chown -h otc:otc -- "$dir" || die "could not hand $dir to the otc user"
    fi
    runuser -u otc -- chmod "$mode" -- "$dir" || die "could not set the permissions of $dir"
}
# Their parents: in directories of root's (/var/log, /var/lib, /var, /mnt),
# so no other account can put anything in their place.
mkdir -p /var/log/otc /etc/otc /var/lib/otc "$STORAGE_PATH"
chown otc:otc /var/log/otc /var/www /var/lib/otc "$STORAGE_PATH"
# Issue #157: the storage is the service's alone (everything in it is
# read through the service).
chmod 750 "$STORAGE_PATH"
# MariaDB's datadir lives under the storage root on the RAID
# ($MOUNT_POINT/mysql, step 10), and the mysql user has to pass through it:
# others may traverse the root (o+x) but not list or read it - everything
# inside stays 0600/0750. Without this MariaDB can't start (2026-10-01).
chmod o+x "$STORAGE_PATH" "$MOUNT_POINT"
chmod 755 /var/log/otc
# /var/lib/otc/users: issue #82's per-user config/identity directories -
# under StateDirectory=otc (still writable under ProtectSystem=full,
# unlike /etc itself), one subdirectory per spawned user, created by the
# supervisor itself on demand - this just makes sure the parent exists
# with the right ownership up front. The storage's unencrypted directory
# is the service's alone (issue #157), like the rest of the storage.
otc_dir /var/lib/otc/users 0755
otc_dir "$UNENC_PATH" 0750

# ---------------------------------------------------------------------------
# 5. Database + config (device identity generated once, first run only)
# ---------------------------------------------------------------------------
# WiFi power saving off: the Raspberry Pi's WiFi driver starts with it on,
# and it makes the WiFi drop at random until a restart (release 23 does the
# same on devices installed before this).
if [ -d /etc/NetworkManager ]; then
    mkdir -p /etc/NetworkManager/conf.d
    printf '# Off The Cloud: WiFi power saving off (2 = disable).\n[connection]\nwifi.powersave = 2\n' > /etc/NetworkManager/conf.d/otc-wifi-powersave.conf
    nmcli general reload conf >/dev/null 2>&1 || true
fi
if command -v iw >/dev/null 2>&1; then
    for dev in $(iw dev 2>/dev/null | awk '$1 == "Interface" {print $2}'); do
        iw dev "$dev" set power_save off 2>/dev/null || true
    done
fi
# A TCP connection that sat idle for a moment starts again from slow start
# on Linux, as if it were new. The bridge connections the device keeps open
# for the apps do sit idle between two requests, and each piece of a video
# sent over one then spent several round trips reaching full speed: the
# window TCP learnt on the connection is kept instead
# (net.ipv4.tcp_slow_start_after_idle=0), now and at every boot. The same
# as updates/169.sh, for the devices installed before
# (updater/release169_test.go).
otc_network_tuning() {
    local conf=/etc/sysctl.d/80-otc-network.conf want
    want='# Off The Cloud (scripts/install.sh, updates/169.sh): a connection that sat
# idle - the bridge connections the device keeps open for the apps - keeps
# the window TCP learnt on it instead of starting again from slow start.
net.ipv4.tcp_slow_start_after_idle = 0'
    if [ "$(cat "$conf" 2>/dev/null)" != "$want" ]; then
        { mkdir -p /etc/sysctl.d && printf '%s\n' "$want" > "$conf.tmp.$$" && chmod 0644 "$conf.tmp.$$" \
            && mv -f "$conf.tmp.$$" "$conf"; } || { rm -f "$conf.tmp.$$"; return 1; }
    fi
    sysctl -q -w net.ipv4.tcp_slow_start_after_idle=0
}
otc_network_tuning || log "WARNING: TCP keeps slowing down connections that sat idle (net.ipv4.tcp_slow_start_after_idle could not be set)"

wiz_step service
log "[10/10] Database, config, and the systemd service"
# >>> release 143 (device reliability): MariaDB's buffer pool by memory.
# InnoDB's buffer pool - MariaDB's default is 128 MB - sized by the device's
# memory: 256 MB with 4 GB, 768 MB with 8 GB, 1 GB from 12 GB, the default
# under 3 GB. The same drop-in scripts/updates/143.sh writes on devices
# installed before; written before MariaDB is (re)started below.
mariadb_pool_mb() {
    local mem_kb mem_mb
    mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || true)"
    case "$mem_kb" in
        ''|*[!0-9]*) return 0 ;;
    esac
    mem_mb=$((mem_kb / 1024))
    if [ "$mem_mb" -lt 3000 ]; then
        return 0
    elif [ "$mem_mb" -lt 6144 ]; then
        echo 256
    elif [ "$mem_mb" -lt 12288 ]; then
        echo 768
    else
        echo 1024
    fi
}
mariadb_memory_changed=""
mariadb_pool="$(mariadb_pool_mb)"
if [ -n "$mariadb_pool" ] && [ -d /etc/mysql/mariadb.conf.d ]; then
    mariadb_memory_want="$(printf '%s\n' \
        "# Off The Cloud (release 143): InnoDB's buffer pool by the device's memory -" \
        "# 256 MB with 4 GB, 768 MB with 8 GB, 1 GB from 12 GB. Written by the" \
        "# updates and install.sh; applies from MariaDB's next start." \
        "[mysqld]" \
        "innodb_buffer_pool_size = ${mariadb_pool}M")"
    if [ "$(cat /etc/mysql/mariadb.conf.d/70-otc-memory.cnf 2>/dev/null || true)" != "$mariadb_memory_want" ]; then
        mariadb_memory_tmp="$(mktemp /etc/mysql/mariadb.conf.d/.70-otc-memory.XXXXXX)"
        printf '%s\n' "$mariadb_memory_want" > "$mariadb_memory_tmp"
        chmod 0644 "$mariadb_memory_tmp"
        mv -f "$mariadb_memory_tmp" /etc/mysql/mariadb.conf.d/70-otc-memory.cnf
        mariadb_memory_changed=1
        log "MariaDB's buffer pool: ${mariadb_pool} MB"
    fi
fi
# <<< release 143
# mariadb_datadir DIR SEED ACCOUNT: DIR made MariaDB's datadir, owned by
# ACCOUNT (mysql) - seeded from SEED, the package's own just-initialized
# datadir, unless DIR holds a datadir already (its mysql/, as a recovered
# array's does), which is used as it is.
#
# DIR sits in the storage root the otc user owns, and the otc service keeps
# running through a re-run of this script: it can rename DIR and put a
# symlink in its place at any moment, which a check of the name and a later
# use of it can't tell apart - and the copy, run as root, wrote MariaDB's
# files through such a link and gave its target the datadir's owner and
# mode (/etc/systemd/system's, say). So root never uses the name past one
# step: it changes into the directory (cd -P resolves a link there and
# then) and goes on only if that directory is DIR itself; everything after
# works on ".", the directory root is in, wherever its name goes. A
# directory still to be seeded is made root's alone first (nobody else can
# add to it from then on), and is seeded only if nothing in it is anyone
# else's or writable by anyone else - a fresh one is empty, one an earlier
# run left half-copied holds only root's and MariaDB's files. chown -R
# never follows a link.
mariadb_datadir() {
    local dir="${1%/}" seed="${2%/}" account="$3" want
    want="$(cd -P -- "${dir%/*}" && pwd -P)/${dir##*/}" || return 1
    if [ -L "$dir" ] || { [ -e "$dir" ] && [ ! -d "$dir" ]; }; then
        echo "$dir is not a directory" >&2
        return 1
    fi
    mkdir -p -- "$dir" || return 1
    (
        cd -P -- "$dir" || exit 1
        if [ "$(pwd -P)" != "$want" ]; then
            echo "$dir changed while it was being set up (it is $(pwd -P) now)" >&2
            exit 1
        fi
        if [ ! -d mysql ] || [ -L mysql ]; then
            chown root:root . && chmod 0700 . || exit 1
            if [ -n "$(find . -mindepth 1 \( \( ! -user root ! -user "$account" \) -o \( ! -type l \
                \( -perm -0002 -o \( -perm -0020 ! -group root ! -group "$account" \) \) \) \) -print -quit)" ]; then
                echo "$dir holds files of another account, and no datadir to keep" >&2
                exit 1
            fi
            rsync -aHAX --numeric-ids -- "$seed/" ./ || exit 1
        fi
        chown -R "$account:$account" . || exit 1
    )
}
# Point MariaDB's datadir at the RAID array before touching it any further
# below - a recovered array already holding a previous device's
# mysql/mysql is used as-is; a fresh/empty array is seeded from the
# package's own just-initialized datadir instead (mariadb_datadir). Skipped
# entirely under OTC_SKIP_RAID=1, where MariaDB just keeps using its normal
# default datadir.
if [ "$SKIP_RAID" != "1" ]; then
    log "Pointing MariaDB's datadir at $MOUNT_POINT..."
    # A symlink or a file there (an old device can leave one on its disks
    # for a recovery) stops the install before anything is stopped;
    # mariadb_datadir checks again, on the directory itself.
    if [ -L "$MOUNT_POINT/mysql" ] || { [ -e "$MOUNT_POINT/mysql" ] && [ ! -d "$MOUNT_POINT/mysql" ]; }; then
        die "$MOUNT_POINT/mysql is not a directory (a symbolic link, or a file), so it can't be MariaDB's - nothing was changed. Remove it (or move the directory a link points to in its place) and run the installer again"
    fi
    systemctl stop mariadb || true
    mariadb_datadir "$MOUNT_POINT/mysql" /var/lib/mysql mysql \
        || die "could not set up $MOUNT_POINT/mysql as MariaDB's directory (see above) - MariaDB is stopped. Check what is there and run the installer again"
    if grep -q "^datadir" /etc/mysql/mariadb.conf.d/50-server.cnf; then
        sed -i "s#^datadir.*#datadir = $MOUNT_POINT/mysql#" /etc/mysql/mariadb.conf.d/50-server.cnf
    else
        echo "datadir = $MOUNT_POINT/mysql" >> /etc/mysql/mariadb.conf.d/50-server.cnf
    fi
fi
systemctl enable --now mariadb
# release 143: a MariaDB no RAID step stopped above takes the buffer pool's
# drop-in only by a restart.
if [ -n "$mariadb_memory_changed" ] && [ "$SKIP_RAID" = "1" ]; then
    systemctl restart mariadb
fi
for i in $(seq 1 60); do
    mysqladmin ping >/dev/null 2>&1 && break
    if [ "$i" -eq 60 ]; then wiz_error mariadb; die "mariadb did not come up within 60s — check 'systemctl status mariadb'"; fi
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
    # Like the config below: made unreadable to other accounts from the
    # start (umask 077; a chmod after the write left it readable for a
    # moment), flushed, and renamed into place.
    env_tmp="$ENV_FILE.tmp.$$"
    rm -f "$env_tmp"
    (
        umask 077
        {
            echo "# Generated $(date -u +%FT%TZ) by scripts/install.sh."
            echo "# Keep this file out of git — it holds this device's DB password and bridge secret."
            echo "DEVICE_UUID=$DEVICE_UUID"
            echo "BRIDGE_SECRET=$BRIDGE_SECRET"
            echo "OTC_DB_PASS=$OTC_DB_PASS"
        } > "$env_tmp"
    )
    chmod 600 "$env_tmp"
    sync "$env_tmp" 2>/dev/null || sync
    mv -Tf "$env_tmp" "$ENV_FILE"
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
SQL
# Issue #82: multiple users on one device - the primary instance
# provisions/drops a whole database+MySQL user per additional user at
# runtime (see dao/provisioning.go): CREATE DATABASE otc_<uuid>, CREATE
# USER otc_<uuid>, GRANT ALL PRIVILEGES ON otc_<uuid>.* to it, FLUSH
# PRIVILEGES, and later DROP DATABASE / DROP USER; and it reads each one's
# files table for the Users panel's storage usage (dao.UserStorageUsageMB).
# A grantor must hold what it hands off, so this account holds everything,
# with GRANT OPTION, on the databases named otc_<something> (otc_%: in a
# GRANT the _ of otc_<uuid> is itself a wildcard, which only a pattern with
# that wildcard covers - the escaped otc\_% was refused, error 1044; otc's
# own database, three letters, is not one of them), plus CREATE USER
# (CREATE and DROP USER) and RELOAD (FLUSH) - and nothing on the server as
# a whole: it used to hold ALL PRIVILEGES ON *.* WITH GRANT OPTION (FILE,
# SUPER, SHUTDOWN, every other database), revoked here and by release 143.
# The narrow grants come first, so the running service never goes without
# what it uses. Quoted: the backticks are SQL, not a command substitution.
mysql <<'SQL'
GRANT ALL PRIVILEGES ON `otc_%`.* TO 'otc'@'localhost' WITH GRANT OPTION;
REVOKE ALL PRIVILEGES ON *.* FROM 'otc'@'localhost';
REVOKE GRANT OPTION ON *.* FROM 'otc'@'localhost';
GRANT CREATE USER, RELOAD ON *.* TO 'otc'@'localhost';
FLUSH PRIVILEGES;
SQL
if mysql otc -N -B -e 'SHOW TABLES LIKE "files"' 2>/dev/null | grep -q files; then
    log "schema already present, applying any new tables/columns since your last update"
else
    tail -n +10 "$SRC_DIR/db/db.sql" | mysql otc
fi
# >>> schema-migrations: from here to the closing mark, run as it is by the container image too (docker/)
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
    -- The slow lane's queue: uploads still waiting for tags and faces.
    CREATE TABLE IF NOT EXISTS pending_analysis (hash VARCHAR(64) NOT NULL, queued DATETIME NOT NULL, PRIMARY KEY (hash)) ENGINE=InnoDB;
    -- Issue #180: share links list (kind, description, opens) and galleries.
    ALTER TABLE shared_links MODIFY size BIGINT NOT NULL;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS kind VARCHAR(16) NOT NULL DEFAULT 'archive';
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS description BLOB NULL;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS files INT NOT NULL DEFAULT 0;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS opens INT NOT NULL DEFAULT 0;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS last_opened DATETIME NULL;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS expires DATETIME NULL;
    CREATE INDEX IF NOT EXISTS shared_links_uuid ON shared_links (uuid);
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS face_recognition_enabled TINYINT(1) NOT NULL DEFAULT 0;
    -- Issue #178: off unless chosen - for a column added before, the default only.
    ALTER TABLE settings ALTER COLUMN face_recognition_enabled SET DEFAULT 0;
    -- Issue #181: image tagging can be turned off.
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS image_tagging_enabled TINYINT(1) NOT NULL DEFAULT 1;
    -- Issue #153: the space friends' posts may take, in MB.
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS social_storage_limit_mb INT NOT NULL DEFAULT 5120;
    -- When a friend's device was last heard from (re-link rule).
    ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS last_seen DATETIME NULL;
    -- Issue #92: ends a friend's notification catch-up suppression once their
    -- pre-existing backlog is fully replayed (see social.go's
    -- updateFriendEvents) - defaulting to 0 on an upgrade is fine even for
    -- already-fully-synced friends, since the very next sync cycle finds
    -- nothing pending and flips it back on within one 120s tick.
    ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS notifications_started TINYINT(1) NOT NULL DEFAULT 0;
    -- Issue #174: unfriending with "delete what I shared there".
    ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS forget_requested DATETIME NULL;
    ALTER TABLE social_publications_comments ADD COLUMN IF NOT EXISTS author_domain VARCHAR(128) NOT NULL DEFAULT '';
    ALTER TABLE events ADD COLUMN IF NOT EXISTS target VARCHAR(128) NULL;
    -- Issue #172: paths compare exactly (case and accents), so deleting or
    -- sharing /Cafe/ never takes /café/ or /CAFE/ with it.
    ALTER TABLE files MODIFY `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
    ALTER TABLE file_versions MODIFY `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
    ALTER TABLE upload_only_folders MODIFY `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
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
    -- Issue #125: the Android app's FCM tokens (see db.sql).
    CREATE TABLE IF NOT EXISTS fcm_tokens (
      `token` varchar(512) NOT NULL,
      `created` datetime NOT NULL,
      PRIMARY KEY (`token`)
    ) ENGINE=InnoDB;
SQL
    # Release 92: one like per domain on a post or comment. A friend's rows
    # that are even in number end in an unlike (see updates/92.sh) and all
    # go, then duplicates (the earliest is kept), and the counters are
    # recounted from the rows. Idempotent, like everything here.
    mysql "$db" <<'SQL'
    DELETE l FROM social_publication_likes l JOIN (
      SELECT pub_uuid, friend_domain FROM social_publication_likes
      WHERE friend_domain IN (SELECT domain FROM social_friendship)
      GROUP BY pub_uuid, friend_domain HAVING COUNT(*) % 2 = 0
    ) d USING (pub_uuid, friend_domain);
    DELETE l1 FROM social_publication_likes l1 JOIN social_publication_likes l2
      ON l1.pub_uuid = l2.pub_uuid AND l1.friend_domain = l2.friend_domain
      AND (l1.dt > l2.dt OR (l1.dt = l2.dt AND l1.uuid > l2.uuid));
    ALTER TABLE social_publication_likes ADD UNIQUE INDEX IF NOT EXISTS like_once (pub_uuid, friend_domain);
    UPDATE social_publications p SET likes = (SELECT COUNT(*) FROM social_publication_likes l WHERE l.pub_uuid = p.uuid);
    DELETE l FROM social_publication_comment_likes l JOIN (
      SELECT comment_uuid, friend_domain FROM social_publication_comment_likes
      WHERE friend_domain IN (SELECT domain FROM social_friendship)
      GROUP BY comment_uuid, friend_domain HAVING COUNT(*) % 2 = 0
    ) d USING (comment_uuid, friend_domain);
    DELETE l1 FROM social_publication_comment_likes l1 JOIN social_publication_comment_likes l2
      ON l1.comment_uuid = l2.comment_uuid AND l1.friend_domain = l2.friend_domain
      AND (l1.dt > l2.dt OR (l1.dt = l2.dt AND l1.uuid > l2.uuid));
    ALTER TABLE social_publication_comment_likes ADD UNIQUE INDEX IF NOT EXISTS comment_like_once (comment_uuid, friend_domain);
    UPDATE social_publications_comments c SET likes = (SELECT COUNT(*) FROM social_publication_comment_likes l WHERE l.comment_uuid = c.uuid);
SQL
    # Release 93 (issue #187): sizes of 2 GiB or more wrapped in an int.
    # MODIFY keeps the index on files.size and changes nothing on a column
    # that is already BIGINT; the device corrects the sizes stored wrapped
    # once, from the blobs, and records it in sizes_backfilled.
    mysql "$db" <<'SQL'
    ALTER TABLE files MODIFY `size` BIGINT NOT NULL;
    ALTER TABLE file_versions MODIFY `size` BIGINT NOT NULL;
    ALTER TABLE social_publications_files MODIFY `size` BIGINT NOT NULL;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS sizes_backfilled TINYINT(1) NOT NULL DEFAULT 0;
SQL
    # Release 108 (issue #192): folders kept out of Images, and the content
    # whose analysis that skipped (see db.sql).
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS out_of_images_folders (
      `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
      PRIMARY KEY (`path`)
    ) ENGINE=InnoDB;
    CREATE TABLE IF NOT EXISTS skipped_analysis (
      `hash` VARCHAR(64) NOT NULL,
      `skipped` DATETIME NOT NULL,
      PRIMARY KEY (`hash`)
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
    # Release 118 (docs/i18n.md): the user's language for every app, the
    # language of the app that last registered for pushes, and alerts as a
    # catalog key next to their English (see updates/118.sh and db.sql).
    # After the notifications block above, which may have just created it.
    mysql "$db" <<'SQL'
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS `language` VARCHAR(16) NOT NULL DEFAULT '';
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS `last_ui_language` VARCHAR(16) NOT NULL DEFAULT '';
    ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `msg_key` VARCHAR(96) NULL;
    ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `msg_args` MEDIUMTEXT NULL;
    ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `msg_lines` MEDIUMTEXT NULL;
    ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `update_version` VARCHAR(16) NULL;
SQL
    # Release 143 (updates/143.sh): the files table's indexes on modified
    # and size, which no query uses - dropped in place, quickly; nothing to
    # do once they are gone.
    mysql "$db" <<'SQL'
    ALTER TABLE files DROP INDEX IF EXISTS `modified`, DROP INDEX IF EXISTS `size`;
SQL
    # Release 148 (updates/148.sh): Places, the map in Images - where each
    # photo was taken, whether the one-off read of what the device held
    # before has run, and the switch, off until the user turns it on (see
    # db.sql).
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS photo_locations (
      `hash` VARCHAR(64) NOT NULL,
      `lat` DOUBLE NULL,
      `lon` DOUBLE NULL,
      PRIMARY KEY (`hash`)
    ) ENGINE=InnoDB;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS locations_backfilled TINYINT(1) NOT NULL DEFAULT 0;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS places_enabled TINYINT(1) NOT NULL DEFAULT 0;
SQL
    # Release 150 (updates/150.sh): On this day - how each photo looks, what
    # the user hid from the memories, and the switch, on by default (see
    # db.sql).
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS photo_quality (
      `hash` VARCHAR(64) NOT NULL,
      `aesthetic` FLOAT NULL,
      `sharpness` FLOAT NOT NULL,
      `dark` FLOAT NOT NULL,
      `bright` FLOAT NOT NULL,
      `brightness` FLOAT NOT NULL,
      `width` INT NOT NULL,
      `height` INT NOT NULL,
      `camera` TINYINT(1) NULL,
      `scored` DATETIME NOT NULL,
      PRIMARY KEY (`hash`)
    ) ENGINE=InnoDB;
    CREATE TABLE IF NOT EXISTS memories_hidden (
      `kind` VARCHAR(16) NOT NULL,
      `value` VARCHAR(64) NOT NULL,
      `hidden` DATETIME NOT NULL,
      PRIMARY KEY (`kind`, `value`)
    ) ENGINE=InnoDB;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS memories_enabled TINYINT(1) NOT NULL DEFAULT 1;
SQL
    # Release 151 (updates/151.sh): Smart search - each photo's description,
    # sealed with the user's key, and the switch (see db.sql).
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS photo_embeddings (
      `hash` VARCHAR(64) NOT NULL,
      `model` VARCHAR(64) NOT NULL,
      `vec` VARBINARY(2048) NOT NULL,
      `made` DATETIME NOT NULL,
      PRIMARY KEY (`hash`)
    ) ENGINE=InnoDB;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS smart_search_enabled TINYINT(1) NOT NULL DEFAULT 1;
SQL
    # Release 152 (updates/152.sh): Smart search on by default. A database
    # whose column still defaults to off (151's) is turned on once, then
    # defaults to on - the default is the mark, so a later run never turns
    # back on a library its user turned off.
    if [ "$(mysql "$db" -N -B -e "SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'smart_search_enabled'")" = 0 ]; then
        mysql "$db" <<'SQL'
    UPDATE settings SET smart_search_enabled = 1;
    ALTER TABLE settings ALTER COLUMN smart_search_enabled SET DEFAULT 1;
SQL
    fi
    # Release 152 (updates/152.sh): dates taken - where the one-off pass
    # that gives what the device held its dates taken has got to (see
    # db.sql).
    mysql "$db" <<'SQL'
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS dates_taken_cursor VARCHAR(64) NOT NULL DEFAULT '';
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS dates_taken_backfilled TINYINT(1) NOT NULL DEFAULT 0;
SQL
    # Release 157 (updates/157.sh): a person's face chosen by the owner -
    # one of their photos' faces, or a picture of their own - and how good
    # each face looks, for the one the device chooses (see db.sql).
    mysql "$db" <<'SQL'
    ALTER TABLE people ADD COLUMN IF NOT EXISTS cover_chosen TINYINT(1) NOT NULL DEFAULT 0;
    ALTER TABLE people ADD COLUMN IF NOT EXISTS cover_image MEDIUMBLOB NULL;
    ALTER TABLE faces ADD COLUMN IF NOT EXISTS quality FLOAT NULL;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS face_quality_done TINYINT(1) NOT NULL DEFAULT 0;
SQL
    # Release 158 (updates/corrected/158.sh): whether the user chose Image
    # Tagging - until then the device sets it by its memory. A switch
    # already off (the old default was on) is marked as their choice, once:
    # the column is added with no value, given one from the switch, and only
    # then made NOT NULL, so a run cut in between finishes the job the next
    # time (a column still NULL-able). It used to be added NOT NULL first,
    # and a run cut before the marking found it there and skipped it, leaving
    # an "off" to the memory default.
    if [ "$(mysql "$db" -N -B -e "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'image_tagging_chosen'")" != NO ]; then
        mysql "$db" <<'SQL'
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS image_tagging_chosen TINYINT(1) NULL;
    UPDATE settings SET image_tagging_chosen = (image_tagging_enabled = 0) WHERE image_tagging_chosen IS NULL;
    ALTER TABLE settings MODIFY image_tagging_chosen TINYINT(1) NOT NULL DEFAULT 0;
SQL
    fi
    # Release 163 (updates/163.sh): collection Rating - a collection's
    # switch and its link key, the photos' stars and sealed comments, a
    # gallery's collection and rating, the user's key pair the comments are
    # sealed to, and the collection a Rating alert names (see db.sql).
    mysql "$db" <<'SQL'
    ALTER TABLE image_groups ADD COLUMN IF NOT EXISTS rating TINYINT(1) NOT NULL DEFAULT 0;
    ALTER TABLE image_groups ADD COLUMN IF NOT EXISTS rating_key BLOB NULL;
    CREATE TABLE IF NOT EXISTS photo_ratings (`group_id` VARCHAR(36) NOT NULL, `hash` VARCHAR(64) NOT NULL, `stars` TINYINT NOT NULL DEFAULT 0, `comment` BLOB NULL, `shared_comment` BLOB NULL, `link` VARCHAR(64) NULL, `updated` DATETIME NOT NULL, PRIMARY KEY (`group_id`, `hash`), KEY (`hash`)) ENGINE=InnoDB;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS group_id VARCHAR(36) NULL;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS rating TINYINT(1) NOT NULL DEFAULT 0;
    ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS rating_alerted DATETIME NULL;
    ALTER TABLE vault ADD COLUMN IF NOT EXISTS rating_public VARBINARY(32) NULL;
    ALTER TABLE vault ADD COLUMN IF NOT EXISTS rating_private BLOB NULL;
    ALTER TABLE notifications ADD COLUMN IF NOT EXISTS group_id VARCHAR(36) NULL;
SQL
    # Release 168 (updates/168.sh, issue #206): Images' month counts, kept
    # as the library changes (see db.sql and dao/months.go). Built by the
    # device itself at its first Images opening.
    mysql "$db" <<'SQL'
    CREATE TABLE IF NOT EXISTS photo_months (`month` INT NOT NULL, `images` INT NOT NULL DEFAULT 0, `media` INT NOT NULL DEFAULT 0, PRIMARY KEY (`month`)) ENGINE=InnoDB;
    CREATE TABLE IF NOT EXISTS photo_month_hashes (`hash` VARCHAR(64) NOT NULL, `images` INT NULL, `media` INT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS photo_months_for MEDIUMTEXT NULL;
    ALTER TABLE settings ADD COLUMN IF NOT EXISTS photo_months_run VARCHAR(32) NULL;
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
# it. The list itself must be read, though: the table was made just above,
# so a query that fails is the database failing (a restart, an error), not
# "no other users" - it used to read as that, and every other user's
# database was left behind, for good, by a run that then succeeded.
user_dbs="$(mysql otc -N -B -e 'SELECT db_name FROM users')" || {
    log "ERROR: could not read the users table, so the other users' databases can't be brought up to date - nothing was skipped: run this again"
    false
}
for user_db in $user_dbs; do
    # The otc service writes this table, and the name reaches SQL, grep
    # and mysql's command line here as root: only the shape the device
    # itself generates (dao's checkGenerated), so nothing can pass as
    # an option or break out of the quotes. Not echoed: untrusted text.
    if ! [[ "$user_db" =~ ^otc_[0-9a-f]{32}$ ]]; then
        log "  WARNING: skipping a users row whose db_name this device didn't generate"
        continue
    fi
    # The same for the answer to "does it exist?": only a database that is
    # really missing is skipped.
    user_db_found="$(mysql -N -B -e "SHOW DATABASES LIKE '$user_db'")" || {
        log "ERROR: could not ask the database whether $user_db exists - nothing was skipped: run this again"
        false
    }
    if grep -qxF -- "$user_db" <<<"$user_db_found"; then
        log "  migrating additional user database $user_db"
        apply_schema_migrations "$user_db"
    else
        log "  WARNING: user database $user_db is in the users table but doesn't exist - skipping"
    fi
done
# <<< schema-migrations

# Local-only (no bridge): the subdomain is the bare name, no ".<bridge>".
FULL_DOMAIN="${SUBDOMAIN}${BRIDGE_ADDR:+.$BRIDGE_ADDR}"
mysql otc <<SQL
INSERT INTO settings (device_uuid, subdomain, bridge_secret)
SELECT '${DEVICE_UUID}', '${FULL_DOMAIN}', '${BRIDGE_SECRET}'
WHERE NOT EXISTS (SELECT 1 FROM settings);
SQL

# OTC_DEMO: a public demo device's server. No Wi-Fi to watch, and more
# standby connections to the bridge: every visitor's session holds one.
# Demo mode is turned on by hand once the content is loaded, so its lines
# are kept from the config this rewrites.
DEMO_CONFIG=""
if [ "$DEMO" = "1" ]; then
    DEMO_CONFIG=$'wifi-watchdog=off\nbridge-pool-target=60\n'
    if [ -f "$INI_FILE" ]; then
        DEMO_KEPT=$(grep -E '^(demo|demo-password)=' "$INI_FILE" || true)
        [ -z "$DEMO_KEPT" ] || DEMO_CONFIG+="$DEMO_KEPT"$'\n'
    fi
fi
# The other [otc] lines the config an earlier run wrote has - a fork's
# update-repo and update-releases, low-memory, a Wi-Fi watchdog switched
# off - are kept: this rewrite used to drop them. Never one written above
# (bridge-addr and bridge-default were read back at the top), nor demo
# mode's, which only a demo server keeps (DEMO_KEPT).
OTC_KEPT=""
if [ -f "$INI_FILE" ]; then
    kept_skip=" bridge-addr bridge-default storage-path unenc-storage-path max-thumbnail-width-px shared-link-ttl-hours demo demo-password "
    [ "$DEMO" = "1" ] && kept_skip+="wifi-watchdog bridge-pool-target "
    OTC_KEPT=$(awk -v skip="$kept_skip" '
        /^[[:space:]]*\[/ { section = $0; gsub(/[[:space:]]/, "", section); next }
        section == "[otc]" && /^[[:space:]]*[a-z0-9-]+[[:space:]]*=/ {
            k = $0; sub(/[[:space:]]*=.*/, "", k); gsub(/[[:space:]]/, "", k)
            if (index(skip, " " k " ") == 0 && !(k in seen)) { seen[k] = 1; print }
        }' "$INI_FILE")
    [ -z "$OTC_KEPT" ] || OTC_KEPT+=$'\n'
fi
# Written beside the config, with its final owner and mode, flushed, and
# renamed over it: the file holds the database password (never readable
# by other accounts, not even for a moment), and a power cut halfway
# through a rewrite in place left a device that couldn't start.
INI_TMP="$INI_FILE.tmp.$$"
rm -f "$INI_TMP"
(umask 077 && cat > "$INI_TMP") <<EOF
[otc]
bridge-addr=$BRIDGE_ADDR
bridge-default=$BRIDGE_DEFAULT
storage-path=$STORAGE_PATH
unenc-storage-path=$UNENC_PATH
max-thumbnail-width-px=1000
shared-link-ttl-hours=168
${DEMO_CONFIG}${OTC_KEPT}
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
max-images-search=30

# Issue #52: face recognition ("People" search), humans only - off by
# default (toggle it from Settings in the app), regardless of this section
# being present. See face_recognition/face_recognition.go's package doc
# comment for what these two models are.
[faces]
detector-model-path=$FACE_DETECTOR_ONNX
recognizer-model-path=$FACE_RECOGNIZER_ONNX

# On this day (Memories): the photo quality model. Optional - a device
# without the file measures photos by sharpness and exposure alone.
[quality]
model-path=$QUALITY_ONNX

# Smart search (beta): SigLIP 2, the photos' and the searches' model. Used
# from these paths when its files are there; otherwise the device downloads
# them once Smart search is turned on, into download-dir (default:
# models/siglip2-base in its working directory), at download-kbps kilobytes
# a second (default 2048; 0 for no cap), from model-url (default: this
# project's "models" release).
[embedder]
image-model-path=$EMBED_DIR/image.onnx
text-model-path=$EMBED_DIR/text.onnx
tokenizer-path=$EMBED_DIR/tokenizer.model
EOF
# The database password (and the rest of the device's settings): root
# and the service only - it used to be left readable by every account.
chown root:otc "$INI_TMP"
chmod 640 "$INI_TMP"
sync "$INI_TMP" 2>/dev/null || sync
mv -Tf "$INI_TMP" "$INI_FILE"

# Issue #97 follow-up: the RAID status LEDs (scripts/raid_watch.py, driven
# over GPIO) are baked into the prebuilt image by build_image.sh - but this
# installer, the path most devices are actually set up through (Makefile.pi
# runs it too), never installed them at all. A device brought up with
# `curl | bash` had a perfectly healthy array and two dark LEDs, with
# nothing on the device even hinting why (no service, no script, no log).
# Same unit the image installs, so both agree.
log "Status LEDs (raid-watch.service)"
# The GPIO binding is genuinely optional: raid_watch.py degrades to a
# dry-run when it can't import one (see its RPi.GPIO import), which is the
# right behaviour on any non-Pi box - so a missing package must never fail
# the whole install. Package name differs across Raspberry Pi OS releases.
apt-get install -y python3-rpi-lgpio 2>/dev/null \
    || apt-get install -y python3-rpi.gpio 2>/dev/null \
    || log "  (no RPi GPIO package available - raid-watch will run in dry-run mode)"

if [ "$DEMO" = "1" ]; then
    # A demo server's disks are its own: the watcher grows and rebuilds
    # md0 by itself, as root.
    log "  demo device: no RAID watcher"
    if [ -f /etc/systemd/system/raid-watch.service ]; then
        systemctl disable --now raid-watch.service 2>/dev/null || true
        rm -f /etc/systemd/system/raid-watch.service /usr/local/bin/raid_watch.py
        systemctl daemon-reload
    fi
elif [ -f "$SRC_DIR/scripts/raid_watch.py" ]; then
    install -m 0755 "$SRC_DIR/scripts/raid_watch.py" /usr/local/bin/raid_watch.py

    # Not after multi-user.target: it tells the apps when the storage is
    # missing a minute after boot (otc_storage_units below).
    cat > /etc/systemd/system/raid-watch.service <<'EOF'
[Unit]
Description=RAID1 watcher + LED driver
After=mdadm.service

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

# Issue #207: the device without its storage. otc.service and MariaDB need
# the storage mounted (the otc-storage.conf drop-ins); with the disks
# missing, or the mirror not assembled, neither starts and every app used
# to say "device unreachable". otc-degraded.service runs the same binary
# without a database (`otc <env> degraded`): on its HTTP port and through
# the bridge, with the identity's copy the service keeps on the boot disk,
# it answers every request storage_unavailable, so the apps say why. The
# root raid_watch.py starts it a minute after the storage went missing,
# mounts the storage once its device is there and then starts MariaDB and
# otc.service, which stops it (Conflicts=). It has no [Install]: nothing
# starts it at boot. It may never write the storage: the mount point is
# read-only in its namespace. raid-watch.service is no longer ordered after
# multi-user.target, which waits for otc.service's start - three minutes
# when the disks are missing (the fstab line's device timeout). The same as
# updates/166.sh, for the devices installed before.
otc_storage_units() {
    local env="$1" mount_point="$2" unit=/etc/systemd/system/otc-degraded.service
    cat > "$unit.tmp" <<UNITEOF
# Off The Cloud without its storage (issue #207): started by raid_watch.py
# while $mount_point is missing, stopped by otc.service's start.
[Unit]
Description=Off The Cloud without its storage (tells the apps why)
Wants=network-online.target
After=network-online.target
Conflicts=otc.service
Before=otc.service

[Service]
Type=simple
User=otc
Group=otc
WorkingDirectory=/var/lib/otc
ExecStart=/usr/bin/otc $env degraded
Restart=on-failure
RestartSec=5
StateDirectory=otc
LogsDirectory=otc
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ReadOnlyPaths=-$mount_point
CapabilityBoundingSet=
SystemCallArchitectures=native
UNITEOF
    chmod 0644 "$unit.tmp"
    mv -f "$unit.tmp" "$unit"
    if [ -f /etc/systemd/system/raid-watch.service ]; then
        sed -i 's/^After=multi-user.target mdadm.service$/After=mdadm.service/' /etc/systemd/system/raid-watch.service
    fi
    systemctl daemon-reload
}

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
# Issue #156: the service locks its memory (mlockall) so decrypted data
# is never swapped out, and never dumps core.
LimitMEMLOCK=infinity
LimitCORE=0
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
otc_storage_units "$ENVIRONMENT" "$MOUNT_POINT"

# Issue #94: the Update button. otc.service above runs with
# NoNewPrivileges=true, so the device can't sudo its way to root for an
# update; it writes /var/lib/otc/update.request instead and this path unit
# runs the update as root - see scripts/update-runner/otc-update-runner.sh.
log "Update trigger (otc-update.path)"
install -m 0755 "$SRC_DIR/scripts/update-runner/otc-update-runner.sh" /usr/local/bin/otc-update-runner
# Named by otc-update.service (ExecStopPost): marks a run cut off part-way
# as failed instead of leaving it "running".
install -m 0755 "$SRC_DIR/scripts/update-runner/otc-update-stopped.sh" /usr/local/bin/otc-update-stopped
# Issue #160: the public key every update's manifest must be signed with.
mkdir -p /etc/otc && install -m 0644 "$SRC_DIR/scripts/release-signing.pub" /etc/otc/release-signing.pub
install -m 0644 "$SRC_DIR/scripts/update-runner/otc-update.service" /etc/systemd/system/otc-update.service
install -m 0644 "$SRC_DIR/scripts/update-runner/otc-update.path" /etc/systemd/system/otc-update.path

# Issue #145: switching the bridge on later from Settings - the same
# trigger-file shape, see scripts/bridge-runner/otc-bridge-runner.sh.
log "Bridge switch-on trigger (otc-bridge.path)"
install -m 0755 "$SRC_DIR/scripts/bridge-runner/otc-bridge-runner.sh" /usr/local/bin/otc-bridge-runner
install -m 0644 "$SRC_DIR/scripts/bridge-runner/otc-bridge.service" /etc/systemd/system/otc-bridge.service
install -m 0644 "$SRC_DIR/scripts/bridge-runner/otc-bridge.path" /etc/systemd/system/otc-bridge.path
# Tailscale Funnel's switch: tailscaled runs only while Funnel is on - see
# scripts/tailscale-runner/otc-tailscale-runner.sh.
log "Tailscale switch trigger (otc-tailscale.path)"
install -m 0755 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale-runner.sh" /usr/local/bin/otc-tailscale-runner
install -m 0644 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale.service" /etc/systemd/system/otc-tailscale.service
install -m 0644 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale.path" /etc/systemd/system/otc-tailscale.path
# Settings > Restart device and Shut down device, and the Wi-Fi watchdog's
# restart of the Wi-Fi (wifiwatch) - the same trigger-file shape, see
# scripts/device-runner/.
log "Restart, shutdown and Wi-Fi restart triggers (otc-reboot.path, otc-poweroff.path, otc-wifi-restart.path)"
for unit in reboot poweroff wifi-restart; do
    install -m 0755 "$SRC_DIR/scripts/device-runner/otc-$unit-runner.sh" "/usr/local/bin/otc-$unit-runner"
    install -m 0644 "$SRC_DIR/scripts/device-runner/otc-$unit.service" "/etc/systemd/system/otc-$unit.service"
    install -m 0644 "$SRC_DIR/scripts/device-runner/otc-$unit.path" "/etc/systemd/system/otc-$unit.path"
done
# Settings > Wi-Fi: changing the network the device uses (a Python runner,
# hence its own lines). It acts only where NetworkManager runs; nothing
# here installs or requires NetworkManager.
log "Wi-Fi change trigger (otc-wifi-join.path)"
install -m 0755 "$SRC_DIR/scripts/device-runner/otc-wifi-join-runner.py" /usr/local/bin/otc-wifi-join-runner
install -m 0644 "$SRC_DIR/scripts/device-runner/otc-wifi-join.service" /etc/systemd/system/otc-wifi-join.service
install -m 0644 "$SRC_DIR/scripts/device-runner/otc-wifi-join.path" /etc/systemd/system/otc-wifi-join.path

# Issue #178: what the setup wizard asked about the owner - the profile's
# name, description and picture, and whether face recognition is on (off
# unless chosen). All optional. Same handling as the password below: a
# root-only file, applied as the otc user before the service first starts,
# then shredded. A recovered device keeps what it had.
if [ -n "${OTC_SETUP_PROFILE_FILE:-}" ] && [ -f "$OTC_SETUP_PROFILE_FILE" ]; then
    log "Owner profile"
    runuser -u otc -- /usr/bin/otc "$ENVIRONMENT" init-profile < "$OTC_SETUP_PROFILE_FILE" \
        || log "  WARNING: could not apply the profile from the setup - set it in Settings"
    shred -u "$OTC_SETUP_PROFILE_FILE" 2>/dev/null || rm -f "$OTC_SETUP_PROFILE_FILE"
fi

# The owner password chosen in the setup wizard (a root-only file it hands
# over in OTC_OWNER_PASSWORD_FILE): set before the service first starts, as
# the otc user so the log keeps its owner. A recovered device already has
# one, which the command leaves alone. The file goes either way.
if [ -n "${OTC_OWNER_PASSWORD_FILE:-}" ] && [ -f "$OTC_OWNER_PASSWORD_FILE" ]; then
    log "Setting the owner password chosen in the setup wizard"
    if ! runuser -u otc -- /usr/bin/otc "$ENVIRONMENT" init-owner-password < "$OTC_OWNER_PASSWORD_FILE"; then
        shred -u "$OTC_OWNER_PASSWORD_FILE" 2>/dev/null || rm -f "$OTC_OWNER_PASSWORD_FILE"
        wiz_error owner_password
        die "could not set the owner password"
    fi
    shred -u "$OTC_OWNER_PASSWORD_FILE" 2>/dev/null || rm -f "$OTC_OWNER_PASSWORD_FILE"
fi

systemctl daemon-reload
systemctl enable otc.service
systemctl enable --now otc-update.path
systemctl enable --now otc-bridge.path
systemctl enable --now otc-tailscale.path
rm -f /var/lib/otc/reboot.request /var/lib/otc/poweroff.request /var/lib/otc/wifi-restart.request
systemctl enable --now otc-reboot.path
systemctl enable --now otc-poweroff.path
systemctl enable --now otc-wifi-restart.path
rm -f /run/otc/wifi-join.request
systemctl enable --now otc-wifi-join.path
systemctl restart otc.service

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
echo ""
echo "=========================================================="
echo " OTC installed and running."
echo " Local web UI: http://${IP:-<this-machine>}:$HTTP_PORT/"
if [ -n "$BRIDGE_ADDR" ]; then echo " Bridge address: ${FULL_DOMAIN}"; else echo " Local only (no bridge): http://otc.local:${HTTP_PORT}"; fi
if [ -n "${OTC_OWNER_PASSWORD_FILE:-}" ]; then echo " Sign in with the password you chose during setup."; else echo " First 'Sign In' sets your password permanently — see README.md."; fi
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
# Written beside the file, flushed and renamed over it, as update.sh does:
# a power cut right after `echo >` left an empty file, which the updater
# read as "already up to date" for good. version.migrated (the last release
# whose migrations ran, see update.sh) follows: a fresh install has
# everything they would have done, and the updater recovers the version
# from it should the version file ever be unreadable.
write_version_file() {
    local tmp="$1.tmp.$$"
    printf '%s\n' "$2" > "$tmp" && { sync "$tmp" 2>/dev/null || sync; } && mv -Tf "$tmp" "$1" || { rm -f "$tmp"; return 1; }
}
if [ -n "${OTC_RELEASE_VERSION:-}" ]; then
    # The signed release just installed (verified-install.sh).
    latest_release="$OTC_RELEASE_VERSION"
    if [[ "$latest_release" =~ ^[0-9]{1,9}$ ]]; then
        write_version_file /etc/otc/version "$latest_release" || die "could not record the release in /etc/otc/version"
        migrated_before="$(head -c 16 /etc/otc/version.migrated 2>/dev/null | head -n 1 || true)"
        if ! [[ "$migrated_before" =~ ^[0-9]{1,9}$ ]] || [ "$migrated_before" -lt "$latest_release" ]; then
            write_version_file /etc/otc/version.migrated "$latest_release" || die "could not record the release in /etc/otc/version.migrated"
        fi
        sync
        echo "[otc-install] Recorded release $latest_release in /etc/otc/version"
    fi
fi

touch /etc/otc/.install-complete
echo "[otc-install] Done. Marker at /etc/otc/.install-complete (remove it if you want the next run to treat this as a fresh install)."

# The verified release verified-install.sh staged (archive, tree and web
# bundle): nothing reads it once installed, and a re-run downloads afresh.
# Only that exact path, never whatever the variable might hold.
case "${OTC_VERIFIED_SRC:-}" in /opt/otc-verified/src) rm -rf /opt/otc-verified ;; esac
