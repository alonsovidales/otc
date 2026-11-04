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
# If scripts/updates/<N>.sh is committed (N = the manifest's last version +
# 1) it is the release's migration script; one that isn't committed stops
# the release (anything untracked under scripts/updates does). Everything a
# release ships comes from HEAD as committed, never from this checkout's
# other files. In order:
#   0. unpacks a `git archive` of HEAD, installs the web app's packages there
#      from its lockfile (npm ci), and runs `make i18n-check` in release mode
#      on it (docs/i18n.md), stopping there if it fails;
#   1. builds the web bundle (web-dist.tar.gz) in that same tree and tags
#      HEAD as vN;
#   2. makes the release's own source archive (src.tar.gz: `git archive` of
#      the tag, gzip -n - reproducible), which devices build from instead of
#      GitHub's generated archive. The manifest and its signature are
#      export-ignore'd (.gitattributes): they carry the archive's hash, so
#      they can't be inside it;
#   3. appends the manifest line - version, script sha (of the script as
#      that archive holds it, which is what devices check), web sha,
#      summary, source sha - and signs the whole manifest with the release
#      key (Ed25519; ~/.otc/otc-release-signing.pem, its passphrase in the
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
# half-way (after the tag). Over SSH: security unlock-keychain first. A
# shell variable, never exported: the build below runs the web app's npm
# packages (Vite, its plugins, the TypeScript compiler) and Go programs,
# and any of them could read an exported variable and the key's fixed path
# - the one trust anchor every device has. Only the signer gets it (sign).
unset KEY_PASS release_pass
release_pass="$(security find-generic-password -s otc-release-signing -a otc -w 2>/dev/null)" \
    || { echo "can't read the signing key's passphrase from the Keychain (locked? run: security unlock-keychain)"; exit 1; }
[ -n "$release_pass" ] || { echo "the Keychain item otc-release-signing is empty"; exit 1; }
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "commit (or stash) your changes first"; git status --short --untracked-files=no; exit 1; }
# A release script that isn't committed would have its hash signed into the
# manifest and be missing from the source archive: every device's update
# would stop at it, and at every release after it.
[ -z "$(git status --porcelain --untracked-files=all -- scripts/updates)" ] \
    || { echo "scripts/updates has files that aren't committed - commit or remove them first"; git status --short --untracked-files=all -- scripts/updates; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "HEAD is not origin/main - pull or push first"; exit 1; }

last="$(grep -v '^#' "$MANIFEST" | awk -F'\t' 'NF{v=$1} END{print v}')"
N=$((last + 1))
if git cat-file -e "HEAD:scripts/updates/$N.sh" 2>/dev/null; then
    HAS_SCRIPT=1
else
    HAS_SCRIPT=""
fi
last_label="$(grep -v '^#' "$KINDS" | awk -F'\t' 'NF>=3{l=$3} END{print l}')"
lmaj="${last_label%%.*}"; lmin="${last_label#*.}"
if [ "$KIND" = minor ]; then LABEL="$lmaj.$((lmin + 1))"; else LABEL="$((lmaj + 1)).0"; fi
echo "release $N = version $LABEL ($KIND; migration script: ${HAS_SCRIPT:+scripts/updates/$N.sh}${HAS_SCRIPT:-none})"

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

# What is about to be tagged, a `git archive` of HEAD, not this checkout: an
# untracked or ignored file here (a generated file never committed, a
# component never added, a web/.env.local) would pass the checks and go
# into the web bundle while missing from src.tar.gz, which devices build
# from. The web app's packages are installed there from its lockfile
# (npm ci), not taken from this checkout's node_modules, whatever was
# installed into that since. Only proto/generated comes from here
# (gitignored; devices run protoc themselves, here it is `make pb`'s output).
[ -f proto/generated/messages.pb.go ] || { echo "proto/generated is missing - run make pb first"; exit 1; }
mkdir "$work/check"
git archive --format=tar HEAD | tar -xf - -C "$work/check"
cp -R proto/generated "$work/check/proto/"
npm ci --prefix "$work/check/web" --no-audit --no-fund --prefer-offline >/dev/null \
    || { echo "npm ci failed in the archive of HEAD - nothing was built or tagged"; exit 1; }

# The translations' gate (docs/i18n.md), before anything is built or tagged:
# `make i18n-check` in release mode - catalog, generated files, the
# hard-coded-text ratchet (its web scanner uses the packages just
# installed), the Go builds.
make -C "$work/check" --no-print-directory i18n-check I18N_RELEASE=1 \
    || { echo "make i18n-check failed on HEAD - nothing was built or tagged"; exit 1; }

# Built in the same tree. No VITE_ variable of this shell (a VITE_DEVICE
# left from testing against a device) reaches the bundle.
(
    for v in $(compgen -e | grep '^VITE_' || true); do unset "$v"; done
    npm run build --prefix "$work/check/web" >/dev/null
) || { echo "the web app's build failed on HEAD - nothing was tagged"; exit 1; }
tar -cf - -C "$work/check/web/dist" . | gzip -n > "$work/web-dist.tar.gz"
WEB_SHA="$(shasum -a 256 "$work/web-dist.tar.gz" | awk '{print $1}')"

git tag "v$N"
git archive --format=tar --prefix="otc-$N/" "v$N" | gzip -n > "$work/src.tar.gz"
SRC_SHA="$(shasum -a 256 "$work/src.tar.gz" | awk '{print $1}')"
# The manifest must not be in the archive: it records the archive's hash.
if tar -tzf "$work/src.tar.gz" | grep -q "scripts/updates/VERSIONS"; then
    git tag -d "v$N" >/dev/null; echo "the source archive contains the manifest - check .gitattributes"; exit 1
fi
# The release script's hash is that of the script as the archive holds it:
# exactly what every device checks before it runs it.
if [ -n "$HAS_SCRIPT" ]; then
    SCRIPT_SHA="$(tar -xzOf "$work/src.tar.gz" "otc-$N/scripts/updates/$N.sh" | shasum -a 256 | awk '{print $1}')" || SCRIPT_SHA=""
    if ! [[ "$SCRIPT_SHA" =~ ^[0-9a-f]{64}$ ]] || [ "$SCRIPT_SHA" = "$(printf '' | shasum -a 256 | awk '{print $1}')" ]; then
        git tag -d "v$N" >/dev/null; echo "scripts/updates/$N.sh is not in the source archive - check .gitattributes"; exit 1
    fi
else
    SCRIPT_SHA=-
fi
echo "migration script: $SCRIPT_SHA"
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
# the passphrase is in that one process's environment, and no other's.
# Retried, since signing has been seen to fail now and then and work a
# moment later.
sign() {
    local attempt
    for attempt in 1 2 3 4 5; do
        if KEY_PASS="$release_pass" "$OPENSSL" pkcs8 -in "$KEY" -passin env:KEY_PASS 2>/dev/null \
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
echo "next: scripts/docker-release.sh $N (the Docker image for this release)"
