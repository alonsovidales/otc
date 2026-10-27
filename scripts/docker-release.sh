#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The Docker image (beta, docs/docker.md) for a release, built, tested and
# published from this Mac:
#
#   scripts/docker-release.sh [--no-publish] [--arch amd64|arm64] [<release>]
#
# Run it after scripts/release.sh; without a release number it takes the
# newest. It builds that release's image from the release's own signed
# archives (src.tar.gz and web-dist.tar.gz, checked against the signed
# manifest), never from this checkout, for linux/arm64 (natively) and
# linux/amd64 (under Rosetta). It tests each one with
# docker/smoke/test-image.sh: it starts, serves the web app, signs in and
# restarts; then again on two mirrored disks (--mirror, two loop devices in
# the VM): the container builds the mirror, mounts it and assembles it again
# once it is recreated. Only once both architectures pass both tests does it
# publish them as ghcr.io/alonsovidales/otc:<release>, moving :beta and
# :latest to it when it is the newest release. A release from before the disk
# mirror (no docker/mirror in its source) is refused, since its image can't
# pass the second test. A failure publishes nothing; the Pi release is never
# affected. --no-publish only builds and tests; --arch builds and tests one
# architecture and never publishes. A release already published is not built
# again (only :beta and :latest are put right, if a run stopped before moving
# them).
#
# Everything runs in a Colima VM of its own (profile otc-image: 8 CPUs,
# 12 GB, Rosetta for amd64) with its own Docker configuration in
# ~/.otc/image-build/docker: the default Colima VM, its containers and
# ~/.docker are never touched. The VM is started when needed and stopped at the
# end if this started it. Its build cache (the toolchain and OpenCV,
# docker/base.Dockerfile) stays on its disk for the next release; a cold build
# of both architectures takes about an hour.
#
# Publishing signs in to ghcr.io with gh's token, which needs the
# write:packages scope (once: gh auth refresh -h github.com -s write:packages),
# and signs out again at the end. It needs this checkout's manifest to be
# main's (so :beta and :latest never move back). The build file, the test and
# the sign-in client come from this checkout's HEAD (git archive), never from
# uncommitted files.
set -euo pipefail

IMAGE=ghcr.io/alonsovidales/otc
REPO=alonsovidales/otc
PROFILE=otc-image
BUILDER=otc-image
HOME_DIR="$HOME/.otc/image-build"
export DOCKER_CONFIG="$HOME_DIR/docker"
export DOCKER_HOST="unix://$HOME/.colima/$PROFILE/docker.sock"

die() { echo "docker-release: $*" >&2; exit 1; }
usage() { die "usage: scripts/docker-release.sh [--no-publish] [--arch amd64|arm64] [<release>]"; }
step() { echo "==> $*"; }

publish=true
arches="arm64 amd64"
release=""
while [ $# -gt 0 ]; do
    case "$1" in
        --no-publish) publish=false ;;
        --arch)
            [ $# -ge 2 ] || usage
            case "$2" in amd64|arm64) arches=$2; publish=false ;; *) usage ;; esac
            shift ;;
        -*) usage ;;
        *) [ -z "$release" ] || usage; release=$1 ;;
    esac
    shift
done

for t in colima docker gh git jq shasum; do
    command -v "$t" >/dev/null || die "needs $t"
done
OPENSSL="$(command -v /opt/homebrew/bin/openssl || command -v openssl)"
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

# The Docker configuration of its own, with the CLI's plugins (docker only
# looks for them under DOCKER_CONFIG and a few system directories).
mkdir -p "$DOCKER_CONFIG/cli-plugins"
chmod 700 "$HOME_DIR" "$DOCKER_CONFIG"
[ -f "$DOCKER_CONFIG/config.json" ] || echo '{}' > "$DOCKER_CONFIG/config.json"
for p in docker-buildx docker-compose; do
    [ -x "$DOCKER_CONFIG/cli-plugins/$p" ] && continue
    for d in "$HOME/.docker/cli-plugins" /opt/homebrew/lib/docker/cli-plugins /usr/local/lib/docker/cli-plugins; do
        if [ -x "$d/$p" ]; then ln -sf "$d/$p" "$DOCKER_CONFIG/cli-plugins/$p"; break; fi
    done
done
docker buildx version >/dev/null 2>&1 || die "needs docker buildx (brew install docker-buildx)"
docker compose version >/dev/null 2>&1 || die "needs docker compose (brew install docker-compose)"

# One run at a time: they share the VM, the builder and the test's ports.
lock="$HOME_DIR/run.lock"
mkdir "$lock" 2>/dev/null || die "another run holds $lock (remove it if none is running)"

work=""
started_vm=false
test_images=""
cleanup() {
    status=$?
    set +e
    docker logout ghcr.io >/dev/null 2>&1
    for i in $test_images; do docker rmi "$i" >/dev/null 2>&1; done
    if $started_vm; then
        step "stopping the build VM"
        colima stop "$PROFILE" >/dev/null 2>&1
    fi
    case "$work" in */otc-image.??????) rm -rf "$work" ;; esac
    rmdir "$lock"
    exit "$status"
}
trap cleanup EXIT
# Ctrl-C stops the run: docker exits normally when interrupted, which would
# otherwise read as a failed try and start the next one.
trap 'exit 130' INT
trap 'exit 143' TERM
tmp=$(mktemp -d "${TMPDIR:-/tmp}/otc-image.XXXXXX")
# Physical path: bake's file-access check compares resolved paths, and
# macOS's temporary directory is behind the /var -> /private/var link.
work=$(cd "$tmp" && pwd -P)
[ -n "$work" ] && [ "$work" != "$root" ] || die "no temporary directory"

step "checking the signed manifest"
git show HEAD:scripts/updates/VERSIONS > "$work/VERSIONS"
git show HEAD:scripts/updates/VERSIONS.sig | base64 -d > "$work/versions.sig"
git show HEAD:scripts/release-signing.pub > "$work/release-signing.pub"
"$OPENSSL" pkeyutl -verify -pubin -inkey "$work/release-signing.pub" -rawin \
    -in "$work/VERSIONS" -sigfile "$work/versions.sig" >/dev/null \
    || die "the manifest's signature doesn't verify"
if $publish; then
    # The newest release decides where :beta and :latest go: main's.
    git fetch -q origin main || die "git fetch origin main failed"
    git show origin/main:scripts/updates/VERSIONS | cmp -s - "$work/VERSIONS" \
        || die "this checkout's manifest isn't main's: git pull first"
fi
newest=$(tail -n 1 "$work/VERSIONS" | cut -f 1)
release=${release:-$newest}
case "$release" in ''|*[!0-9]*) die "the release must be a number, not '$release'" ;; esac
line=$(awk -F '\t' -v n="$release" '$1 == n' "$work/VERSIONS")
[ -n "$line" ] || die "release $release is not in the manifest"
web_sha=$(printf '%s\n' "$line" | cut -f 3)
src_sha=$(printf '%s\n' "$line" | cut -f 5)
echo "release $release (newest $newest), publish $publish, $arches"

if $publish; then
    scopes=$(gh api -i user 2>/dev/null | tr -d '\r' | awk -F ': ' 'tolower($1) == "x-oauth-scopes" {print $2}') \
        || die "gh can't reach GitHub (gh auth status)"
    case ", $scopes," in
        *", write:packages,"*) ;;
        *) die "gh's token can't publish packages: run  gh auth refresh -h github.com -s write:packages  once" ;;
    esac
fi

step "downloading the release's archives"
mkdir -p "$work/release" "$work/src" "$work/web" "$work/tools" "$work/smoke-src/proto" "$work/smoke-src/docker"
gh release download "v$release" -R "$REPO" -p src.tar.gz -p web-dist.tar.gz -D "$work/release"
echo "$src_sha  $work/release/src.tar.gz" | shasum -a 256 -c - >/dev/null || die "src.tar.gz doesn't match the manifest"
echo "$web_sha  $work/release/web-dist.tar.gz" | shasum -a 256 -c - >/dev/null || die "web-dist.tar.gz doesn't match the manifest"
# The image's revision label: the commit git archive recorded in the
# (verified) source archive.
# (git stops reading after the first header: gzip's broken pipe is expected.)
revision=$(gzip -dc "$work/release/src.tar.gz" 2>/dev/null | git get-tar-commit-id) || true
[ -n "$revision" ] || die "src.tar.gz names no commit"
tar -xzf "$work/release/src.tar.gz" -C "$work/src" --strip-components=1
tar -xzf "$work/release/web-dist.tar.gz" -C "$work/web"
[ -f "$work/src/docker/Dockerfile" ] || die "release $release has no docker/Dockerfile"
[ -d "$work/src/docker/mirror" ] || die "release $release predates the disk mirror (docker/mirror): its image can't pass the mirror test"
# The image's build context is an allowlist (docker/Dockerfile.dockerignore):
# a package the device's build needs and the list leaves out fails the
# build itself, far into it (release 143's diskspace and storagewatch).
# proto is generated inside the image.
missing=$(cd "$work/src" && { go list -e -deps ./bin/otc.go; go list -e -deps ./docker/mirror; } 2>/dev/null \
    | sed -n 's#^github.com/alonsovidales/otc/\([^/]*\).*#\1#p' | sort -u \
    | while read -r d; do [ "$d" = proto ] || grep -qx "!$d/" docker/Dockerfile.dockerignore || echo "$d"; done | tr '\n' ' ')
[ -z "$missing" ] || die "docker/Dockerfile.dockerignore leaves out packages the device needs: $missing"
# The build file, the test and the sign-in client, as committed.
git archive HEAD docker/docker-bake.hcl docker/compose.yaml docker/compose.mirror.yaml docker/smoke go.mod go.sum proto/messages.proto \
    | tar -x -C "$work/tools"
# The sign-in client's sources alone (docker/docker-bake.hcl, smoke).
cp "$work/tools/go.mod" "$work/tools/go.sum" "$work/smoke-src/"
cp "$work/tools/proto/messages.proto" "$work/smoke-src/proto/"
cp -R "$work/tools/docker/smoke" "$work/smoke-src/docker/"
# git archive dates every file at the tag's commit: the image's timestamps
# follow it, the same in the tested and the pushed image (whose layers are
# checked against each other below).
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(stat -f %m "$work/src/go.mod")
export SRC="$work/src" WEB="$work/web" SMOKE_SRC="$work/smoke-src"
export OTC_RELEASE="$release" OTC_REVISION="$revision"
echo "tag commit $revision"

if ! colima status -p "$PROFILE" >/dev/null 2>&1; then
    step "starting the build VM"
    started_vm=true
    colima start "$PROFILE" --vm-type vz --vz-rosetta --arch aarch64 \
        --cpu 8 --memory 12 --disk 80 --runtime docker >/dev/null
fi
docker info >/dev/null 2>&1 || die "the build VM's Docker doesn't answer"
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
    # A builder of its own (as CI's), with room for both architectures' cache.
    cat > "$work/buildkitd.toml" <<'EOF'
[worker.oci]
  gc = true
  reservedSpace = "30GB"
  maxUsedSpace = "50GB"
  minFreeSpace = "10GB"
EOF
    docker buildx create --name "$BUILDER" --driver docker-container \
        --buildkitd-config "$work/buildkitd.toml" >/dev/null
fi
docker buildx inspect --bootstrap "$BUILDER" >/dev/null

index_digest() {
    docker buildx imagetools inspect "$IMAGE:$1" --format '{{json .Manifest}}' 2>/dev/null | jq -r '.digest // empty'
}
# The release a tag points at, from its version annotation (empty: none).
tag_release() {
    docker buildx imagetools inspect "$IMAGE:$1" --format '{{json .Manifest.Annotations}}' 2>/dev/null \
        | jq -r '."org.opencontainers.image.version" // empty' 2>/dev/null || true
}
# :beta and :latest move to the newest release only, never back to an older
# one (a run of an older release, or main's newest published late).
moves_tags() {
    [ "$release" = "$newest" ] || return 1
    local at
    at=$(tag_release beta)
    case "$at" in ''|*[!0-9]*) return 0 ;; esac
    [ "$at" -le "$release" ]
}
annotations() {
    printf '%s\n' \
        "--annotation" "index:org.opencontainers.image.title=Off The Cloud (beta)" \
        "--annotation" "index:org.opencontainers.image.description=Off The Cloud on a home server: a private family social network and home NAS for photos and files. Beta; on a Raspberry Pi, use the Off The Cloud image." \
        "--annotation" "index:org.opencontainers.image.source=https://github.com/$REPO" \
        "--annotation" "index:org.opencontainers.image.licenses=AGPL-3.0-or-later" \
        "--annotation" "index:org.opencontainers.image.version=$release"
}

ann=()
while IFS= read -r a; do ann+=("$a"); done < <(annotations)

if $publish; then
    gh auth token | docker login ghcr.io -u "$(gh api user -q .login)" --password-stdin >/dev/null
    published=$(index_digest "$release")
    if [ -n "$published" ]; then
        if moves_tags && { [ "$(index_digest beta)" != "$published" ] || [ "$(index_digest latest)" != "$published" ]; }; then
            step "$IMAGE:$release is published; moving :beta and :latest to it"
            docker buildx imagetools create -t "$IMAGE:beta" -t "$IMAGE:latest" "${ann[@]}" "$IMAGE:$release"
        else
            echo "$IMAGE:$release is already published: nothing to do"
        fi
        exit 0
    fi
fi

# clear_mirror_test <image> <data dir>: what a run stopped mid-test leaves
# on the VM's disk for the mirror test - its data dir and its two disks, their
# array stopped and their loop devices detached first (test-image.sh refuses
# to start over them).
clear_mirror_test() {
    # shellcheck disable=SC2016 # the container's sh expands them
    docker run --rm --privileged --network none -v /dev:/dev -v /srv:/parent --entrypoint sh "$1" -c '
        for f in "$1.disk1" "$1.disk2"; do
            [ -e "$f" ] || continue
            for l in $(losetup -j "$f" | cut -d: -f1); do
                for h in /sys/block/"${l#/dev/}"/holders/*; do
                    [ -e "$h" ] && mdadm --stop "/dev/${h##*/}"
                done
                losetup -d "$l"
            done
            rm -f "$f"
        done
        rm -rf "$1"' sh "/parent/${2#/srv/}" >/dev/null
}

bake() {
    docker buildx bake --builder "$BUILDER" -f "$work/tools/docker/docker-bake.hcl" \
        --allow "fs.read=$work" --allow "fs.write=$work" \
        --set "*.platform=linux/$arch" "$@"
}

digests=""
for arch in $arches; do
    tested="otc-test:$release-$arch"
    step "building $tested"
    test_images="$test_images $tested"
    # A download the build makes (Go, ONNX Runtime, protoc, Debian's packages)
    # can fail for a while upstream: up to three tries, a minute apart. What
    # already built is kept, so a try only redoes what failed.
    built=false
    for try in 1 2 3; do
        if bake otc smoke \
            --set "otc.output=type=docker,name=$tested" \
            --set "smoke.output=type=local,dest=$work/smoke-$arch"; then
            built=true; break
        fi
        [ "$try" = 3 ] || { echo "build try $try failed, trying again in a minute"; sleep 60; }
    done
    $built || die "building $tested failed"

    step "testing $tested"
    # The test refuses a container or data dir that already exists: a run
    # stopped mid-test leaves both on the VM's disk.
    data="/srv/otc-image-test-$arch"
    docker rm -f otc-image-test otc-image-test-client >/dev/null 2>&1 || true
    docker run --rm -v /srv:/parent --entrypoint rm "$tested" -rf "/parent/${data#/srv/}" >/dev/null
    "$work/tools/docker/smoke/test-image.sh" "$tested" "$release" "$work/smoke-$arch/otc-smoke" "$data" \
        || die "$tested failed its test: nothing was published"

    step "testing $tested on two mirrored disks"
    data="/srv/otc-image-test-mirror-$arch"
    docker rm -f otc-image-test otc-image-test-client >/dev/null 2>&1 || true
    clear_mirror_test "$tested" "$data"
    "$work/tools/docker/smoke/test-image.sh" --mirror "$tested" "$release" "$work/smoke-$arch/otc-smoke" "$data" \
        || die "$tested failed its mirror test: nothing was published"

    $publish || continue
    step "pushing the tested image for linux/$arch (by digest, untagged)"
    bake otc --set "otc.output=type=image,name=$IMAGE,push-by-digest=true,name-canonical=true,push=true" \
        --metadata-file "$work/meta-$arch.json" >/dev/null
    digest=$(jq -r '.otc."containerimage.digest"' "$work/meta-$arch.json")
    case "$digest" in sha256:*) ;; *) die "no digest in the build's metadata" ;; esac
    # The same layers as the image the test ran.
    want=$(docker image inspect -f '{{json .RootFS.Layers}}' "$tested" | jq -c .)
    got=$(docker buildx imagetools inspect "$IMAGE@$digest" --format '{{json .Image}}' \
        | jq -c --arg p "linux/$arch" 'if has("rootfs") then .rootfs.diff_ids else .[$p].rootfs.diff_ids end')
    [ "$want" = "$got" ] || die "the pushed image's layers differ from the tested image's: tested $want, pushed $got"
    echo "pushed $IMAGE@$digest (linux/$arch), the tested image"
    digests="$digests $IMAGE@$digest"
done

if ! $publish; then
    echo "PASS: release $release built and tested ($arches); nothing published"
    exit 0
fi

step "tagging both architectures as release $release"
tags="-t $IMAGE:$release"
moved=""
if moves_tags; then
    tags="$tags -t $IMAGE:beta -t $IMAGE:latest"
    moved=", and :beta and :latest with it"
fi
# shellcheck disable=SC2086 # $tags and $digests are word lists without spaces inside
docker buildx imagetools create $tags "${ann[@]}" $digests
docker buildx imagetools inspect "$IMAGE:$release"
# Every tag on the same index, or say which isn't.
want=$(index_digest "$release")
[ -n "$want" ] || die "$IMAGE:$release doesn't resolve after tagging"
if [ -n "$moved" ]; then
    for t in beta latest; do
        [ "$(index_digest "$t")" = "$want" ] || die ":$t isn't on $want: run this again to move it"
    done
fi
echo "Published $IMAGE:$release ($want)$moved."
