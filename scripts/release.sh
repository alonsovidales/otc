#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Cuts a device release (issue #160: signed releases), from a clean, pushed
# main:
#
#   scripts/release.sh [--major|--critical] "Summary shown in Settings"
#
# Issue #183: a release is minor by default; --major for changes to
# security, stability or durability (devices notify their owners),
# --critical for one that breaks compatibility with the bridge or the apps
# if not installed, or a serious security fix (every app shows a banner).
# Its version label follows: a minor adds to the minor number (1.4 -> 1.5),
# a major or critical starts the next major (1.5 -> 2.0). Both go into
# scripts/updates/RELEASES, signed into RELEASES.sig.
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
#   4. publishes the GitHub release with both archives, checks what was
#      published, and only then commits and pushes the manifest (a failure
#      before that takes the release, the tag and the manifest lines back).
# A device verifies the signature with the public key it pins
# (/etc/otc/release-signing.pub, = scripts/release-signing.pub) before it
# runs anything (otc-update-runner.sh).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
KIND=minor
case "${1:-}" in
    --major) KIND=major; shift ;;
    --critical) KIND=critical; shift ;;
esac
SUMMARY="${1:?usage: scripts/release.sh [--major|--critical] \"summary\"}"
case "$SUMMARY" in *$'\t'*|*$'\n'*) echo "the summary can't contain tabs or newlines"; exit 1 ;; esac
KEY="${OTC_RELEASE_KEY:-$HOME/.otc/otc-release-signing.pem}"
MANIFEST=scripts/updates/VERSIONS
SIG=scripts/updates/VERSIONS.sig
KINDS=scripts/updates/RELEASES
KINDS_SIG=scripts/updates/RELEASES.sig
OPENSSL="$(command -v /opt/homebrew/bin/openssl || command -v openssl)"

[ -f "$KEY" ] || { echo "no release signing key at $KEY"; exit 1; }
# Read before anything changes, so a locked Keychain fails here and not
# half-way (after the tag). Over SSH: security unlock-keychain first.
KEY_PASS="$(security find-generic-password -s otc-release-signing -a otc -w 2>/dev/null)" \
    || { echo "can't read the signing key's passphrase from the Keychain (locked? run: security unlock-keychain)"; exit 1; }
[ -n "$KEY_PASS" ] || { echo "the Keychain item otc-release-signing is empty"; exit 1; }
export KEY_PASS
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
last_label="$(grep -v '^#' "$KINDS" | awk -F'\t' 'NF>=3{l=$3} END{print l}')"
lmaj="${last_label%%.*}"; lmin="${last_label#*.}"
if [ "$KIND" = minor ]; then LABEL="$lmaj.$((lmin + 1))"; else LABEL="$((lmaj + 1)).0"; fi
echo "release $N = version $LABEL ($KIND; migration script: $SCRIPT_SHA)"

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
# Every device downloads this on every update and stages it in RAM: about
# 11 MB once the screenshots and the bridge binary are export-ignore'd.
SRC_SIZE="$(wc -c < "$work/src.tar.gz" | tr -d ' ')"
if [ "$SRC_SIZE" -gt 30000000 ]; then
    git tag -d "v$N" >/dev/null
    echo "src.tar.gz is $SRC_SIZE bytes - a large file slipped in; export-ignore it in .gitattributes"; exit 1
fi

printf '%s\t%s\t%s\t%s\t%s\n' "$N" "$SCRIPT_SHA" "$WEB_SHA" "$SUMMARY" "$SRC_SHA" >> "$MANIFEST"
printf '%s\t%s\t%s\n' "$N" "$KIND" "$LABEL" >> "$KINDS"

# sign FILE OUT: the raw Ed25519 signature of FILE. The key is decrypted
# with `openssl pkcs8` and piped straight into the signer, never written;
# retried, since signing has been seen to fail now and then and work a
# moment later.
sign() {
    local attempt
    for attempt in 1 2 3 4 5; do
        if "$OPENSSL" pkcs8 -in "$KEY" -passin env:KEY_PASS 2>/dev/null \
            | "$OPENSSL" pkeyutl -sign -inkey /dev/stdin -rawin -in "$1" -out "$2" 2>/dev/null; then
            return 0
        fi
        sleep 2
    done
    return 1
}
if ! sign "$MANIFEST" "$work/VERSIONS.sig.bin" || ! sign "$KINDS" "$work/RELEASES.sig.bin"; then
    git checkout -- "$MANIFEST" "$KINDS"; git tag -d "v$N" >/dev/null
    echo "signing failed - the tag and the manifest lines were undone"; exit 1
fi
base64 < "$work/VERSIONS.sig.bin" | tr -d '\n' > "$SIG"; echo >> "$SIG"
base64 < "$work/RELEASES.sig.bin" | tr -d '\n' > "$KINDS_SIG"; echo >> "$KINDS_SIG"
# Signed with the private key, checked with the public one devices pin.
base64 -d < "$SIG" > "$work/check.sig"
"$OPENSSL" pkeyutl -verify -pubin -inkey scripts/release-signing.pub -rawin -in "$MANIFEST" -sigfile "$work/check.sig" >/dev/null
base64 -d < "$KINDS_SIG" > "$work/check2.sig"
"$OPENSSL" pkeyutl -verify -pubin -inkey scripts/release-signing.pub -rawin -in "$KINDS" -sigfile "$work/check2.sig" >/dev/null

# undo_release WHY: until the manifest is committed, a failure takes back
# the GitHub release, the tag (on GitHub and here) and the manifest lines -
# all four files are tracked and only modified so far.
undo_release() {
    gh release delete "v$N" --yes --cleanup-tag >/dev/null 2>&1 \
        || git push -q origin ":refs/tags/v$N" 2>/dev/null || true
    git tag -d "v$N" >/dev/null 2>&1 || true
    git checkout -- "$MANIFEST" "$SIG" "$KINDS" "$KINDS_SIG"
    echo "$1 - the release, the tag and the manifest lines were undone"; exit 1
}

# Archives first, the manifest last (as desktop-release.sh does): every
# device acts on the newest line of the manifest on main, so one pushed
# before its archives were up - or when the upload then failed - made every
# update and every new install fail on a 404.
git push -q origin "v$N" || undo_release "pushing the tag failed"
gh release create "v$N" "$work/web-dist.tar.gz" "$work/src.tar.gz" --title "$LABEL (v$N)" --notes "$SUMMARY" >/dev/null \
    || undo_release "publishing the GitHub release failed"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)" || undo_release "gh repo view failed"
for f in web-dist.tar.gz src.tar.gz; do
    want="$(shasum -a 256 "$work/$f" | awk '{print $1}')"
    # || got="": a failed download must reach the undo, not stop set -e.
    got="$(curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors "https://github.com/$repo/releases/download/v$N/$f" | shasum -a 256 | awk '{print $1}')" || got=""
    [ "$want" = "$got" ] || undo_release "published $f does not match (${got:-download failed}, expected $want)"
done
git add "$MANIFEST" "$SIG" "$KINDS" "$KINDS_SIG"
git commit -q -m "Release $N ($LABEL, $KIND): $SUMMARY"
# Not undone past here: a published release no manifest names is invisible
# to every device, and the local commit only needs pushing.
git push -q origin main \
    || { echo "release v$N is published but main was not pushed (moved meanwhile?): pull --rebase and push main by hand - until then devices simply don't see v$N"; exit 1; }
echo "release $N (version $LABEL, $KIND) published and signed: web $WEB_SHA, source $SRC_SHA"
