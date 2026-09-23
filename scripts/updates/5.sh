#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 5 (issue #94 follow-up): make the Update button work on a device
# installed the normal way.
#
# otc.service runs with NoNewPrivileges=true, so the device could never
# actually start an update: it tried `sudo -n`, which fails there whatever
# sudoers says, and Settings just kept showing the old version. Only a
# hand-set-up development device (a unit without that flag, an otc user
# with sudo) ever got past it. From now on the device drops a trigger file
# and systemd runs the update as root in a unit of its own - see
# scripts/update-runner/. This installs those units.
#
# Runs as root inside scripts/update.sh, which exports OTC_REPO_RAW for
# exactly this kind of fetch; a fork's own repository is honoured. A device
# that has never run an update (Cala after the image install) needs this
# one run from its console once, after which the button works.
#
# Idempotent: re-installing the same files and re-enabling the same unit is
# a no-op.
set -euo pipefail

RAW="${OTC_REPO_RAW:-https://raw.githubusercontent.com/alonsovidales/otc/main}"
RAW="${RAW%/}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for f in otc-update-runner.sh otc-update.service otc-update.path; do
    curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/$f" "$RAW/scripts/update-runner/$f"
done

install -m 0755 "$tmp/otc-update-runner.sh" /usr/local/bin/otc-update-runner
install -m 0644 "$tmp/otc-update.service" /etc/systemd/system/otc-update.service
install -m 0644 "$tmp/otc-update.path" /etc/systemd/system/otc-update.path
systemctl daemon-reload
systemctl enable --now otc-update.path

echo "release 5 applied: otc-update.path is watching /var/lib/otc/update.request"
