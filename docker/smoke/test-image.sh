#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The tests an image passes before it is published (.github/workflows/docker.yml),
# on any Linux Docker host:
#
#   docker/smoke/test-image.sh <image> <release> <otc-smoke binary> [data dir]
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
# The data dir (default /srv/otc-image-test, on the Docker host) must not
# exist yet. The container and the data dir are removed at the end, whatever
# the outcome. The ports are compose's defaults (OTC_HTTP_PORT and the
# others override them), so they must be free.
set -euo pipefail

image=${1:?usage: test-image.sh <image> <release> <otc-smoke binary> [data dir]}
release=${2:?release number}
smoke=${3:?otc-smoke binary}
data=${4:-/srv/otc-image-test}
port=${OTC_HTTP_PORT:-8080}
root=$(cd "$(dirname "$0")/../.." && pwd)
project=otc-image-test
c=otc-image-test
work=$(mktemp -d)

step() { echo "==> $*"; }
fail() {
    echo "FAIL: $*" >&2
    echo "--- the container's last 80 log lines:" >&2
    docker logs --tail 80 "$c" >&2 2>&1 || true
    exit 1
}

cat > "$work/override.yaml" <<EOF
services:
  otc:
    image: $image
    container_name: $c
EOF
compose() { OTC_DATA="$data" docker compose -p "$project" -f "$root/docker/compose.yaml" -f "$work/override.yaml" "$@"; }

cleanup() {
    compose down --timeout 120 >/dev/null 2>&1 || true
    docker rm -f "$c-client" >/dev/null 2>&1 || true
    # Made by the Docker daemon as root, so removed through a container.
    docker run --rm -v "$(dirname "$data"):/parent" --entrypoint rm "$image" -rf "/parent/$(basename "$data")" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT

# The check refuses to reuse a data dir: what is in it isn't a fresh device.
if docker run --rm -v "$(dirname "$data"):/parent" --entrypoint test "$image" -e "/parent/$(basename "$data")"; then
    trap - EXIT
    rm -rf "$work"
    echo "FAIL: $data already exists on the Docker host" >&2
    exit 1
fi

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

step "starting $image"
compose up -d --quiet-pull
wait_healthy "first start"

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
sign_in "after a restart"

step "log"
if docker logs "$c" 2>&1 | grep -E '^(panic: |fatal error: )'; then
    fail "the log has a Go panic"
fi

echo "PASS: $image (release $release)"
