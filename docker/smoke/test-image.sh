#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The tests an image passes before it is published (scripts/docker-release.sh),
# on any Linux Docker host (Colima included):
#
#   docker/smoke/test-image.sh [--mirror] <image> <release> <otc-smoke binary> [data dir]
#
# It starts docker/compose.yaml with <image>, under a project and container
# name of its own, and checks, in order:
#   1. it turns healthy (models downloaded, MariaDB answering) within 25 minutes;
#   2. /etc/otc/version is <release>;
#   3. the web app is served;
#   4. `otc-entrypoint set-password` sets the owner password;
#   5. otc-smoke (docker/smoke): a wrong password is refused, the owner
#      password signs in, and the device reports <release>;
#   6. after `docker compose restart` it is healthy again and 5 passes again;
#   7. the log has no Go panic.
# With --mirror it does all that on two mirrored disks (docker/compose.mirror.yaml):
# two blank 3 GiB loop devices on the Docker host, made from files next to the
# data dir by a privileged container of <image> (it refuses to start when the
# host has no free loop device). It also checks, after the first start and the
# restart, that the container's /mnt/storage is that mirror (findmnt) and that
# /proc/mdstat shows the array with both disks, whole; then that after
# `docker compose down` and `up -d` the array is assembled again, whole, and 5
# passes again; and that the data dir's storage/ stayed empty.
# The data dir (default /srv/otc-image-test, on the Docker host) must not
# exist yet, nor, with --mirror, its disks (<data dir>.disk1 and .disk2). The
# container, the data dir and the disks (their array stopped and their loop
# devices detached first) are removed at the end, whatever the outcome. The
# ports are compose's defaults (OTC_HTTP_PORT and the others override them),
# so they must be free.
set -euo pipefail

mirror=false
if [ "${1:-}" = --mirror ]; then
    mirror=true
    shift
fi
image=${1:?usage: test-image.sh [--mirror] <image> <release> <otc-smoke binary> [data dir]}
release=${2:?release number}
smoke=${3:?otc-smoke binary}
data=${4:-/srv/otc-image-test}
port=${OTC_HTTP_PORT:-8080}
root=$(cd "$(dirname "$0")/../.." && pwd)
project=otc-image-test
c=otc-image-test
work=$(mktemp -d)
parent=$(dirname "$data")
name=$(basename "$data")
# The mirror's disks: their files (as the helper containers see them) and
# the loop devices they are attached to on the Docker host.
disk_files=("/parent/$name.disk1" "/parent/$name.disk2")
disk1=
disk2=

step() { echo "==> $*"; }
fail() {
    echo "FAIL: $*" >&2
    if docker inspect "$c" >/dev/null 2>&1; then
        echo "--- the container's last 80 log lines:" >&2
        docker logs --tail 80 "$c" >&2 2>&1 || true
    fi
    exit 1
}

cat > "$work/override.yaml" <<EOF
services:
  otc:
    image: $image
    container_name: $c
EOF
files=(-f "$root/docker/compose.yaml")
if $mirror; then
    files+=(-f "$root/docker/compose.mirror.yaml")
fi
files+=(-f "$work/override.yaml")
compose() {
    OTC_DATA="$data" OTC_MIRROR_DISK1="${disk1:-/dev/null}" OTC_MIRROR_DISK2="${disk2:-/dev/null}" OTC_MIRROR_ERASE='' \
        docker compose -p "$project" "${files[@]}" "$@"
}

# A root shell on the Docker host's disks, in a container of <image>:
# privileged, with the host's /dev (a loop device made after a container
# started has no node in that container's own) and the data dir's parent
# at /parent.
host_sh() {
    docker run --rm --privileged --network none -v /dev:/dev -v "$parent:/parent" --entrypoint sh "$image" -c "$@"
}

# The mirror's disks gone: any array on them stopped, their loop devices
# detached (found by their files, so only these) and the files removed.
drop_disks() {
    # shellcheck disable=SC2016 # the helper's sh expands them
    host_sh '
        for f in "$@"; do
            [ -e "$f" ] || continue
            for try in 1 2 3; do
                loops=$(losetup -j "$f" | cut -d: -f1)
                [ -n "$loops" ] || break
                for l in $loops; do
                    for h in /sys/block/"${l#/dev/}"/holders/*; do
                        [ -e "$h" ] && mdadm --stop "/dev/${h##*/}"
                    done
                    losetup -d "$l"
                done
                sleep "$try"
            done
            rm -f "$f"
        done' sh "${disk_files[@]}"
}

cleanup() {
    compose down --timeout 120 >/dev/null 2>&1 || true
    docker rm -f "$c" "$c-client" >/dev/null 2>&1 || true
    if $mirror; then
        drop_disks >/dev/null 2>&1 || true
    fi
    # Made by the Docker daemon as root, so removed through a container.
    docker run --rm -v "$parent:/parent" --entrypoint rm "$image" -rf "/parent/$name" >/dev/null 2>&1 || true
    rm -rf "$work"
}

# The check refuses to reuse a data dir (what is in it isn't a fresh device)
# or, with --mirror, the disks' files. Nothing is removed until it passed.
names=("$name")
if $mirror; then
    names+=("$name.disk1" "$name.disk2")
fi
taken=$(docker run --rm -v "$parent:/parent" --entrypoint sh "$image" \
    -c 'for f in "$@"; do [ ! -e "/parent/$f" ] || echo "$f"; done' sh "${names[@]}") \
    || { rm -rf "$work"; echo "FAIL: could not run $image on the Docker host" >&2; exit 1; }
if [ -n "$taken" ]; then
    rm -rf "$work"
    while IFS= read -r f; do echo "FAIL: $parent/$f already exists on the Docker host" >&2; done <<< "$taken"
    exit 1
fi
trap cleanup EXIT

wait_healthy() {
    local h
    for _ in $(seq 1 300); do
        h=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$c" 2>/dev/null || true)
        [ "$h" = healthy ] && return 0
        [ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = true ] || fail "the container stopped ($1)"
        sleep 5
    done
    fail "not healthy within 25 minutes ($1)"
}

sign_in() {
    docker rm -f "$c-client" >/dev/null 2>&1 || true
    docker create --network host --name "$c-client" --entrypoint /otc-smoke "$image" \
        -addr "127.0.0.1:$port" -password-file /pw -release "$release" >/dev/null
    docker cp -q "$smoke" "$c-client:/otc-smoke"
    docker cp -q "$work/pw" "$c-client:/pw"
    docker start -a "$c-client" || fail "signing in ($1)"
    docker rm "$c-client" >/dev/null
}

# The container's /mnt/storage is the mirror of the two loop disks (what a
# program opening it gets, and findmnt's ext4 on it), and /proc/mdstat shows
# that array as raid1 on both, whole ([2/2] [UU], a rebuild included).
check_mirror() {
    local majmin md
    majmin=$(docker exec "$c" mountpoint -d /mnt/storage) || fail "/mnt/storage is not mounted ($1)"
    [ "${majmin%%:*}" = 9 ] || fail "/mnt/storage is device $majmin, not an md array ($1)"
    docker exec "$c" findmnt -l -n -o MAJ:MIN,FSTYPE -M /mnt/storage \
        | awk -v m="$majmin" '$1 == m && $2 == "ext4" { f = 1 } END { exit !f }' \
        || fail "findmnt shows no ext4 of $majmin on /mnt/storage ($1)"
    md=$(docker exec "$c" readlink -f "/sys/dev/block/$majmin") || fail "no block device $majmin ($1)"
    md=${md##*/}
    docker exec "$c" cat /proc/mdstat > "$work/mdstat" || fail "could not read /proc/mdstat ($1)"
    awk -v md="$md" -v a="${disk1#/dev/}" -v b="${disk2#/dev/}" '
        $1 == md && $2 == ":" {
            f = 1
            for (i = 3; i <= NF; i++) {
                n = $i; sub(/\[.*/, "", n)
                if ($i == "raid1") r = 1
                if (n == a) ha = 1
                if (n == b) hb = 1
            }
            next
        }
        f { whole = index($0, "[2/2] [UU]") > 0; exit }
        END { exit !(r && ha && hb && whole) }' "$work/mdstat" \
        || fail "/proc/mdstat doesn't show $md as a whole mirror of $disk1 and $disk2 ($1):
$(cat "$work/mdstat")"
    echo "/mnt/storage is $md: $(grep -A 1 "^$md :" "$work/mdstat" | tr -s ' \n' ' ')"
}

no_panic() {
    if docker logs "$c" 2>&1 | grep -E '^(panic: |fatal error: )'; then
        fail "the log has a Go panic ($1)"
    fi
    if $mirror && docker logs "$c" 2>&1 | grep -F "the disk mirror's watcher stopped"; then
        fail "the mirror's watcher stopped ($1)"
    fi
}

if $mirror; then
    step "two blank 3 GiB loop disks on the Docker host"
    # shellcheck disable=SC2016 # the helper's sh expands them
    loops=$(host_sh '
        losetup -f >/dev/null 2>&1 || { echo "the Docker host has no free loop device" >&2; exit 1; }
        truncate -s 3G "$1" "$2" && losetup -f --show "$1" && losetup -f --show "$2"' sh "${disk_files[@]}") \
        || fail "could not make the loop disks"
    disk1=$(sed -n 1p <<< "$loops")
    disk2=$(sed -n 2p <<< "$loops")
    [[ "$disk1" =~ ^/dev/loop[0-9]+$ && "$disk2" =~ ^/dev/loop[0-9]+$ ]] || fail "the loop disks are '$loops'"
    echo "$disk1 ($parent/$name.disk1) and $disk2 ($parent/$name.disk2)"
fi

step "starting $image"
compose up -d --quiet-pull
wait_healthy "first start"

if $mirror; then
    step "mirror"
    docker logs "$c" 2>&1 | grep -F '[otc-mirror]' || true
    check_mirror "first start"
fi

step "release"
got=$(docker exec "$c" cat /etc/otc/version)
[ "$got" = "$release" ] || fail "/etc/otc/version is '$got', not $release"

step "web app"
docker exec "$c" curl -fsS "http://127.0.0.1:$port/" | grep -q '<div id="root">' || fail "the web app isn't served on :$port"

step "owner password"
head -c 24 /dev/urandom | base64 | tr -d '/+=\n' > "$work/pw"
docker exec -i "$c" otc-entrypoint set-password < "$work/pw" || fail "set-password"

step "sign-in"
sign_in "first start"

step "restart"
compose restart --timeout 120 >/dev/null
sleep 5
wait_healthy "after a restart"
if $mirror; then
    check_mirror "after a restart"
fi
sign_in "after a restart"

if $mirror; then
    step "docker compose down and up -d"
    no_panic "before docker compose down"
    compose down --timeout 120 >/dev/null
    held=$(docker run --rm --network none --entrypoint sh "$image" \
        -c 'ls /sys/block/"$1"/holders /sys/block/"$2"/holders' sh "${disk1#/dev/}" "${disk2#/dev/}" \
        | grep -E '^md' | sort -u | tr '\n' ' ' || true)
    if [ -z "$held" ]; then
        echo "the container stopped the array as it went"
    else
        echo "the array is still active after the container went (${held% }): the next start adopts it"
    fi
    compose up -d --quiet-pull
    wait_healthy "after docker compose down and up -d"
    check_mirror "after docker compose down and up -d"
    sign_in "after docker compose down and up -d"

    step "the data dir's storage/"
    left=$(docker run --rm --network none -v "$data/storage:/s:ro" --entrypoint ls "$image" -A /s) \
        || fail "could not list $data/storage"
    [ -z "$left" ] || fail "$data/storage on the Docker host isn't empty (the device wrote beside the mirror): $(tr '\n' ' ' <<< "$left")"
fi

step "log"
no_panic "at the end"

if $mirror; then
    echo "PASS: $image (release $release), on two mirrored disks"
else
    echo "PASS: $image (release $release)"
fi
