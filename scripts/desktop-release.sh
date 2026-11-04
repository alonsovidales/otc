#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Publishes the desktop apps (website downloads + self-update):
#
#   scripts/desktop-release.sh "What changed"          # 1.2.0 -> 1.3.0
#   scripts/desktop-release.sh --patch "What changed"  # 1.2.0 -> 1.2.1
#
# Builds otc-sync for Linux and Windows (amd64, arm64) with the new version
# (app/desktop/VERSION), writes desktop.json - version, notes and, per
# platform, the download URL and SHA-256 - signs it with the release key
# (scripts/sign-file.sh) and uploads everything to the "desktop" GitHub
# release, whose download URLs never change: the website links to them and
# the apps read desktop.json + desktop.json.sig from there (selfupdate).
# Like a device release (scripts/release.sh), from a clean, pushed main and
# nothing else: the binaries every otc-sync installs as its own update are
# built from a `git archive` of HEAD - never this checkout's uncommitted,
# untracked or ignored files - with the protobuf code generated there from
# HEAD's proto by the protoc-gen-go go.mod pins (devices generate it the
# same way), and `make i18n-check` must pass on that tree in release mode.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
BUMP=minor
[ "${1:-}" = "--patch" ] && { BUMP=patch; shift; }
NOTES="${1:?usage: scripts/desktop-release.sh [--patch] \"what changed\"}"
REL=desktop

[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "commit (or stash) your changes first"; git status --short --untracked-files=no; exit 1; }
[ "$(git symbolic-ref --short -q HEAD || true)" = main ] || { echo "check out main first"; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "HEAD is not origin/main - pull or push first"; exit 1; }
command -v protoc >/dev/null || { echo "protoc is missing (brew install protobuf)"; exit 1; }

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
src="$work/src"
mkdir "$src"
git archive --format=tar HEAD | tar -xf - -C "$src"
pgg="$(awk '/google.golang.org\/protobuf /{print $2}' "$src/go.mod")"
[ -n "$pgg" ] || { echo "couldn't find google.golang.org/protobuf's version in go.mod"; exit 1; }
GOBIN="$work/bin" go install "google.golang.org/protobuf/cmd/protoc-gen-go@$pgg"
mkdir -p "$src/proto/generated"
PATH="$work/bin:$PATH" protoc -I="$src/proto" --go_out="$src/proto/generated" --go_opt=paths=source_relative "$src/proto/messages.proto"

# otc-sync embeds the translations (i18n/catalog), so they are committed
# like its code, and nothing is built or published until `make i18n-check`
# passes in release mode (docs/i18n.md) on the tree the binaries are built
# from. Its web scanner reads this checkout's web/node_modules (nothing of
# it is in the binaries).
ln -s "$PWD/web/node_modules" "$src/web/node_modules"
make -C "$src" --no-print-directory i18n-check I18N_RELEASE=1 \
    || { echo "make i18n-check failed - nothing was built or published"; exit 1; }
REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
BASE="https://github.com/$REPO/releases/download/$REL"
cur="$(cat app/desktop/VERSION)"
IFS=. read -r maj min pat <<<"$cur"
if [ "$BUMP" = patch ]; then VERSION="$maj.$min.$((pat + 1))"; else VERSION="$maj.$((min + 1)).0"; fi
echo "otc-sync $cur -> $VERSION"

LDF="-s -w -X main.version=$VERSION"
# The download name of each platform (bash 3.2 on macOS: no associative arrays).
binname() { case "$1" in windows-*) echo "otc-sync-$1.exe" ;; *) echo "otc-sync-$1" ;; esac; }
PLATS="linux-amd64 linux-arm64 windows-amd64 windows-arm64"
files=""
for plat in $PLATS; do
    os="${plat%-*}"; arch="${plat#*-}"; name="$(binname "$plat")"
    extra=""; [ "$os" = windows ] && extra="-H windowsgui"
    (cd "$src" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$LDF $extra" -o "$work/$name" ./app/desktop/cmd/otc-sync)
    sha="$(shasum -a 256 "$work/$name" | awk '{print $1}')"
    files="$files${files:+,}\"$plat\":{\"url\":\"$BASE/$name\",\"sha256\":\"$sha\"}"
done
printf '{"version":"%s","notes":%s,"files":{%s}}\n' "$VERSION" "$(printf '%s' "$NOTES" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" "$files" > "$work/desktop.json"
python3 -m json.tool "$work/desktop.json" >/dev/null
scripts/sign-file.sh "$work/desktop.json" "$work/desktop.json.sig"

gh release view "$REL" >/dev/null 2>&1 || gh release create "$REL" --title "Desktop apps" --notes "Off The Cloud for Windows, Linux and macOS: the downloads on off-the.cloud. The apps update themselves from here."
# Binaries first, the manifest last: an app never reads a manifest naming
# files that aren't up yet.
gh release upload "$REL" "$work"/otc-sync-* --clobber
gh release upload "$REL" "$work/desktop.json" "$work/desktop.json.sig" --clobber
for name in $(for p in $PLATS; do binname "$p"; done) desktop.json; do
    want="$(shasum -a 256 "$work/$name" | awk '{print $1}')"
    got="$(curl -fsSL "$BASE/$name" | shasum -a 256 | awk '{print $1}')"
    [ "$want" = "$got" ] || { echo "published $name does not match"; exit 1; }
done
echo "$VERSION" > app/desktop/VERSION
git add app/desktop/VERSION && git commit -q -m "Desktop release $VERSION: $NOTES" && git push -q origin main
echo "otc-sync $VERSION published: $BASE/desktop.json"
