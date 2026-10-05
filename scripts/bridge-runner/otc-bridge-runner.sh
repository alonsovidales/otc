#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of switching the bridge on from Settings (issue #145), for
# a device set up without it - and off again (issue #182): a request
# reading "off <reason>" empties bridge-addr instead, and the device
# restarts local-only, as if it had been set up without the bridge.
#
# The device has already done everything it can as the otc user: signed
# the owner in, reserved the name on the bridge and stored the new
# identity (device_uuid, subdomain, bridge_secret) in its database. What
# it can't do is the switch itself - [otc] bridge-addr in the root-owned
# config - nor restart itself to use it. Same shape as the Update button's
# otc-update-runner.sh.
#
# Trust boundary: the trigger file is only a trigger. The bridge joined
# comes from the root-owned config ([otc] bridge-default, else
# off-the.cloud), never from anything the otc user can write; and the
# switch is made only once the database holds a name on that bridge.
set -uo pipefail

REQUEST=/var/lib/otc/bridge.request
STATUS_FILE=/var/lib/otc/bridge-status.json
ENV_FILE=/etc/otc/otc-install.env
DEFAULT_BRIDGE=off-the.cloud

# Only "off left" / "off released" mean anything; any other content is
# the switch-on request it always was.
request="$(head -c 64 "$REQUEST" 2>/dev/null | head -1)"
rm -f "$REQUEST"

# Written to a fresh temp file and moved into place: the directory is the
# otc user's, and a plain "> $STATUS_FILE" as root followed a symlink it
# could have put there - to overwrite (or chmod) any file on the system.
# dd's conv=excl creates the temp file with O_CREAT|O_EXCL, which refuses
# any name already there (a planted symlink included), and umask 022 gives
# it its final mode, so there is no chmod for a symlink swapped in later
# to redirect. mv replaces a symlink rather than following it.
status() {
    local tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM"
    printf '{"state":"%s","message":"%s","updated":"%s"}\n' \
        "$1" "${2//\"/\\\"}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

ini_value() {
    local ini="$1" key="$2"
    awk -F= -v key="$key" '
        /^[[:space:]]*\[/ { section = $0; gsub(/[[:space:]]/, "", section) }
        section == "[otc]" {
            k = $1; gsub(/[[:space:]]/, "", k)
            if (k == key) { v = $2; sub(/^[[:space:]]+/, "", v); sub(/[[:space:]]+$/, "", v); print v; exit }
        }' "$ini" 2>/dev/null
}
env_name="$(systemctl show -p ExecStart --value otc.service 2>/dev/null \
    | sed -n 's/.*argv\[\]=\/usr\/bin\/otc \([^ ;]*\).*/\1/p' | head -1)"
ini="/etc/otc_${env_name:-dev}.ini"
[ -f "$ini" ] || { status failed "the device's config ($ini) is missing"; exit 1; }

# set_bridge_addr writes [otc] bridge-addr=$1 in place (install.sh always
# writes the line, empty on a local-only device); a config without the
# line gets it under [otc].
set_bridge_addr() {
    local value="$1" tmp
    tmp="$(mktemp "$ini.XXXXXX")"
    awk -v bridge="$value" '
        /^[[:space:]]*\[/ {
            if (section == "[otc]" && !done) { print "bridge-addr=" bridge; done = 1 }
            section = $0; gsub(/[[:space:]]/, "", section)
        }
        section == "[otc]" && $0 ~ /^[[:space:]]*bridge-addr[[:space:]]*=/ {
            print "bridge-addr=" bridge; done = 1; next
        }
        { print }
        END { if (section == "[otc]" && !done) print "bridge-addr=" bridge }
    ' "$ini" > "$tmp" || { rm -f "$tmp"; return 1; }
    chmod --reference="$ini" "$tmp" 2>/dev/null || chmod 644 "$tmp"
    chown --reference="$ini" "$tmp" 2>/dev/null || true
    mv "$tmp" "$ini"
}

case "$request" in
    "off left"|"off released")
        reason="${request#off }"
        if [ -z "$(ini_value "$ini" bridge-addr)" ]; then
            status off "$reason"
            exit 0
        fi
        status running "Switching the bridge off"
        set_bridge_addr "" || { status failed "could not update $ini"; exit 1; }
        # bridge-default stays: Settings can join the bridge again later.
        status off "$reason"
        echo "bridge switched off ($reason)"
        systemctl restart otc.service
        exit 0
        ;;
esac

if [ -n "$(ini_value "$ini" bridge-addr)" ]; then
    status done "the device is already on the bridge"
    exit 0
fi

bridge="$(ini_value "$ini" bridge-default)"
bridge="${bridge:-$DEFAULT_BRIDGE}"
if ! [[ "$bridge" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
    status failed "the configured bridge ($bridge) is not a host name"
    exit 1
fi

status running "Switching the bridge on"

# The primary instance's database, as root over the socket (install.sh
# leaves MariaDB's root on socket auth).
read -r subdomain device_uuid secret < <(mysql -N -B otc \
    -e 'select subdomain, device_uuid, bridge_secret from settings limit 1' 2>/dev/null)
# Everything read back from the database is checked before root uses it:
# the otc user can write that database.
name="${subdomain%.$bridge}"
if [ "$name" = "$subdomain" ] || ! [[ "$name" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
    status failed "no name on $bridge has been reserved for this device"
    exit 1
fi

set_bridge_addr "$bridge" || { status failed "could not update $ini"; exit 1; }

# The install identity follows the database, so re-running the installer
# later presents the same identity (its OTC_DB_PASS is left alone).
if [ -f "$ENV_FILE" ] && [[ "$device_uuid" =~ ^[0-9a-fA-F-]{16,64}$ ]] && [[ "$secret" =~ ^[0-9a-fA-F]{32,128}$ ]]; then
    sed -i -e "s/^DEVICE_UUID=.*/DEVICE_UUID=$device_uuid/" \
           -e "s/^BRIDGE_SECRET=.*/BRIDGE_SECRET=$secret/" "$ENV_FILE"
fi

status done "The device is on the bridge as $subdomain"
echo "bridge switched on: $subdomain"

# Last: this is what the device waits for.
systemctl restart otc.service
