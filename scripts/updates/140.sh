#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 140: Settings > Wi-Fi - the owner changes the network the device
# uses, or its password, from the apps. otc.service runs unprivileged with
# NoNewPrivileges, so the change itself is a root runner started by a
# trigger file, like the others (scripts/device-runner/):
#   /run/otc/wifi-join.request -> otc-wifi-join.path -> otc-wifi-join-runner
# The request is on the /run tmpfs, never the SD card: it holds the Wi-Fi
# password. install.sh sets the runner up on new devices and update.sh
# reinstalls it on every update; this puts it in place on devices installed
# before, from the verified release being installed, in this order:
#   1. The Wi-Fi restart runner, which now leaves the Wi-Fi alone while a
#      change from Settings holds /run/otc-wifi.lock (and brings the Wi-Fi
#      back by its connection's UUID, never by a name two profiles share).
#   2. The Wi-Fi change runner, and its path unit enabled.
#   3. The request the retired first-run Wi-Fi step (SetWifi) left with the
#      password in the clear, on devices without a root side to read it.
# The image's own network script (network_setup.py) is left as it is: the
# repository's copy, which acts on a join request only before setup, goes
# to new images only.
# Idempotent: re-installing the same files and re-enabling the same units
# changes nothing.
set -euo pipefail

src="${OTC_VERIFIED_SRC:-}"
if [ -z "$src" ] || [ ! -f "$src/scripts/device-runner/otc-wifi-join-runner.py" ]; then
    echo "release 140: no verified source with the Wi-Fi change runner (OTC_VERIFIED_SRC=${src:-unset})" >&2
    exit 1
fi

install -m 0755 "$src/scripts/device-runner/otc-wifi-restart-runner.sh" /usr/local/bin/otc-wifi-restart-runner
install -m 0644 "$src/scripts/device-runner/otc-wifi-restart.service" /etc/systemd/system/otc-wifi-restart.service
install -m 0644 "$src/scripts/device-runner/otc-wifi-restart.path" /etc/systemd/system/otc-wifi-restart.path

install -m 0755 "$src/scripts/device-runner/otc-wifi-join-runner.py" /usr/local/bin/otc-wifi-join-runner
install -m 0644 "$src/scripts/device-runner/otc-wifi-join.service" /etc/systemd/system/otc-wifi-join.service
install -m 0644 "$src/scripts/device-runner/otc-wifi-join.path" /etc/systemd/system/otc-wifi-join.path
systemctl daemon-reload
# Nothing could have asked yet: this release's device is the first to.
rm -f /run/otc/wifi-join.request
systemctl enable --now otc-wifi-restart.path
systemctl enable --now otc-wifi-join.path

# Unlinked, never opened: /var/lib/otc is the otc user's directory (and
# something else planted under that name doesn't stop the release).
rm -f /var/lib/otc/wifi_join_request.json 2>/dev/null || true

echo "release 140 applied: otc-wifi-join.path is watching /run/otc"
