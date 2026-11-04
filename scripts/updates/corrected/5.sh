#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 5, as release 169 corrected it: update.sh runs this in place of
# updates/5.sh, which stays as it was signed (see README.md here).
#
# Release 5 (issue #94 follow-up) installs the update runner and its units,
# so the Update button works on a device installed the normal way - see
# updates/5.sh and scripts/update-runner/. It downloaded them from the
# repository's branch, unsigned: on a device that old, the one thing an
# update ever ran that its signed release didn't vouch for. They come from
# the release being installed now instead (OTC_VERIFIED_SRC, checked
# against the signed manifest before any of it runs), together with the
# helper the current unit names, each written beside its destination and
# renamed over it.
#
# Idempotent: the same files installed again, the same path unit enabled.
set -euo pipefail

src="${OTC_VERIFIED_SRC:-}"
dir="$src/scripts/update-runner"
if [ -z "$src" ] || [ ! -f "$dir/otc-update-runner.sh" ]; then
    echo "release 5: no verified source with scripts/update-runner (OTC_VERIFIED_SRC=${src:-unset})" >&2
    exit 1
fi

# put <mode> <src> <dst>: written beside the destination and renamed over it.
put() {
    if ! install -m "$1" "$2" "$3.new.$$" || ! mv -Tf "$3.new.$$" "$3"; then
        rm -f "$3.new.$$"
        return 1
    fi
}

put 0755 "$dir/otc-update-runner.sh" /usr/local/bin/otc-update-runner
put 0755 "$dir/otc-update-stopped.sh" /usr/local/bin/otc-update-stopped
put 0644 "$dir/otc-update.service" /etc/systemd/system/otc-update.service
put 0644 "$dir/otc-update.path" /etc/systemd/system/otc-update.path
systemctl daemon-reload
systemctl enable --now otc-update.path

echo "release 5 applied: otc-update.path is watching /var/lib/otc/update.request"
