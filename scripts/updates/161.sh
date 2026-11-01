#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 161 (critical).
#
# CVE-2026-98432 (release 161; owner, 2026-11-01: security updates and a
# night restart): until then a device never updated its system - the
# kernel and packages stayed as they were the day it was set up. Now the
# distribution's security updates install by themselves every day
# (unattended-upgrades: Debian's and Ubuntu's security archives, from the
# distribution's own 50unattended-upgrades, plus the Raspberry Pi's kernel
# and firmware, from the main component of its own archive), and when one
# needs a restart - a new kernel - the device restarts at 04:00
# (OTC_SECURITY_REBOOT=0 on a machine that shouldn't; a choice already
# made stays). apt waits for another apt instead of failing. And TCP
# packets carrying IPv4 options are dropped before the kernel's TCP code
# sees them: that bug needs them, and nothing OTC serves uses them (an
# nftables table of its own, otc-ipv4-options.service). The same as
# scripts/install.sh, for new devices.
#
# Not in a container: its host's system is its host's (the entrypoint runs
# no release scripts anyway). Idempotent: run again, it writes the same.
#
# Run as root by scripts/update.sh.
set -euo pipefail

if [ -f /.dockerenv ] || [ "${OTC_CONTAINER:-}" = "1" ]; then
    echo "release 161: in a container, the host's system is left alone"
    exit 0
fi

otc_os_security() {
    # A device that hasn't asked in months has stale lists: ask first (an
    # offline device installs from what it has, or fails here).
    apt-get update || true
    apt-get install -y unattended-upgrades nftables
    cat > /etc/apt/apt.conf.d/20auto-upgrades <<'APTEOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APTEOF
    local conf=/etc/apt/apt.conf.d/52otc-security-updates reboot=true
    if [ "${OTC_SECURITY_REBOOT:-1}" = "0" ] || grep -q 'Automatic-Reboot "false"' "$conf" 2>/dev/null; then
        reboot=false
    fi
    cat > "$conf" <<APTEOF
// Off The Cloud (scripts/install.sh, updates/161.sh): the system's security
// updates install by themselves every day, and when one needs a restart (a
// new kernel) the device restarts at 04:00. Debian's and Ubuntu's security
// archives come from the distribution's own 50unattended-upgrades; the
// Raspberry Pi's kernel and firmware from its own archive.
Unattended-Upgrade::Origins-Pattern {
        "origin=Raspberry Pi Foundation,codename=\${distro_codename},component=main";
};
Unattended-Upgrade::Automatic-Reboot "$reboot";
Unattended-Upgrade::Automatic-Reboot-WithUsers "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
DPkg::Lock::Timeout "600";
APTEOF
    mkdir -p /etc/otc
    cat > /etc/otc/ipv4-options.nft <<'NFTEOF'
#!/usr/sbin/nft -f
# Off The Cloud: TCP packets carrying IPv4 options never reach the kernel's
# TCP code (CVE-2026-98432 needs them; nothing the device serves uses them).
# Only this table is replaced.
table ip otc_ipv4_options
delete table ip otc_ipv4_options
table ip otc_ipv4_options {
	chain input {
		type filter hook input priority -10; policy accept;
		meta l4proto tcp ip hdrlength != 5 drop
	}
}
NFTEOF
    cat > /etc/systemd/system/otc-ipv4-options.service <<'UNITEOF'
[Unit]
Description=Off The Cloud: drop TCP packets carrying IPv4 options (CVE-2026-98432)
DefaultDependencies=no
After=nftables.service
Before=network-pre.target shutdown.target
Wants=network-pre.target
Conflicts=shutdown.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/otc/ipv4-options.nft

[Install]
WantedBy=sysinit.target
UNITEOF
    systemctl daemon-reload
    systemctl enable otc-ipv4-options.service
    systemctl restart otc-ipv4-options.service
}

otc_os_security
echo "release 161 applied: security updates every day (restart at 04:00 when needed), TCP packets with IPv4 options dropped"
