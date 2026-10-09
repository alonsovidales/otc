#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of the Wi-Fi watchdog (wifiwatch). When the Wi-Fi has
# stopped sending properly - Pit after ~27 h up: the brcmfmac chip over
# SDIO failing its writes ("CMD53 sg block write failed -84"), sending at
# 0.15 MB/s with 7.7% of TCP segments retransmitted, fixed only by a
# reboot - the device (otc.service, unprivileged with NoNewPrivileges)
# drops /var/lib/otc/wifi-restart.request and this restarts the Wi-Fi as
# close to a reboot as it can without one:
#
#   1. the Wi-Fi driver's module out (and the modules using it first,
#      brcmfmac_wcc on newer kernels),
#   2. the SDIO controller the chip sits on unbound and bound again, which
#      powers the chip down and re-enumerates and re-tunes the bus - only
#      when that controller holds no disk (never the SD card the system
#      runs from),
#   3. the module back in, power saving off again,
#   4. the connection back through NetworkManager (the image's network
#      manager; wpa_supplicant/dhcpcd/networkd on hand-made installs).
#
# If the Wi-Fi isn't connected again within CONNECT_WAIT, the device
# restarts instead: a device left off the network by its own watchdog
# could otherwise only be fixed by pulling the plug.
#
# The answer goes to /var/lib/otc/wifi-restart-status.json: state running,
# done, failed or rebooted, which the watchdog reads back.
#
# Trust boundary: the request's content is never read. The interface, the
# driver and the controller all come from the kernel (the default route,
# sysfs), never from anything the otc user can write; a restart less than
# MIN_INTERVAL after the last one is refused, so a flood of requests can't
# keep the Wi-Fi down.
set -uo pipefail

REQUEST=/var/lib/otc/wifi-restart.request
STATUS_FILE=/var/lib/otc/wifi-restart-status.json
# Root's tmpfs: seconds since boot of the last restart (empty after a boot).
LAST=/run/otc-wifi-restart.last
MIN_INTERVAL=1800
CONNECT_WAIT=150

rm -f "$REQUEST"

log() { echo "[otc-wifi-restart] $*"; }

# Temp file moved into place: the directory is the otc user's, and a plain
# "> $STATUS_FILE" as root would follow a symlink put there. dd's
# conv=excl (O_CREAT|O_EXCL) refuses a name planted ahead of it, umask 022
# leaves no chmod to redirect, and mv replaces a symlink, never follows it.
status() {
    local tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM"
    printf '{"state":"%s","message":"%s","updated":"%s"}\n' \
        "$1" "${2//\"/\\\"}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

uptime_s() { cut -d. -f1 /proc/uptime; }

# The interface of the IPv4 default route with the lowest metric.
default_iface() {
    awk 'NR > 1 && $2 == "00000000" && $8 == "00000000" { print $7, $1 }' /proc/net/route \
        | sort -n | awk 'NR == 1 { print $2 }'
}

is_wireless() { [ -e "/sys/class/net/$1/wireless" ] || [ -e "/sys/class/net/$1/phy80211" ]; }

# Connected again: NetworkManager says so, or a default route goes
# through the interface - or through another one now (a cable plugged in
# meanwhile reaches the device just as well).
connected() {
    local via
    via="$(default_iface)"
    [ -n "$via" ] && return 0
    if [ "$nm" = 1 ]; then
        nmcli -t -g GENERAL.STATE device show "$iface" 2>/dev/null | grep -q '^100'
        return
    fi
    return 1
}

wait_connected() {
    local deadline=$(( $(uptime_s) + $1 ))
    while [ "$(uptime_s)" -lt "$deadline" ]; do
        connected && return 0
        sleep 3
    done
    return 1
}

now="$(uptime_s)"
last=""
[ -f "$LAST" ] && last="$(tr -dc '0-9' < "$LAST" | head -c 12)"
if [ -n "$last" ] && [ $(( now - last )) -lt "$MIN_INTERVAL" ]; then
    log "refused: the Wi-Fi was restarted $(( now - last )) s ago"
    status failed "the Wi-Fi was restarted less than 30 minutes ago"
    exit 0
fi

iface="$(default_iface)"
if [ -z "$iface" ] || ! [[ "$iface" =~ ^[A-Za-z0-9_.:-]{1,15}$ ]] || [ "$iface" = "." ] || [ "$iface" = ".." ]; then
    log "refused: no default route"
    status failed "the device has no network connection to restart"
    exit 0
fi
if ! is_wireless "$iface"; then
    log "refused: the default route goes through $iface, which isn't Wi-Fi"
    status failed "the device isn't connected over Wi-Fi"
    exit 0
fi

# Recorded before anything is touched, so even a run cut short counts.
(umask 077; echo "$now" > "$LAST")

nm=0
conn=""
if command -v nmcli >/dev/null 2>&1 && systemctl is-active --quiet NetworkManager; then
    nm=1
    conn="$(nmcli -g GENERAL.CONNECTION device show "$iface" 2>/dev/null | head -n 1)"
fi

devpath="$(readlink -f "/sys/class/net/$iface/device" 2>/dev/null)"
mod=""
modpath="$(readlink -f "/sys/class/net/$iface/device/driver/module" 2>/dev/null)"
[ -n "$modpath" ] && mod="${modpath##*/}"
[[ "$mod" =~ ^[A-Za-z0-9_-]+$ ]] || mod=""

# The SDIO controller the chip hangs off (the Pi 5's 1001100000.mmc), and
# whether it is safe to reset: no block device may sit under it.
host=""
hostdrv=""
case "$devpath" in
    /sys/devices/*/mmc_host/*) host="${devpath%%/mmc_host/*}" ;;
esac
if [ -n "$host" ] && [ -e "$host/driver" ]; then
    hostdrv="$(readlink -f "$host/driver")"
    for b in /sys/class/block/*; do
        p="$(readlink -f "$b" 2>/dev/null)"
        case "$p" in
            "$host"/*)
                log "not resetting ${host##*/}: it also holds ${b##*/}"
                host=""
                break
                ;;
        esac
    done
else
    host=""
fi
hostname="${host##*/}"
[[ "$hostname" =~ ^[A-Za-z0-9_.:-]+$ ]] || { host=""; hostname=""; }

status running "Restarting the Wi-Fi"
log "restarting $iface (driver ${mod:-built in}, controller ${hostname:-not reset}, connection ${conn:-unknown})"

# 1. The driver out, the modules that use it first.
unloaded=0
if [ -n "$mod" ] && [ -d "/sys/module/$mod" ]; then
    for h in /sys/module/"$mod"/holders/*; do
        [ -e "$h" ] || continue
        timeout 30 modprobe -r "${h##*/}" || log "could not unload ${h##*/}"
    done
    if timeout 30 modprobe -r "$mod"; then
        unloaded=1
    else
        log "could not unload $mod"
    fi
fi

# 2. The chip's bus, powered down and up again.
if [ -n "$host" ]; then
    if timeout 30 sh -c 'printf %s "$1" > "$2/unbind"' _ "$hostname" "$hostdrv"; then
        sleep 2
        timeout 30 sh -c 'printf %s "$1" > "$2/bind"' _ "$hostname" "$hostdrv" \
            || log "could not bind $hostname to ${hostdrv##*/} again"
    else
        log "could not unbind $hostname from ${hostdrv##*/}"
    fi
fi

# 3. The driver back (udev may have loaded it already), power saving off.
if [ "$unloaded" = 1 ]; then
    timeout 60 modprobe "$mod" || log "could not load $mod"
fi
if [ "$unloaded" = 0 ] && [ -z "$host" ]; then
    # Nothing could be reset: at least take the link down and up.
    ip link set "$iface" down 2>/dev/null
    sleep 2
    ip link set "$iface" up 2>/dev/null
fi
for _ in $(seq 1 30); do
    [ -e "/sys/class/net/$iface" ] && break
    sleep 2
done
command -v iw >/dev/null 2>&1 && iw dev "$iface" set power_save off 2>/dev/null

# 4. The connection back.
if [ "$nm" = 1 ]; then
    nmcli radio wifi on >/dev/null 2>&1
    if ! wait_connected 45; then
        if [ -n "$conn" ]; then
            timeout 60 nmcli connection up "$conn" ifname "$iface" >/dev/null 2>&1 || log "could not bring the connection up"
        else
            timeout 60 nmcli device connect "$iface" >/dev/null 2>&1 || log "could not connect $iface"
        fi
    fi
else
    for unit in "wpa_supplicant@$iface.service" wpa_supplicant.service dhcpcd.service systemd-networkd.service; do
        systemctl try-restart "$unit" >/dev/null 2>&1
    done
fi

if wait_connected "$CONNECT_WAIT"; then
    command -v iw >/dev/null 2>&1 && iw dev "$iface" set power_save off 2>/dev/null
    log "done: connected again"
    status done "The Wi-Fi was restarted"
    exit 0
fi

log "the Wi-Fi did not come back; restarting the device"
status rebooted "The Wi-Fi did not reconnect after its restart, so the device restarted"
sync
systemctl reboot
