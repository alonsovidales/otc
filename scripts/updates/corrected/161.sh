#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 161, as release 169 corrected it: update.sh runs this in place of
# updates/161.sh, which stays as it was signed (see README.md here).
#
# Release 161 (critical) sets up the system's own security updates and
# the IPv4-options filter (CVE-2026-98432) on the devices installed before
# - see updates/161.sh. As it was, it stopped every later update on a
# device whose apt was unwell, for good: under set -e the whole script
# failed with apt-get, and an update has no terminal - dpkg's question
# about a configuration file someone had changed failed, as did a dpkg
# left "interrupted" by a power cut, another apt holding the lock before
# the lock wait was written, or lists gone stale. The device then got no
# later release at all, security fixes included, and one made from the
# image has no SSH to repair apt from.
#
# Corrected: the same function as scripts/install.sh's otc_os_security
# (updater/os_security_test.go), which asks nothing, finishes what dpkg
# left first, waits for another apt, tries a failed install once more, and
# sets up all it can before it reports a failure - the IPv4-options filter
# wherever nft is there already. A failure no longer stops the update:
# /etc/otc/os-updates.pending (root's) records it, every later update runs
# this again until everything is in place (update.sh), and the update says
# so when it finishes.
#
# Not in a container: its host's system is its host's (the entrypoint runs
# no release scripts anyway). Idempotent: run again, it writes the same.
#
# Run as root by scripts/update.sh.
set -uo pipefail

if [ -f /.dockerenv ] || [ "${OTC_CONTAINER:-}" = "1" ]; then
    echo "release 161: in a container, the host's system is left alone"
    exit 0
fi

PENDING=/etc/otc/os-updates.pending

otc_os_security() {
    local rc=0 conf=/etc/apt/apt.conf.d/52otc-security-updates reboot=true
    local apt_get=(apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600)
    export DEBIAN_FRONTEND=noninteractive
    dpkg --force-confdef --force-confold --configure -a || true
    # A device that hasn't asked in months has stale lists: ask first (an
    # offline device installs from what it has, or fails here).
    "${apt_get[@]}" update || true
    if ! "${apt_get[@]}" install unattended-upgrades nftables; then
        dpkg --force-confdef --force-confold --configure -a || true
        "${apt_get[@]}" update || true
        "${apt_get[@]}" install unattended-upgrades nftables \
            || { echo "WARNING: unattended-upgrades and nftables could not be installed"; rc=1; }
    fi
    cat > /etc/apt/apt.conf.d/20auto-upgrades <<'APTEOF' || rc=1
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APTEOF
    if [ "${OTC_SECURITY_REBOOT:-1}" = "0" ] || grep -q 'Automatic-Reboot "false"' "$conf" 2>/dev/null; then
        reboot=false
    fi
    cat > "$conf" <<APTEOF || rc=1
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
    mkdir -p /etc/otc || rc=1
    cat > /etc/otc/ipv4-options.nft <<'NFTEOF' || rc=1
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
    cat > /etc/systemd/system/otc-ipv4-options.service <<'UNITEOF' || rc=1
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
    systemctl daemon-reload || rc=1
    if [ -x /usr/sbin/nft ]; then
        { systemctl enable otc-ipv4-options.service && systemctl restart otc-ipv4-options.service; } \
            || { echo "WARNING: the IPv4-options filter could not be loaded"; rc=1; }
    else
        echo "WARNING: nft is not installed, so the IPv4-options filter can't be loaded yet"
        rc=1
    fi
    return "$rc"
}

if otc_os_security; then
    rm -f "$PENDING"
    echo "release 161 applied: security updates every day (restart at 04:00 when needed), TCP packets with IPv4 options dropped"
else
    { mkdir -p /etc/otc && : >"$PENDING"; } || exit 1
    echo "WARNING: release 161 could not set everything up (see above) - every update tries again until it is in place"
fi
