#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 166.
#
# Issue #207: the device tells the apps when it can't reach its storage
# disks, and starts again by itself when they are back. Until now, with the
# disks unplugged, slow at boot or the mirror not assembled, otc.service and
# MariaDB didn't start (their RequiresMountsFor drop-ins), every app said
# "device unreachable", and nothing started them again when the disks came
# back: the owner had to restart the device.
#
# This installs otc-degraded.service, `otc <env> degraded`: the same binary
# without a database, answering every request storage_unavailable on its
# HTTP port and through the bridge. The new raid_watch.py (installed by
# this release's update.sh) starts it a minute after the storage went
# missing, mounts the storage once its device is there, and then starts
# MariaDB and otc.service, which stops it. raid-watch.service is no longer
# ordered after multi-user.target, which waits three minutes for
# otc.service when the disks are missing. The same function as
# scripts/install.sh's, for new devices.
#
# Not in a container: its host starts and stops it, and there is no
# raid_watch.py. Idempotent: run again, it writes the same.
#
# Run as root by scripts/update.sh.
set -euo pipefail

if [ -f /.dockerenv ] || [ "${OTC_CONTAINER:-}" = "1" ]; then
    echo "release 166: in a container, nothing to install"
    exit 0
fi
if [ ! -f /etc/systemd/system/otc.service ]; then
    echo "release 166: no otc.service here, nothing to install"
    exit 0
fi

otc_storage_units() {
    local env="$1" mount_point="$2" unit=/etc/systemd/system/otc-degraded.service
    cat > "$unit.tmp" <<UNITEOF
# Off The Cloud without its storage (issue #207): started by raid_watch.py
# while $mount_point is missing, stopped by otc.service's start.
[Unit]
Description=Off The Cloud without its storage (tells the apps why)
Wants=network-online.target
After=network-online.target
Conflicts=otc.service
Before=otc.service

[Service]
Type=simple
User=otc
Group=otc
WorkingDirectory=/var/lib/otc
ExecStart=/usr/bin/otc $env degraded
Restart=on-failure
RestartSec=5
StateDirectory=otc
LogsDirectory=otc
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ReadOnlyPaths=-$mount_point
CapabilityBoundingSet=
SystemCallArchitectures=native
UNITEOF
    chmod 0644 "$unit.tmp"
    mv -f "$unit.tmp" "$unit"
    if [ -f /etc/systemd/system/raid-watch.service ]; then
        sed -i 's/^After=multi-user.target mdadm.service$/After=mdadm.service/' /etc/systemd/system/raid-watch.service
    fi
    systemctl daemon-reload
}

# The service's environment, as update.sh reads it: the config otc.service
# runs with (/etc/otc_<env>.ini).
env="$(systemctl show -p ExecStart --value otc.service 2>/dev/null \
    | sed -n 's/.*argv\[\]=\/usr\/bin\/otc \([^ ;]*\).*/\1/p' | head -1)"
[[ "$env" =~ ^[A-Za-z0-9_-]{1,32}$ ]] || env=dev

otc_storage_units "$env" /mnt/storage
echo "release 166 applied: otc-degraded.service installed (environment $env), raid-watch.service no longer waits for multi-user.target"
