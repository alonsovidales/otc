#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 115: Settings > Restart device, and a Wi-Fi watchdog that
# restarts a Wi-Fi which has stopped sending properly (once per episode).
# otc.service runs unprivileged with NoNewPrivileges, so both are root
# runners started by a trigger file, like the Update button's:
#   /var/lib/otc/reboot.request       -> otc-reboot.path       -> otc-reboot-runner
#   /var/lib/otc/wifi-restart.request -> otc-wifi-restart.path -> otc-wifi-restart-runner
# See scripts/device-runner/. install.sh sets them up on new devices and
# update.sh reinstalls them on every update; this puts them in place on
# devices installed before, from the verified release being installed.
# Idempotent: re-installing the same files and re-enabling the same units
# changes nothing.
set -euo pipefail

src="${OTC_VERIFIED_SRC:-}"
if [ -z "$src" ] || [ ! -f "$src/scripts/device-runner/otc-wifi-restart-runner.sh" ]; then
    echo "release 115: no verified source with scripts/device-runner (OTC_VERIFIED_SRC=${src:-unset})" >&2
    exit 1
fi

for unit in reboot wifi-restart; do
    install -m 0755 "$src/scripts/device-runner/otc-$unit-runner.sh" "/usr/local/bin/otc-$unit-runner"
    install -m 0644 "$src/scripts/device-runner/otc-$unit.service" "/etc/systemd/system/otc-$unit.service"
    install -m 0644 "$src/scripts/device-runner/otc-$unit.path" "/etc/systemd/system/otc-$unit.path"
done
systemctl daemon-reload
# Nothing could have asked yet; a stray file must not restart the device
# in the middle of this update.
rm -f /var/lib/otc/reboot.request /var/lib/otc/wifi-restart.request
for unit in reboot wifi-restart; do
    systemctl enable --now "otc-$unit.path"
done

echo "release 115 applied: otc-reboot.path and otc-wifi-restart.path are watching /var/lib/otc"
