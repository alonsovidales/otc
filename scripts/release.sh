#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Cuts a device release (issue #160: signed releases), from a clean, pushed
# main:
#
#   scripts/release.sh "Summary shown in Settings"
#
# If scripts/updates/<N>.sh exists (N = the manifest's last version + 1) it
# is the release's migration script. In order:
#   1. builds the web bundle (web-dist.tar.gz) and tags HEAD as vN;
#   2. makes the release's own source archive (src.tar.gz: `git archive` of
#      the tag, gzip -n - reproducible), which devices build from instead of
#      GitHub's generated archive. The manifest and its signature are
#      export-ignore'd (.gitattributes): they carry the archive's hash, so
#      they can't be inside it;
#   3. appends the manifest line - version, script sha, web sha, summary,
#      source sha - and signs the whole manifest with the release key
#      (Ed25519; ~/.otc/otc-release-signing.pem, its passphrase in the
#      Keychain item otc-release-signing) into scripts/updates/VERSIONS.sig;
#   4. commits and pushes the manifest, then publishes the GitHub release
#      with both archives and checks what was published.
# A device verifies the signature with the public key it pins
# (/etc/otc/release-signing.pub, = scripts/release-signing.pub) before it
# runs anything (otc-update-runner.sh).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
SUMMARY="${1:?usage: scripts/release.sh \"summary\"}"
case "$SUMMARY" in *$'\t'*|*$'\n'*) echo "the summary can't contain tabs or newlines"; exit 1 ;; esac
KEY="${OTC_RELEASE_KEY:-$HOME/.otc/otc-release-signing.pem}"
MANIFEST=scripts/updates/VERSIONS
SIG=scripts/updates/VERSIONS.sig
OPENSSL="$(command -v /opt/homebrew/bin/openssl || command -v openssl)"

[ -f "$KEY" ] || { echo "no release signing key at $KEY"; exit 1; }
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "commit (or stash) your changes first"; git status --short --untracked-files=no; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "HEAD is not origin/main - pull or push first"; exit 1; }

last="$(grep -v '^#' "$MANIFEST" | awk -F'\t' 'NF{v=$1} END{print v}')"
N=$((last + 1))
if [ -f "scripts/updates/$N.sh" ]; then
    SCRIPT_SHA="$(shasum -a 256 "scripts/updates/$N.sh" | awk '{print $1}')"
else
    SCRIPT_SHA=-
fi
echo "release $N (migration script: $SCRIPT_SHA)"

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

npm run build --prefix web >/dev/null
tar -cf - -C web/dist . | gzip -n > "$work/web-dist.tar.gz"
WEB_SHA="$(shasum -a 256 "$work/web-dist.tar.gz" | awk '{print $1}')"

git tag "v$N"
git archive --format=tar --prefix="otc-$N/" "v$N" | gzip -n > "$work/src.tar.gz"
SRC_SHA="$(shasum -a 256 "$work/src.tar.gz" | awk '{print $1}')"
# The manifest must not be in the archive: it records the archive's hash.
if tar -tzf "$work/src.tar.gz" | grep -q "scripts/updates/VERSIONS"; then
    git tag -d "v$N" >/dev/null; echo "the source archive contains the manifest - check .gitattributes"; exit 1
fi

printf '%s\t%s\t%s\t%s\t%s\n' "$N" "$SCRIPT_SHA" "$WEB_SHA" "$SUMMARY" "$SRC_SHA" >> "$MANIFEST"
P="$(security find-generic-password -s otc-release-signing -a otc -w)" \
    "$OPENSSL" pkeyutl -sign -inkey "$KEY" -passin env:P -rawin -in "$MANIFEST" -out "$work/VERSIONS.sig.bin"
base64 < "$work/VERSIONS.sig.bin" | tr -d '\n' > "$SIG"; echo >> "$SIG"
# Signed with the private key, checked with the public one devices pin.
base64 -d < "$SIG" > "$work/check.sig"
"$OPENSSL" pkeyutl -verify -pubin -inkey scripts/release-signing.pub -rawin -in "$MANIFEST" -sigfile "$work/check.sig" >/dev/null

git push -q origin "v$N"
git add "$MANIFEST" "$SIG"
git commit -q -m "Release $N: $SUMMARY"
git push -q origin main

gh release create "v$N" "$work/web-dist.tar.gz" "$work/src.tar.gz" --title "v$N" --notes "$SUMMARY" >/dev/null
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
for f in web-dist.tar.gz src.tar.gz; do
    want="$(shasum -a 256 "$work/$f" | awk '{print $1}')"
    got="$(curl -fsSL "https://github.com/$repo/releases/download/v$N/$f" | shasum -a 256 | awk '{print $1}')"
    [ "$want" = "$got" ] || { echo "published $f does not match ($got, expected $want)"; exit 1; }
done
echo "release $N published and signed: web $WEB_SHA, source $SRC_SHA"
