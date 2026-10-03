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
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
BUMP=minor
[ "${1:-}" = "--patch" ] && { BUMP=patch; shift; }
NOTES="${1:?usage: scripts/desktop-release.sh [--patch] \"what changed\"}"
REL=desktop
REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
BASE="https://github.com/$REPO/releases/download/$REL"

[ -z "$(git status --porcelain --untracked-files=no -- app/desktop proto)" ] || { echo "commit app/desktop and proto first"; exit 1; }
cur="$(cat app/desktop/VERSION)"
IFS=. read -r maj min pat <<<"$cur"
if [ "$BUMP" = patch ]; then VERSION="$maj.$min.$((pat + 1))"; else VERSION="$maj.$((min + 1)).0"; fi
echo "otc-sync $cur -> $VERSION"

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
LDF="-s -w -X main.version=$VERSION"
# The download name of each platform (bash 3.2 on macOS: no associative arrays).
binname() { case "$1" in windows-*) echo "otc-sync-$1.exe" ;; *) echo "otc-sync-$1" ;; esac; }
PLATS="linux-amd64 linux-arm64 windows-amd64 windows-arm64"
files=""
for plat in $PLATS; do
    os="${plat%-*}"; arch="${plat#*-}"; name="$(binname "$plat")"
    extra=""; [ "$os" = windows ] && extra="-H windowsgui"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$LDF $extra" -o "$work/$name" ./app/desktop/cmd/otc-sync
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
git add app/desktop/VERSION && git commit -q -m "Desktop release $VERSION: $NOTES" && git push -q
echo "otc-sync $VERSION published: $BASE/desktop.json"
