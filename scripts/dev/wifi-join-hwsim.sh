#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Settings > Wi-Fi's root runner (scripts/device-runner/otc-wifi-join-runner.py)
# against a real NetworkManager and wpa_supplicant, with simulated radios
# (mac80211_hwsim): wlan0 is the device's Wi-Fi, wlan1 an access point
# (hostapd, one or more networks on one radio) in its own network namespace,
# so traffic really goes over the air, with DHCP (dnsmasq). The "bridge" the
# runner checks the internet with is an HTTPS server in that namespace
# (bridge.test, 203.0.113.10, a test CA this script adds to the VM's trust
# store), which a scenario can make unreachable; the device's config is
# /etc/otc_prod.ini, found as the runner finds it: from a stand-in
# otc.service's ExecStart (/usr/bin/otc prod, a `sleep`), which also makes
# /run/otc (RuntimeDirectory=otc) as the real one does.
#
# ONLY in a throwaway VM: it loads kernel modules, adds a namespace, a test
# CA, a stand-in /usr/bin/otc and otc.service, and changes NetworkManager's
# profiles. Never on a device, never in the Lima VM `otc`. A Debian 13 VM
# (closest to Raspberry Pi OS Trixie; NetworkManager 1.52.1 in both):
#
#   limactl create --name=otc-wifi --cpus=2 --memory=2 --vm-type=vz --mount-none template:debian-13
#   limactl start otc-wifi
#   # NetworkManager must leave Lima's eth0 alone - write this BEFORE
#   # installing it:
#   limactl shell otc-wifi sudo bash -c 'mkdir -p /etc/NetworkManager/conf.d && printf "[keyfile]\nunmanaged-devices=interface-name:eth0;interface-name:wlan1;interface-name:otcv*;interface-name:hwsim*\n" > /etc/NetworkManager/conf.d/99-unmanaged.conf'
#   limactl shell otc-wifi sudo apt-get install -y linux-image-arm64 network-manager wpasupplicant hostapd iw curl iputils-ping dnsmasq-base rfkill iptables openssl
#   # the cloud kernel has no mac80211_hwsim: boot the generic one
#   limactl shell otc-wifi sudo bash -c 'systemctl disable --now hostapd; apt-get purge -y $(dpkg -l "linux-image-*cloud*" | awk "/^ii/ {print \$2}"); update-grub'
#   limactl stop otc-wifi && limactl start otc-wifi
#
# Then, from the repository (again after any change to the runner):
#
#   limactl copy scripts/device-runner/otc-wifi-join-runner.py scripts/device-runner/otc-wifi-join.path scripts/device-runner/otc-wifi-join.service scripts/dev/wifi-join-hwsim.sh otc-wifi:/tmp/otc-wifi/
#   limactl shell otc-wifi sudo bash /tmp/otc-wifi/wifi-join-hwsim.sh /tmp/otc-wifi            # every scenario (~30 min)
#   limactl shell otc-wifi sudo bash /tmp/otc-wifi/wifi-join-hwsim.sh /tmp/otc-wifi s03 s19    # some
#   limactl stop otc-wifi       # keep it for next time; `limactl delete -f otc-wifi` when done
#
# (`limactl shell otc-wifi mkdir -p /tmp/otc-wifi` first: /tmp is emptied at
# each boot. Scenarios are the s<NN> functions below, numbered as design
# (G)'s Lima VM list, 14 on added.) A real power cut in the middle of a
# change: `... wifi-join-hwsim.sh /tmp/otc-wifi powercut <point>` (load: the
# trial just loaded; saved: just after the new profile was saved, before the
# runner's sync; retired: just after the old profile was deleted, after it)
# cuts the VM's power from inside (sysrq b: no sync, no shutdown). With vz the
# VM boots again by itself and that `limactl shell` hangs: stop it, copy the
# files to /tmp/otc-wifi again and run `... powercut_check`.
#
# Each check prints PASS or FAIL (INFO lines report without counting); the
# exit status is the number that failed.
set -uo pipefail
export LC_ALL=C.UTF-8

SRC="${1:?usage: wifi-join-hwsim.sh <directory with otc-wifi-join-runner.py and its units> [scenario...]}"
shift
NS=otcap
AP_IF=wlan1
DEV_IF=wlan0
STATUS=/run/otc-wifi-join/status.json
STATE=/run/otc-wifi-join/state.json
NMRUN=/run/NetworkManager/system-connections
T=/run/otc-hwsim
BRIDGE_IP=203.0.113.10
BRIDGE_HOST=bridge.test
INI=/etc/otc_prod.ini
HOME_PW=right-password-1
failed=0

die() { echo "wifi-join-hwsim: $*" >&2; exit 100; }
[ "$(id -u)" = 0 ] || die "run as root"
for c in nmcli hostapd dnsmasq iw ip iptables curl python3 flock openssl runuser; do
    command -v "$c" >/dev/null || die "missing $c"
done
nmcli -t -f DEVICE,STATE device | grep -q '^eth0:unmanaged' \
    || die "NetworkManager manages eth0: write the unmanaged-devices conf first (see the header)"

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; failed=$((failed + 1)); }
info() { echo "INFO: $*"; }
check() { local what="$1"; shift; if "$@"; then pass "$what"; else fail "$what"; fi; }
section() { echo; echo "--- $*"; }

# --- the radios, the access point's namespace and the "bridge" -------------
setup_network() {
    mkdir -p "$T"
    lsmod | grep -q '^mac80211_hwsim' || modprobe mac80211_hwsim radios=2 || die "no mac80211_hwsim"
    local phy d
    if ! ip netns list | grep -qw "$NS"; then
        ip netns add "$NS"
        d=$(( SECONDS + 20 ))
        until iw dev "$AP_IF" info >/dev/null 2>&1 || [ "$SECONDS" -ge "$d" ]; do sleep 0.5; done
        phy="phy$(iw dev "$AP_IF" info | awk '/wiphy/ { print $2 }')"
        iw phy "$phy" set netns name "$NS" || die "could not move $AP_IF"
        ip netns exec "$NS" ip link set lo up
        ip netns exec "$NS" ip link add otcnet type dummy
        ip netns exec "$NS" ip addr add "$BRIDGE_IP/32" dev otcnet
        ip netns exec "$NS" ip link set otcnet up
    fi
    # The bridge's certificate, from a test CA the VM trusts.
    local pki=/var/lib/otc-hwsim/pki
    if [ ! -s "$pki/srv.crt" ] || [ ! -s /usr/local/share/ca-certificates/otc-hwsim-test-ca.crt ]; then
        mkdir -p "$pki"
        openssl req -x509 -newkey rsa:2048 -nodes -keyout "$pki/ca.key" -out "$pki/ca.crt" -days 3650 \
            -subj /CN=otc-hwsim-test-ca 2>/dev/null
        openssl req -newkey rsa:2048 -nodes -keyout "$pki/srv.key" -out "$pki/srv.csr" -subj "/CN=$BRIDGE_HOST" 2>/dev/null
        printf 'subjectAltName=DNS:%s\n' "$BRIDGE_HOST" > "$pki/ext"
        openssl x509 -req -in "$pki/srv.csr" -CA "$pki/ca.crt" -CAkey "$pki/ca.key" -CAcreateserial \
            -out "$pki/srv.crt" -days 3650 -extfile "$pki/ext" 2>/dev/null
        cp "$pki/ca.crt" /usr/local/share/ca-certificates/otc-hwsim-test-ca.crt
        update-ca-certificates >/dev/null 2>&1
    fi
    grep -q " $BRIDGE_HOST\$" /etc/hosts || echo "$BRIDGE_IP $BRIDGE_HOST" >> /etc/hosts
    if ! { [ -s "$T/bridge.pid" ] && kill -0 "$(cat "$T/bridge.pid")" 2>/dev/null; }; then
        cat > "$T/bridge.py" <<EOF
import http.server, ssl
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(204)
        self.end_headers()
    def log_message(self, fmt, *args):
        with open("$T/bridge.log", "a") as f:
            f.write(self.client_address[0] + "\n")
srv = http.server.HTTPServer(("$BRIDGE_IP", 443), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("$pki/srv.crt", "$pki/srv.key")
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
EOF
        ip netns exec "$NS" setsid python3 -I "$T/bridge.py" >/dev/null 2>&1 < /dev/null &
        echo $! > "$T/bridge.pid"
        sleep 1
    fi
}

# ap <spec>...: the access point, anew, one network per spec
# "<ssid>|<open|wpa2|sae>|<password>|<flags>" on one radio (the first on
# wlan1, the others as wlan1_1, ...), each with DHCP on 10.77.<n>.0/24.
# Flags: hidden; acl (refuses every station); nodhcp; psk2=<second
# password> (wpa2: either password works). With AP_BRIDGE set, the first is
# bridged to it (a router whose Wi-Fi and cable ports are one network) and
# DHCP serves the bridge. Left alone when it already serves exactly that.
ap() {
    local conf="$T/hostapd.conf" new="$T/hostapd.conf.new" i=0 spec ssid mode pw flags ifn hexssid psk lan
    local -a dhcp=()
    : > "$new"
    for spec in "$@"; do
        IFS='|' read -r ssid mode pw flags <<< "$spec"
        hexssid="$(printf '%s' "$ssid" | od -An -tx1 | tr -d ' \n')"
        if [ "$i" = 0 ]; then
            ifn="$AP_IF"
            printf 'interface=%s\ndriver=nl80211\nhw_mode=g\nchannel=6\n' "$AP_IF" >> "$new"
            [ -n "${AP_BRIDGE:-}" ] && echo "bridge=$AP_BRIDGE" >> "$new"
        else
            ifn="${AP_IF}_$i"
            echo "bss=$ifn" >> "$new"
        fi
        {
            echo "ssid2=$hexssid"
            [[ "$flags" == *hidden* ]] && echo "ignore_broadcast_ssid=1"
            [[ "$flags" == *acl* ]] && printf 'macaddr_acl=1\naccept_mac_file=%s/none.accept\n' "$T"
            case "$mode" in
                wpa2)
                    # PSKs in hex: no doubt about how hostapd reads a
                    # passphrase with spaces or symbols.
                    psk="$(python3 -c 'import hashlib,sys; print(hashlib.pbkdf2_hmac("sha1", sys.argv[1].encode(), sys.argv[2].encode(), 4096, 32).hex())' "$pw" "$ssid")"
                    printf 'wpa=2\nwpa_key_mgmt=WPA-PSK\nrsn_pairwise=CCMP\n'
                    if [[ "$flags" == *psk2=* ]]; then
                        local pw2="${flags##*psk2=}" psk2
                        psk2="$(python3 -c 'import hashlib,sys; print(hashlib.pbkdf2_hmac("sha1", sys.argv[1].encode(), sys.argv[2].encode(), 4096, 32).hex())' "$pw2" "$ssid")"
                        printf '00:00:00:00:00:00 %s\n00:00:00:00:00:00 %s\n' "$psk" "$psk2" > "$T/psk.$i"
                        echo "wpa_psk_file=$T/psk.$i"
                    else
                        echo "wpa_psk=$psk"
                    fi ;;
                sae)
                    printf 'wpa=2\nwpa_key_mgmt=SAE\nrsn_pairwise=CCMP\nieee80211w=2\nsae_password=%s\n' "$pw" ;;
            esac
        } >> "$new"
        if [ "$i" = 0 ] && [ -n "${AP_BRIDGE:-}" ]; then lan="$AP_BRIDGE"; else lan="$ifn"; fi
        echo "#lan $i $lan" >> "$new"
        [[ "$flags" == *nodhcp* ]] || dhcp+=("$i:$lan")
        i=$((i + 1))
    done
    if cmp -s "$new" "$conf" && [ -s "$T/hostapd.pid" ] && kill -0 "$(cat "$T/hostapd.pid")" 2>/dev/null; then
        return 0
    fi
    ap_stop
    : > "$T/none.accept"
    mv "$new" "$conf"
    ip netns exec "$NS" hostapd -B -P "$T/hostapd.pid" "$conf" >/dev/null || { fail "hostapd: $*"; return 1; }
    local d args=()
    for d in $(sed -n 's/^#lan \([0-9]*\) \(.*\)$/\1:\2/p' "$conf"); do
        ip netns exec "$NS" ip addr replace "10.77.${d%%:*}.1/24" dev "${d#*:}"
    done
    for d in "${dhcp[@]}"; do
        args+=("--interface=${d#*:}" "--dhcp-range=10.77.${d%%:*}.50,10.77.${d%%:*}.150,12h")
    done
    if [ "${#args[@]}" -gt 0 ]; then
        # port=0: DHCP only (a default route through 10.77.<n>.1), no DNS.
        ip netns exec "$NS" dnsmasq --port=0 --bind-interfaces --except-interface=lo "${args[@]}" \
            --pid-file="$T/dnsmasq.pid" --dhcp-leasefile="$T/dnsmasq.leases"
    fi
    sleep 2
}

ap_stop() {
    [ -s "$T/hostapd.pid" ] && kill "$(cat "$T/hostapd.pid")" 2>/dev/null
    [ -s "$T/dnsmasq.pid" ] && kill "$(cat "$T/dnsmasq.pid")" 2>/dev/null
    rm -f "$T/hostapd.pid" "$T/dnsmasq.pid" "$T/hostapd.conf"
    sleep 1
    ip netns exec "$NS" ip addr flush dev "$AP_IF" 2>/dev/null
}

# --- the device side, installed as install.sh does --------------------------
setup_runner() {
    id otc >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin otc
    install -d -o otc -g otc /var/lib/otc
    # The device service's stand-in: its ExecStart names the config, and
    # RuntimeDirectory=otc makes /run/otc.
    if [ ! -e /usr/bin/otc ]; then
        printf '#!/bin/sh\n# wifi-join-hwsim.sh stand-in\nexec sleep infinity\n' > /usr/bin/otc
        chmod 0755 /usr/bin/otc
    fi
    grep -q 'wifi-join-hwsim.sh stand-in' /usr/bin/otc || die "/usr/bin/otc isn't this script's stand-in: not a throwaway VM?"
    cat > /etc/systemd/system/otc.service <<'EOF'
[Unit]
Description=otc stand-in (wifi-join-hwsim.sh)
[Service]
User=otc
Group=otc
ExecStart=/usr/bin/otc prod
RuntimeDirectory=otc
EOF
    install -m 0755 "$SRC/otc-wifi-join-runner.py" /usr/local/bin/otc-wifi-join-runner
    install -m 0644 "$SRC/otc-wifi-join.service" /etc/systemd/system/otc-wifi-join.service
    install -m 0644 "$SRC/otc-wifi-join.path" /etc/systemd/system/otc-wifi-join.path
    systemctl daemon-reload
    systemctl start otc.service
    rm -f /run/otc/wifi-join.request
    systemctl enable --now otc-wifi-join.path >/dev/null 2>&1 || die "could not enable otc-wifi-join.path"
    set_bridge "$BRIDGE_HOST"
}

# set_bridge <host>: the device's [otc] bridge-addr ("" for none: the
# runner checks the gateway instead).
set_bridge() {
    printf '[otc]\nbridge-addr=%s\n' "$1" > "$INI"
    chown root:root "$INI"
    chmod 0644 "$INI"
}

# req <ssid> <password> [hidden]: asks as the device does (network/join.go:
# the otc user, the JSON with only '"' and '\' escaped, a 0600 temporary
# file linked into place, then unlinked); prints the request's id.
req() {
    local id d=$(( SECONDS + 60 ))
    while [ -e /run/otc/wifi-join.request ] && [ "$SECONDS" -lt "$d" ]; do sleep 0.5; done
    id="$(python3 -c 'import secrets; print(secrets.token_hex(8))')"
    runuser -u otc -- python3 -I -c '
import os, sys, tempfile
rid, ssid, psk, hidden = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] == "1"
def js(s):
    b = s.encode("utf-8")
    return b"\"" + b.replace(b"\\", b"\\\\").replace(b"\"", b"\\\"") + b"\""
body = b"{\"v\":1,\"id\":\"" + rid.encode() + b"\",\"ssid\":" + js(ssid) + b",\"psk\":" + js(psk) \
    + (b",\"hidden\":true}" if hidden else b",\"hidden\":false}")
fd, tmp = tempfile.mkstemp(dir="/run/otc", prefix=".wifi-join-")
os.write(fd, body)
os.close(fd)
try:
    os.link(tmp, "/run/otc/wifi-join.request")
finally:
    os.unlink(tmp)
' "$id" "$1" "$2" "${3:-0}"
    echo "$id"
}

# answer <id> [seconds]: the runner's final answer for id, read as the otc
# user - the device's own view of it ("" on a timeout).
answer() {
    local deadline=$(( SECONDS + ${2:-480} )) st
    while [ "$SECONDS" -lt "$deadline" ]; do
        st="$(runuser -u otc -- cat "$STATUS" 2>/dev/null)"
        if [ -n "$st" ] && python3 -c 'import json,sys; s=json.loads(sys.argv[1]); sys.exit(0 if s["id"]==sys.argv[2] and s["state"]!="joining" else 1)' "$st" "$1" 2>/dev/null; then
            echo "$st"
            return 0
        fi
        sleep 1
    done
    return 1
}

field() { python3 -c 'import json,sys; v=json.loads(sys.argv[1])[sys.argv[2]]; print(str(v).lower() if isinstance(v, bool) else v)' "$1" "$2" 2>/dev/null; }
clear_history() { rm -f /run/otc-wifi-join/history; }

# The network wlan0 is on, by name ("" for none) and by its bytes.
on_ssid() { iw dev "$DEV_IF" link | sed -n 's/^\s*SSID: //p'; }
on_hex() { nmcli -t -f ACTIVE,SSID-HEX device wifi list ifname "$DEV_IF" --rescan no | awk -F: '$1 == "yes" { print $2; exit }'; }
hex_of() { printf '%s' "$1" | od -An -tx1 | tr -d ' \n' | tr a-f A-F; }
ours() { nmcli -t -f NAME connection show | grep -cx 'OTC-WiFi'; }
our_uuid() { nmcli -t -f NAME,UUID connection show | sed -n 's/^OTC-WiFi:\(.*\)$/\1/p' | head -1; }
our_file() { nmcli -t -f NAME,FILENAME connection show | sed -n 's/^OTC-WiFi:\(.*\)$/\1/p' | head -1 | sed 's/\\:/:/g'; }
saved_under_etc() { [ "$(ours)" = 1 ] && [[ "$(our_file)" == /etc/NetworkManager/system-connections/* ]] && [ -s "$(our_file)" ]; }
no_trial_left() { ! ls "$NMRUN"/otc-wifi-* >/dev/null 2>&1 && ! nmcli -t -f NAME connection show | grep -qx 'OTC-WiFi-new'; }
# NetworkManager's state 100 (connected), not only associated: iw shows
# the name already while a wrong password is being tried.
nm_connected() { nmcli -g GENERAL.STATE device show "$DEV_IF" | grep -q '^100'; }
wait_on() { local d=$(( SECONDS + ${2:-60} )); while [ "$SECONDS" -lt "$d" ]; do [ "$(on_ssid)" = "$1" ] && nm_connected && return 0; sleep 1; done; return 1; }
unit_idle() { local d=$(( SECONDS + 300 )) s; while [ "$SECONDS" -lt "$d" ]; do s="$(systemctl is-active otc-wifi-join.service)"; [ "$s" = inactive ] || [ "$s" = failed ] && return 0; sleep 0.5; done; return 1; }
wait_trial() { local d=$(( SECONDS + 90 )); until ls "$NMRUN"/otc-wifi-*.nmconnection >/dev/null 2>&1 || [ "$SECONDS" -ge "$d" ]; do sleep 0.2; done; }
keyfile_lines() { grep -E '^(id|uuid|type|interface-name|autoconnect|autoconnect-priority|mode|ssid|hidden|cloned-mac-address|key-mgmt|psk|method|address1|dns|addr-gen-mode)=' "$1" | sed 's/^/    /'; }

# baseline: home (WPA2, $HOME_PW) on the air, the device on it through one
# saved OTC-WiFi.
baseline() {
    ap "home|wpa2|$HOME_PW"
    if wait_on home 30 && saved_under_etc; then return 0; fi
    clear_history
    local st
    st="$(answer "$(req home "$HOME_PW")")"
    if [ "$(field "$st" code)" = connected ] && wait_on home 30; then return 0; fi
    fail "baseline: on home through OTC-WiFi ($st)"
    return 1
}

# The kill switch for the commit's steps: nmcli, through a wrapper that
# kills its caller (the runner) right after the call whose arguments hold
# the text in $T/kill-at - as a power cut would, but with ExecStopPost
# still running.
kill_wrapper_on() {
    [ -e /usr/bin/nmcli.real ] && return 0
    dpkg-divert --quiet --local --rename --divert /usr/bin/nmcli.real --add /usr/bin/nmcli
    cat > /usr/bin/nmcli <<EOF
#!/bin/bash
# wifi-join-hwsim.sh: nmcli, killing its caller right after a chosen call.
/usr/bin/nmcli.real "\$@"
rc=\$?
if [ -s $T/kill-at ] && [[ " \$* " == *"\$(cat $T/kill-at)"* ]]; then
    action="\$(cat $T/kill-how 2>/dev/null)"
    rm -f $T/kill-at $T/kill-how
    cp $STATE $T/state-at-kill 2>/dev/null || echo none > $T/state-at-kill
    if [ "\$action" = powercut ]; then
        echo b > /proc/sysrq-trigger
    fi
    kill -KILL "\$PPID"
fi
exit \$rc
EOF
    chmod 0755 /usr/bin/nmcli
    sync
}
kill_wrapper_off() {
    [ -e /usr/bin/nmcli.real ] || return 0
    rm -f /usr/bin/nmcli "$T/kill-at" "$T/kill-how"
    dpkg-divert --quiet --local --rename --remove /usr/bin/nmcli
    sync
}

cleanup() {
    kill_wrapper_off
    [ -n "${holder:-}" ] && kill "$holder" 2>/dev/null
}
trap cleanup EXIT

runner_mod='import importlib.util, importlib.machinery, sys
l = importlib.machinery.SourceFileLoader("r", "/usr/local/bin/otc-wifi-join-runner")
s = importlib.util.spec_from_loader("r", l)
m = importlib.util.module_from_spec(s)
l.exec_module(m)
'
classify() { python3 -I -c "$runner_mod"'print(m.classify(sys.argv[1]))' "$1"; }
# nm_get <property> <connection>: a profile's value (secrets too), with
# nmcli's -g escaping ('\:' and '\\') undone as the runner does.
nm_get() { nmcli -s -g "$1" connection show "$2" | python3 -I -c "$runner_mod"'sys.stdout.write(m.unescape_value(sys.stdin.read()))'; }

# --- scenarios ---------------------------------------------------------------

s01() {
    section "1. a WPA2 network, from no Wi-Fi at all: saved under /etc"
    nmcli -t -f UUID,TYPE connection show | awk -F: '$2 == "802-11-wireless" { print $1 }' | xargs -r -n1 nmcli connection delete uuid >/dev/null 2>&1
    ap "home|wpa2|$HOME_PW"
    clear_history
    # What NetworkManager shows while the trial is loaded.
    ( while :; do nmcli -t -f NAME,FILENAME connection show | grep '^OTC-WiFi-new:'; sleep 0.3; done ) > "$T/trial-seen" 2>/dev/null &
    local sampler=$! st id
    id="$(req home "$HOME_PW")"
    st="$(answer "$id")"
    kill "$sampler" 2>/dev/null; wait "$sampler" 2>/dev/null
    check "joined: $st" [ "$(field "$st" code)" = connected ]
    check "not left (no network before), not restored" [ "$(field "$st" left)/$(field "$st" restored)" = false/false ]
    check "on home" [ "$(on_ssid)" = home ]
    check "the trial was loaded from RAM ($(head -1 "$T/trial-seen"))" grep -q "^OTC-WiFi-new:$NMRUN/otc-wifi-[0-9a-f]*\.nmconnection$" "$T/trial-seen"
    check "saved under /etc: $(our_file)" saved_under_etc
    check "no trial left in RAM" no_trial_left
    check "the saved file is root's, 0600 ($(stat -c '%U %a' "$(our_file)"))" [ "$(stat -c '%U %a' "$(our_file)")" = "root 600" ]
    echo "  the saved keyfile (psk masked):"
    keyfile_lines "$(our_file)" | sed 's/^\(    psk=\).*/\1<set>/'
    nmcli -f connection.id,connection.autoconnect,connection.autoconnect-priority,802-11-wireless.ssid,802-11-wireless.cloned-mac-address,802-11-wireless-security.key-mgmt,ipv6.addr-gen-mode connection show OTC-WiFi | sed 's/^/    /'
    check "autoconnect yes, priority 10" [ "$(nmcli -g connection.autoconnect,connection.autoconnect-priority connection show OTC-WiFi | tr '\n' ' ')" = "yes 10 " ]
    check "cloned-mac-address permanent" [ "$(nmcli -g 802-11-wireless.cloned-mac-address connection show OTC-WiFi)" = permanent ]
    check "IPv6 addresses not made from the MAC ($(nmcli -g ipv6.addr-gen-mode connection show OTC-WiFi))" \
        [ "$(nmcli -g ipv6.addr-gen-mode connection show OTC-WiFi)" = stable-privacy ]
    check "the password NetworkManager keeps is the one sent" [ "$(nm_get 802-11-wireless-security.psk OTC-WiFi)" = "$HOME_PW" ]
    check "state directory root's, 0755 ($(stat -c '%U %a' /run/otc-wifi-join))" [ "$(stat -c '%U %a' /run/otc-wifi-join)" = "root 755" ]
    check "status.json 0644, readable by otc" bash -c '[ "$(stat -c %a '"$STATUS"')" = 644 ] && runuser -u otc -- cat '"$STATUS"' >/dev/null'
    check "state.json gone after the change" [ ! -e "$STATE" ]
    check "the history is not readable by otc" bash -c '! runuser -u otc -- cat /run/otc-wifi-join/history >/dev/null 2>&1'
    check "the request is gone" [ ! -e /run/otc/wifi-join.request ]
    echo "  DNS from this network: '$(nmcli -g IP4.DNS device show "$DEV_IF")' (none: DHCP only)"
}

s12() {
    section "12. NetworkManager restarted after a commit: the profile stays and reconnects"
    baseline || return
    local u; u="$(our_uuid)"
    systemctl restart NetworkManager
    check "back on home after a NetworkManager restart" wait_on home 90
    check "the same OTC-WiFi, still under /etc" bash -c '[ "$(nmcli -t -f NAME,UUID connection show | sed -n "s/^OTC-WiFi://p")" = "'"$u"'" ]'
    check "nothing reloaded from RAM" no_trial_left
}

s02() {
    section "2. the router's password changed: the new one joins, the old profile goes"
    baseline || return
    local old st
    old="$(our_uuid)"
    # rpi-imager's profile, say: someone else's for the same network.
    nmcli connection delete preconfigured >/dev/null 2>&1
    nmcli connection add type wifi con-name preconfigured ifname "$DEV_IF" ssid home \
        wifi-sec.key-mgmt wpa-psk wifi-sec.psk "$HOME_PW" connection.autoconnect yes >/dev/null
    ap "home|wpa2|new-password-22"
    sleep 25
    echo "  25 s after the router's password changed: wlan0 $(nmcli -g GENERAL.STATE device show "$DEV_IF"), connection '$(nmcli -g GENERAL.CONNECTION device show "$DEV_IF")'"
    journalctl -u NetworkManager --since "-30s" --no-pager -o cat | grep -E "no-secrets|secrets|blocked|autoconnect" | tail -6 | sed 's/^/    NM: /'
    clear_history
    st="$(answer "$(req home new-password-22)")"
    check "joined with the new password: $st" [ "$(field "$st" code)" = connected ]
    check "nothing to go back to: left=false" [ "$(field "$st" left)" = false ]
    check "one OTC-WiFi, a new one ($(our_uuid) was $old)" bash -c '[ "$(nmcli -t -f NAME connection show | grep -cx OTC-WiFi)" = 1 ] && [ "$(nmcli -t -f NAME,UUID connection show | sed -n "s/^OTC-WiFi://p")" != "'"$old"'" ]'
    check "saved under /etc: $(our_file)" saved_under_etc
    check "someone else's profile for home kept, autoconnect off" \
        [ "$(nmcli -t -g connection.autoconnect connection show preconfigured)" = no ]
    nmcli connection delete preconfigured >/dev/null
    HOME_PW=new-password-22
}

s18() {
    section "18. a profile that failed for want of secrets: does NetworkManager try it again by itself?"
    baseline || return
    ap "home|wpa2|other-password-18"
    sleep 45
    local tries
    tries="$(journalctl -u NetworkManager --since "-50s" --no-pager -o cat | grep -c "Activation: starting connection 'OTC-WiFi'")"
    info "45 s with a password it doesn't have: wlan0 $(nmcli -g GENERAL.STATE device show "$DEV_IF"); NetworkManager started OTC-WiFi $tries time(s)"
    journalctl -u NetworkManager --since "-50s" --no-pager -o cat | grep -iE "no-secrets|blocked|autoconnect" | tail -4 | sed 's/^/    NM: /'
    ap "home|wpa2|$HOME_PW"
    local t0=$SECONDS
    if wait_on home 180; then
        info "the router's old password back: NetworkManager reconnected by itself after $((SECONDS - t0)) s"
    else
        info "the router's old password back: NetworkManager did NOT reconnect by itself within 180 s (wlan0 $(nmcli -g GENERAL.STATE device show "$DEV_IF"))"
        # A restart of the device restarts NetworkManager, which forgets
        # what it blocked.
        systemctl restart NetworkManager
        t0=$SECONDS
        if wait_on home 90; then
            info "after a NetworkManager restart (as at a device restart) it reconnected by itself in $((SECONDS - t0)) s"
        else
            info "not even after a NetworkManager restart"
            nmcli --wait 40 connection up OTC-WiFi >/dev/null 2>&1
        fi
        check "back on home" wait_on home 30
    fi
}

s03() {
    section "3. a wrong password: back on the network it was on, nothing on disk"
    baseline || return
    clear_history
    local st code
    st="$(answer "$(req home wrong-password-3)")"
    code="$(field "$st" code)"
    check "wrong password: $st" [ "$code" = wrong_password ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    check "back on home" wait_on home
    check "the wrong password is nowhere under /etc/NetworkManager or /run" bash -c '! grep -rqsa wrong-password-3 /etc/NetworkManager /run/NetworkManager /run/otc /run/otc-wifi-join'
    check "no trial left" no_trial_left
    check "one OTC-WiFi, the same password" bash -c '[ "$(nmcli -t -f NAME connection show | grep -cx OTC-WiFi)" = 1 ]' && [ "$(nm_get 802-11-wireless-security.psk OTC-WiFi)" = "$HOME_PW" ]
}

s04() {
    section "4. a network that isn't there: refused, nothing touched"
    baseline || return
    clear_history
    local st
    st="$(answer "$(req elsewhere some-password-4)")"
    check "not found: $st" [ "$(field "$st" code)" = not_found ]
    check "never left" [ "$(field "$st" left)" = false ]
    check "still on home" [ "$(on_ssid)" = home ]
    check "no trial was made" no_trial_left
}

s14() {
    section "14. connected but no internet through it: rolled back (bridge, then gateway)"
    baseline || return
    local u st
    u="$(our_uuid)"
    set_bridge "$BRIDGE_HOST"
    ip netns exec "$NS" iptables -I INPUT -d "$BRIDGE_IP" -p tcp --dport 443 -j DROP
    clear_history
    st="$(answer "$(req home "$HOME_PW")")"
    check "the bridge unreachable: $st" [ "$(field "$st" code)" = no_internet ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    check "the old OTC-WiFi kept, still saved" bash -c '[ "$(nmcli -t -f NAME,UUID connection show | sed -n "s/^OTC-WiFi://p")" = "'"$u"'" ]'
    check "back on home" wait_on home
    ip netns exec "$NS" iptables -D INPUT -d "$BRIDGE_IP" -p tcp --dport 443 -j DROP
    set_bridge ""
    ip netns exec "$NS" iptables -I INPUT -p icmp -j DROP
    st="$(answer "$(req home "$HOME_PW")")"
    check "no bridge, the gateway silent: $st" [ "$(field "$st" code)" = no_internet ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    ip netns exec "$NS" iptables -D INPUT -p icmp -j DROP
    check "back on home" wait_on home
    st="$(answer "$(req home "$HOME_PW")")"
    check "no bridge, the gateway answering: $st" [ "$(field "$st" code)" = connected ]
    set_bridge "$BRIDGE_HOST"
    : > "$T/bridge.log"
    st="$(answer "$(req home "$HOME_PW")")"
    check "the bridge reached through the new network: $st" [ "$(field "$st" code)" = connected ]
    local wifi_ip
    wifi_ip="$(nmcli -g IP4.ADDRESS device show "$DEV_IF" | cut -d/ -f1)"
    check "the bridge saw the Wi-Fi's address ($wifi_ip): $(sort -u "$T/bridge.log" | tr '\n' ' ')" grep -qx "$wifi_ip" "$T/bridge.log"
    # Owned by someone else or writable by others: not read (gateway).
    chmod 0666 "$INI"
    ip netns exec "$NS" iptables -I INPUT -d "$BRIDGE_IP" -p tcp --dport 443 -j DROP
    st="$(answer "$(req home "$HOME_PW")")"
    check "a config others can write is ignored (the gateway checked instead): $st" [ "$(field "$st" code)" = connected ]
    ip netns exec "$NS" iptables -D INPUT -d "$BRIDGE_IP" -p tcp --dport 443 -j DROP
    set_bridge "$BRIDGE_HOST"
}

s09() {
    section "9. a Wi-Fi restart holding the lock: busy"
    baseline || return
    clear_history
    ( flock -x 9; sleep 25 ) 9>>/run/otc-wifi.lock &
    holder=$!
    sleep 1
    local st
    st="$(answer "$(req home "$HOME_PW")")"
    check "busy: $st" [ "$(field "$st" code)" = busy ]
    check "never left" [ "$(field "$st" left)" = false ]
    wait "$holder"; holder=
}

s11() {
    section "11. stopped in the middle: interrupted, the previous network back"
    baseline || return
    clear_history
    local id st
    id="$(req home wrong-password-11)"
    wait_trial
    sleep 1
    systemctl stop otc-wifi-join.service
    st="$(answer "$id" 60)"
    check "interrupted: $st" [ "$(field "$st" code)" = interrupted ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    check "back on home" wait_on home
    check "no trial left" no_trial_left
    check "state.json cleared" [ ! -e "$STATE" ]
}

s19() {
    section "19a. killed outright (systemctl kill -s KILL) while joining, then --stopped by hand"
    baseline || return
    clear_history
    local id st u
    u="$(our_uuid)"
    id="$(req home wrong-password-19)"
    wait_trial
    sleep 1
    systemctl kill -s KILL otc-wifi-join.service
    unit_idle
    sleep 5
    st="$(runuser -u otc -- cat "$STATUS")"
    if [ "$(field "$st" state)" = joining ] && [ -e "$STATE" ]; then
        info "systemctl kill killed ExecStopPost (--stopped) too: the status still says joining, state.json and the trial are left"
    else
        info "ExecStopPost ran after systemctl kill: $st"
    fi
    check "the path unit still active after the kill" [ "$(systemctl is-active otc-wifi-join.path)" = active ]
    /usr/local/bin/otc-wifi-join-runner --stopped
    st="$(runuser -u otc -- cat "$STATUS")"
    check "--stopped by hand: interrupted ($st)" [ "$(field "$st" id)/$(field "$st" code)" = "$id/interrupted" ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    check "back on home" wait_on home
    check "the same OTC-WiFi, saved" bash -c '[ "$(nmcli -t -f NAME,UUID connection show | sed -n "s/^OTC-WiFi://p")" = "'"$u"'" ]'
    check "no trial left" no_trial_left
    check "state.json cleared" [ ! -e "$STATE" ]

    section "19a'. killed outright on the unsaved new network, nothing run after: the next change puts it right first"
    baseline || return
    clear_history
    u="$(our_uuid)"
    # The trial up and the bridge check under way (made slow) when killed.
    ip netns exec "$NS" iptables -I INPUT -d "$BRIDGE_IP" -p tcp --dport 443 -j DROP
    id="$(req home "$HOME_PW")"
    local d=$(( SECONDS + 120 ))
    until nmcli -t -f NAME connection show --active | grep -qx OTC-WiFi-new || [ "$SECONDS" -ge "$d" ]; do sleep 0.3; done
    sleep 2
    systemctl kill -s KILL otc-wifi-join.service
    unit_idle
    ip netns exec "$NS" iptables -D INPUT -d "$BRIDGE_IP" -p tcp --dport 443 -j DROP
    sleep 3
    echo "  left behind: status $(runuser -u otc -- cat "$STATUS"); state.json $(cat "$STATE" 2>/dev/null || echo none); wlan0 on '$(nmcli -g GENERAL.CONNECTION device show "$DEV_IF")'"
    check "left on the unsaved trial (as a run killed with its clean-up would leave it)" bash -c 'nmcli -t -f NAME connection show --active | grep -qx OTC-WiFi-new && [ -e '"$STATE"' ]'
    st="$(answer "$(req home "$HOME_PW")")"
    check "the next change: $st" [ "$(field "$st" code)" = connected ]
    check "it put the cut-short change right first" bash -c 'journalctl -u otc-wifi-join --since "-5min" --no-pager -o cat | grep -q "a change cut short earlier is put right first"'
    check "one OTC-WiFi, saved under /etc" saved_under_etc
    check "no trial left" no_trial_left
    check "state.json cleared" [ ! -e "$STATE" ]
    check "on home" wait_on home

    section "19b. killed right after the new profile was saved, before the commit was recorded"
    baseline || return
    clear_history
    u="$(our_uuid)"
    kill_wrapper_on
    echo "connection.id OTC-WiFi" > "$T/kill-at"
    id="$(req home "$HOME_PW")"
    st="$(answer "$id")"
    echo "  state.json when killed: $(cat "$T/state-at-kill" 2>/dev/null)"
    check "the kill came after the save (state without a commit)" bash -c '! grep -q committed '"$T"'/state-at-kill && grep -q trial_uuid '"$T"'/state-at-kill'
    check "--stopped finished it: $st" [ "$(field "$st" code)" = connected ]
    check "one OTC-WiFi, the new one, saved ($(our_uuid), was $u)" bash -c '[ "$(nmcli -t -f NAME connection show | grep -cx OTC-WiFi)" = 1 ] && [ "$(nmcli -t -f NAME,UUID connection show | sed -n "s/^OTC-WiFi://p")" != "'"$u"'" ]'
    check "saved under /etc" saved_under_etc
    check "on home" wait_on home
    check "state.json cleared" [ ! -e "$STATE" ]

    section "19c. killed after the commit was recorded, while the old profile was being deleted"
    clear_history
    u="$(our_uuid)"
    echo "connection delete uuid" > "$T/kill-at"
    id="$(req home "$HOME_PW")"
    st="$(answer "$id")"
    echo "  state.json when killed: $(cat "$T/state-at-kill" 2>/dev/null)"
    check "the kill came after the commit marker" grep -q '"committed"' "$T/state-at-kill"
    check "--stopped finished it: $st" [ "$(field "$st" code)" = connected ]
    check "one OTC-WiFi, the new one, saved" bash -c '[ "$(nmcli -t -f NAME connection show | grep -cx OTC-WiFi)" = 1 ] && [ "$(nmcli -t -f NAME,UUID connection show | sed -n "s/^OTC-WiFi://p")" != "'"$u"'" ]'
    check "saved under /etc" saved_under_etc
    check "on home" wait_on home
    kill_wrapper_off
}

# put_as_otc <content> [padding]: a request the otc user writes whole
# (a temporary file renamed into place), with padding spaces after it.
put_as_otc() {
    runuser -u otc -- python3 -I -c '
import os, sys
tmp = "/run/otc/.plant-tmp"
with open(tmp, "w", encoding="utf-8", errors="surrogatepass") as f:
    f.write(sys.argv[1] + " " * int(sys.argv[2]))
os.rename(tmp, "/run/otc/wifi-join.request")
' "$1" "${2:-0}"
}

# plant <what> <command...>: a request planted by hand; the runner must
# answer bad_request and open nothing else.
plant() {
    local what="$1" d st; shift
    rm -f "$STATUS"
    "$@"
    d=$(( SECONDS + 30 ))
    until [ -s "$STATUS" ] || [ "$SECONDS" -ge "$d" ]; do sleep 0.5; done
    unit_idle
    st="$(cat "$STATUS" 2>/dev/null)"
    check "$what: bad_request ($st)" [ "$(field "$st" code)" = bad_request ]
}

s10() {
    section "10. planted requests: refused, nothing else opened"
    baseline || return
    local shadow good_id=0123456789abcdef
    shadow="$(sha256sum /etc/shadow)"
    # A FIFO only root may open: a writer waits on it, so any open shows.
    rm -f "$T/probe.fifo" "$T/probe.opened"
    mkfifo -m 0600 "$T/probe.fifo"
    ( timeout 40 bash -c "echo x > $T/probe.fifo" && touch "$T/probe.opened" ) &
    clear_history
    plant "a symlink to a FIFO" runuser -u otc -- ln -s "$T/probe.fifo" /run/otc/wifi-join.request
    sleep 2
    check "the symlink's target was never opened" [ ! -e "$T/probe.opened" ]
    plant "a symlink to /etc/shadow" runuser -u otc -- ln -s /etc/shadow /run/otc/wifi-join.request
    check "/etc/shadow untouched" [ "$(sha256sum /etc/shadow)" = "$shadow" ]
    plant "a FIFO" runuser -u otc -- mkfifo /run/otc/wifi-join.request
    plant "a directory" runuser -u otc -- mkdir /run/otc/wifi-join.request
    check "the directory went too" [ ! -e /run/otc/wifi-join.request ]
    plant "a file root wrote" bash -c "printf '{\"v\":1,\"id\":\"$good_id\",\"ssid\":\"home\",\"psk\":\"\",\"hidden\":false}' > /run/otc/wifi-join.request"
    local good='{"v":1,"id":"'$good_id'","ssid":"home","psk":"","hidden":false}'
    plant "over 1 KiB (whitespace after good JSON)" put_as_otc "$good" 1100
    runuser -u otc -- python3 -I -c 'import sys; open("/run/otc/.hl-test", "w").write(sys.argv[1])' "$good"
    plant "a second hard link kept" runuser -u otc -- ln /run/otc/.hl-test /run/otc/wifi-join.request
    rm -f /run/otc/.hl-test
    plant "a duplicate key" put_as_otc '{"v":1,"id":"'$good_id'","ssid":"home","ssid":"elsewhere","psk":"","hidden":false}'
    plant "an unknown key" put_as_otc '{"v":1,"id":"'$good_id'","ssid":"home","psk":"","hidden":false,"x":1}'
    check "  answered with its id" [ "$(field "$(cat "$STATUS")" id)" = "$good_id" ]
    plant "v 2" put_as_otc '{"v":2,"id":"'$good_id'","ssid":"home","psk":"","hidden":false}'
    plant "v true" put_as_otc '{"v":true,"id":"'$good_id'","ssid":"home","psk":"","hidden":false}'
    plant "a tab in the password" put_as_otc '{"v":1,"id":"'$good_id'","ssid":"home","psk":"pass\tword1","hidden":false}'
    plant "a lone surrogate in the name" put_as_otc '{"v":1,"id":"'$good_id'","ssid":"\ud800","psk":"","hidden":false}'
    plant "a 33-byte name" put_as_otc '{"v":1,"id":"'$good_id'","ssid":"'"$(printf 'x%.0s' $(seq 33))"'","psk":"","hidden":false}'
    plant "a 7-character password" put_as_otc '{"v":1,"id":"'$good_id'","ssid":"home","psk":"1234567","hidden":false}'
    plant "NaN" put_as_otc '{"v":NaN,"id":"'$good_id'","ssid":"home","psk":"","hidden":false}'
    check "nothing left in root's directory but its own files ($(ls -A /run/otc-wifi-join | tr '\n' ' '))" bash -c '! ls -A /run/otc-wifi-join | grep -qvE "^(status.json|history)$"'
    check "still on home" [ "$(on_ssid)" = home ]
    check "no trial" no_trial_left
    rm -f "$T/probe.fifo"
}

s20() {
    section "20. an update asked for or running: update_running"
    baseline || return
    clear_history
    local st
    runuser -u otc -- touch /var/lib/otc/update.request
    st="$(answer "$(req home "$HOME_PW")")"
    check "an update request waiting: $st" [ "$(field "$st" code)" = update_running ]
    rm -f /var/lib/otc/update.request
    ( flock -s 9; sleep 20 ) 9>>/run/otc-update.lock &
    holder=$!
    sleep 1
    st="$(answer "$(req home "$HOME_PW")")"
    check "update.sh holding its lock: $st" [ "$(field "$st" code)" = update_running ]
    wait "$holder"; holder=
    check "still on home, never left" [ "$(on_ssid)" = home ]
}

s15() {
    section "15. a stray file in RAM for the saved profile: the profile stays"
    baseline || return
    clear_history
    local stray=$NMRUN/otc-wifi-0badf00d.nmconnection st
    install -m 0600 "$(our_file)" "$stray"
    nmcli connection reload
    st="$(answer "$(req elsewhere some-password-15)")"
    check "not found: $st" [ "$(field "$st" code)" = not_found ]
    check "the stray file removed" [ ! -e "$stray" ]
    check "one OTC-WiFi, saved under /etc ($(our_file))" saved_under_etc
    check "still on home" wait_on home
}

s16() {
    section "16. an open copy of the secured network's name"
    baseline || return
    ap "home|wpa2|$HOME_PW" "home|open||nodhcp"
    sleep 3
    nmcli -t -f SSID,SECURITY device wifi list ifname "$DEV_IF" --rescan yes 2>/dev/null | grep '^home:' | sed 's/^/  seen: /'
    clear_history
    local st
    st="$(answer "$(req home "$HOME_PW")")"
    check "joined the secured one: $st" [ "$(field "$st" code)" = connected ]
    check "saved with key-mgmt=wpa-psk" grep -q '^key-mgmt=wpa-psk' "$(our_file)"
    check "on a secured access point" bash -c 'nmcli -t -f ACTIVE,SECURITY device wifi list ifname '"$DEV_IF"' --rescan no | grep -q "^yes:WPA2"'
    st="$(answer "$(req home "")")"
    check "no password: refused, not joined open: $st" [ "$(field "$st" code)" = password_required ]
    check "still on home" [ "$(on_ssid)" = home ]
}

s05() {
    section "5. a hidden network, typed by hand"
    baseline || return
    ap "home|wpa2|$HOME_PW" "hidden-net|wpa2|hidden-password-5|hidden"
    wait_on home 30
    clear_history
    local st
    st="$(answer "$(req hidden-net hidden-password-5)")"
    check "not ticked as hidden: not_found ($st)" [ "$(field "$st" code)" = not_found ]
    st="$(answer "$(req hidden-net hidden-password-5 1)")"
    check "hidden joined: $st" [ "$(field "$st" code)" = connected ]
    check "saved with hidden=true" grep -q '^hidden=true' "$(our_file)"
    check "on hidden-net" [ "$(on_ssid)" = hidden-net ]
}

s06() {
    section "6. WPA3 only (SAE), with a UTF-8 password"
    ap "wpa3-net|sae|sae-pässwörd-€6"
    clear_history
    local st
    st="$(answer "$(req wpa3-net 'sae-pässwörd-€6')")"
    check "SAE joined: $st" [ "$(field "$st" code)" = connected ]
    check "saved with key-mgmt=sae" grep -q '^key-mgmt=sae' "$(our_file)"
    check "the password kept byte for byte" [ "$(nm_get 802-11-wireless-security.psk OTC-WiFi)" = 'sae-pässwörd-€6' ]
    st="$(answer "$(req wpa3-net 'sae-wrong-€')")"
    # NetworkManager says "could not be found" for it: the runner, having
    # just seen the network, makes it auth_failed (check the password).
    check "SAE wrong password: auth_failed ($st)" [ "$(field "$st" code)" = auth_failed ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    check "back on wpa3-net" wait_on wpa3-net 30
}

s07() {
    section "7. an open network"
    ap "open-net|open"
    clear_history
    local st
    st="$(answer "$(req open-net "")")"
    check "open joined: $st" [ "$(field "$st" code)" = connected ]
    check "saved without a [wifi-security]" bash -c '! grep -q "^\[wifi-security\]" "$0"' "$(our_file)"
    st="$(answer "$(req open-net some-password-7)")"
    check "a password for an open network: refused ($st)" [ "$(field "$st" code)" = open_network ]
    check "never left" [ "$(field "$st" left)" = false ]
}

s08() {
    section "8. a name and a password with spaces at both ends, \\ ; # = [ \" and Unicode"
    local odd_ssid=' sp\ce;#=[é☕ ' odd_psk='  p\ss w;#=[ñ€"x  ' st
    ap "$odd_ssid|wpa2|$odd_psk"
    clear_history
    st="$(answer "$(req "$odd_ssid" "$odd_psk")")"
    check "odd name and password joined: $st" [ "$(field "$st" code)" = connected ]
    check "on the odd network, byte for byte" [ "$(on_hex)" = "$(hex_of "$odd_ssid")" ]
    check "the name NetworkManager keeps, byte for byte" [ "$(nm_get 802-11-wireless.ssid OTC-WiFi)" = "$odd_ssid" ]
    local kept
    kept="$(nm_get 802-11-wireless-security.psk OTC-WiFi)"
    check "the password NetworkManager keeps, byte for byte" [ "$kept" = "$odd_psk" ]
    [ "$kept" = "$odd_psk" ] || { echo "  kept: $(printf '%s' "$kept" | od -c | head -3 | tr '\n' ' ')"; nmcli -t -f NAME,UUID,FILENAME connection show | sed 's/^/    /'; }
    echo "  as saved in $(our_file):"
    keyfile_lines "$(our_file)" | grep -E '^    (ssid|psk)='
    systemctl restart NetworkManager
    local i
    for i in $(seq 90); do nm_connected && break; sleep 1; done
    check "read back the same after a NetworkManager restart" [ "$(nm_get 802-11-wireless-security.psk OTC-WiFi)/$(nm_get 802-11-wireless.ssid OTC-WiFi)" = "$odd_psk/$odd_ssid" ]
    check "and joins it again (the password read back from the file works)" [ "$(on_hex)" = "$(hex_of "$odd_ssid")" ]
}

s13() {
    section "13. a burst: ten bad requests, then the eleventh in 15 minutes is refused"
    baseline || return
    clear_history
    local i st
    for i in 1 2 3 4 5 6 7 8 9 10; do
        rm -f "$STATUS"
        put_as_otc "not json $i"
        local d=$(( SECONDS + 30 ))
        until [ -s "$STATUS" ] || [ "$SECONDS" -ge "$d" ]; do sleep 0.3; done
        unit_idle
    done
    check "ten bad requests counted ($(wc -l < /run/otc-wifi-join/history))" [ "$(wc -l < /run/otc-wifi-join/history)" = 10 ]
    st="$(answer "$(req home "$HOME_PW")")"
    check "too many: $st" [ "$(field "$st" code)" = too_many ]
    check "never left" [ "$(field "$st" left)" = false ]
    check "the path unit still active" [ "$(systemctl is-active otc-wifi-join.path)" = active ]
    clear_history
}

# probe <label> <ssid> <key-mgmt> <psk>: one join as the runner makes it
# (its own keyfile text, loaded from RAM), and nmcli's own words.
probe() {
    local label="$1" u path out rc
    u="$(cat /proc/sys/kernel/random/uuid)"
    path=$NMRUN/otc-hwsim-probe.nmconnection
    python3 -I -c "$runner_mod"'sys.stdout.write(m.keyfile_text(sys.argv[1], "wlan0", sys.argv[2], sys.argv[3], sys.argv[4], False).replace("id=OTC-WiFi-new", "id=otc-hwsim-probe"))' \
        "$u" "$2" "$3" "$4" > "$path"
    chmod 0600 "$path"
    nmcli connection load "$path"
    out="$(nmcli --wait 60 connection up uuid "$u" ifname "$DEV_IF" 2>&1 >/dev/null)"
    rc=$?
    nmcli connection delete uuid "$u" >/dev/null 2>&1
    rm -f "$path"
    echo "  $label: nmcli exit $rc: $out"
    local code
    code="$(classify "$out")"
    # A network its scan showed: "could not be found" is auth_failed.
    [ "$code" = not_found ] && code="auth_failed (not_found, but the network was in its scan)"
    echo "  $label: the runner reads it as $code"
}

s21() {
    section "21. NetworkManager's own words for failed joins (wrong password vs the rest)"
    baseline || return
    probe "WPA2, wrong password" home wpa-psk wrong-password-21
    nmcli --wait 40 connection up OTC-WiFi >/dev/null 2>&1
    ap "home|wpa2|$HOME_PW" "sae-net|sae|sae-password-21"
    wait_on home 30
    probe "WPA3-SAE, wrong password" sae-net sae wrong-password-21
    nmcli --wait 40 connection up OTC-WiFi >/dev/null 2>&1
    ap "home|wpa2|$HOME_PW" "acl-net|wpa2|acl-password-21|acl"
    wait_on home 30
    probe "WPA2, right password, the access point refusing the station" acl-net wpa-psk acl-password-21
    nmcli --wait 40 connection up OTC-WiFi >/dev/null 2>&1
    ap "home|wpa2|$HOME_PW" "nodhcp-net|wpa2|nodhcp-password-21|nodhcp"
    wait_on home 30
    probe "WPA2, right password, no DHCP server" nodhcp-net wpa-psk nodhcp-password-21
    nmcli --wait 40 connection up OTC-WiFi >/dev/null 2>&1
    wait_on home 30
    ap "home|wpa2|$HOME_PW"
    wait_on home 30
    kill -STOP "$(cat "$T/hostapd.pid")"
    probe "WPA2, right password, the access point frozen (beacons only)" home wpa-psk "$HOME_PW"
    kill -CONT "$(cat "$T/hostapd.pid")"
    nmcli --wait 40 connection up OTC-WiFi >/dev/null 2>&1
    wait_on home 60

    # The same through the runner: the code, and the way back.
    local st
    ap "home|wpa2|$HOME_PW" "acl-net|wpa2|acl-password-21|acl"
    wait_on home 30
    clear_history
    st="$(answer "$(req acl-net acl-password-21)")"
    check "runner, the access point refusing the station: auth_failed ($st)" [ "$(field "$st" code)" = auth_failed ]
    check "left and restored" [ "$(field "$st" left)/$(field "$st" restored)" = true/true ]
    check "back on home" wait_on home 60
    wait_on home 30
    ap "home|wpa2|$HOME_PW" "nodhcp-net|wpa2|nodhcp-password-21|nodhcp"
    wait_on home 30
    st="$(answer "$(req nodhcp-net nodhcp-password-21)")"
    check "no DHCP: no_address, back on home ($st)" [ "$(field "$st" code)/$(field "$st" restored)" = no_address/true ]
    wait_on home 30
    ap "home|wpa2|$HOME_PW"
}

s17() {
    section "17. the cable and the Wi-Fi on one network (the FAQ's way back)"
    baseline || return
    # The router: its Wi-Fi and a cable port bridged, one DHCP server. The
    # device: the cable up, its Wi-Fi profile left with the old password.
    ap_stop
    ip netns exec "$NS" ip link add br0 type bridge 2>/dev/null
    ip netns exec "$NS" ip link set br0 up
    ip link add otceth0 type veth peer name otceth1 2>/dev/null && ip link set otceth1 netns "$NS"
    ip netns exec "$NS" ip link set otceth1 master br0
    ip netns exec "$NS" ip link set otceth1 up
    AP_BRIDGE=br0 ap "home|wpa2|cable-password-17"
    nmcli device set otceth0 managed yes
    nmcli connection add type ethernet ifname otceth0 con-name otc-hwsim-cable ipv4.method auto ipv6.method ignore >/dev/null
    nmcli --wait 30 connection up otc-hwsim-cable >/dev/null
    sleep 5
    echo "  cable: $(nmcli -g IP4.ADDRESS device show otceth0), Wi-Fi: $(nmcli -g GENERAL.STATE device show "$DEV_IF")"
    set_bridge "$BRIDGE_HOST"
    clear_history
    local st
    : > "$T/bridge.log"
    st="$(answer "$(req home cable-password-17)")"
    check "joined over Wi-Fi with the cable in (bridge check): $st" [ "$(field "$st" code)" = connected ]
    echo "  Wi-Fi: $(nmcli -g IP4.ADDRESS device show "$DEV_IF"); the bridge saw: $(sort -u "$T/bridge.log" | tr '\n' ' ')"
    set_bridge ""
    st="$(answer "$(req home cable-password-17)")"
    check "joined over Wi-Fi with the cable in (gateway check): $st" [ "$(field "$st" code)" = connected ]
    # The worst case: the router forgetting where the device's addresses
    # are all the time, so both interfaces answer its ARP queries for the
    # Wi-Fi address. Reported, not counted.
    set_bridge "$BRIDGE_HOST"
    ( while sleep 0.3; do ip netns exec "$NS" ip neigh flush dev br0 2>/dev/null; done ) &
    local flusher=$!
    st="$(answer "$(req home cable-password-17)")"
    kill "$flusher" 2>/dev/null
    info "with the router's ARP table flushed every 0.3 s: $(field "$st" code)"
    nmcli connection delete otc-hwsim-cable >/dev/null 2>&1
    ip link del otceth0 2>/dev/null
    HOME_PW=cable-password-17
    ap_stop
    ip netns exec "$NS" ip link del br0 2>/dev/null
}

# A real power cut: the VM's power cut from inside (sysrq b - no sync, no
# shutdown) right after a chosen nmcli call. `powercut load|saved|retired`,
# then start the VM again and run `powercut_check`. The access point takes
# the old password and the new one, so whichever profile is left joins.
powercut() {
    local point="${1:-}"
    section "22. a power cut ($point)"
    baseline || return
    nmcli -t -f NAME,UUID,FILENAME connection show | grep '^OTC-WiFi' > /var/lib/otc-hwsim/before-powercut
    ap "home|wpa2|$HOME_PW|psk2=powercut-password-22"
    wait_on home 30
    clear_history
    kill_wrapper_on
    case "$point" in
        load) echo "connection load" > "$T/kill-at" ;;
        saved) echo "connection.id OTC-WiFi" > "$T/kill-at" ;;
        retired) echo "connection delete uuid" > "$T/kill-at" ;;
        *) die "powercut load|saved|retired" ;;
    esac
    echo powercut > "$T/kill-how"
    printf '%s\n%s\n' "$point" "$HOME_PW" > /var/lib/otc-hwsim/powercut
    sync
    echo "  asking; the VM loses its power when the runner passes '$point'"
    req home powercut-password-22 >/dev/null
    sleep 120
    fail "the power was not cut"
}

powercut_check() {
    local point pw
    point="$(sed -n 1p /var/lib/otc-hwsim/powercut 2>/dev/null)"
    pw="$(sed -n 2p /var/lib/otc-hwsim/powercut 2>/dev/null)"
    section "22. after a power cut ($point)"
    [ -n "$point" ] || { fail "no power cut recorded"; return; }
    echo "  before: $(tr '\n' ' ' < /var/lib/otc-hwsim/before-powercut)"
    echo "  after:  $(nmcli -t -f NAME,UUID,FILENAME connection show | grep '^OTC-WiFi' | tr '\n' ' ')"
    local f
    for f in /etc/NetworkManager/system-connections/*; do
        echo "    $f: $(stat -c %s "$f") bytes, $(grep -c . "$f") lines"
    done
    check "nothing of the trial in RAM" bash -c '! ls '"$NMRUN"'/otc-wifi-* >/dev/null 2>&1'
    check "at least one OTC-WiFi, every file whole" bash -c 'n=0; for f in /etc/NetworkManager/system-connections/*; do grep -q "^\[wifi\]" "$f" && grep -q "^uuid=" "$f" || exit 1; n=$((n+1)); done; [ $n -ge 1 ]'
    HOME_PW="$pw"
    ap "home|wpa2|$pw|psk2=powercut-password-22"
    check "NetworkManager joins home by itself with what was left" wait_on home 120
    info "profiles now: $(nmcli -t -f NAME,UUID connection show | grep '^OTC-WiFi' | tr '\n' ' ') - connected with $(nmcli -g GENERAL.CONNECTION device show "$DEV_IF")"
    rm -f /var/lib/otc-hwsim/powercut
    kill_wrapper_off
    # What the next change does with what the cut left.
    clear_history
    local st
    st="$(answer "$(req home "$pw")")"
    check "the next change works: $st" [ "$(field "$st" code)" = connected ]
    check "and leaves one OTC-WiFi ($(ours))" [ "$(ours)" = 1 ]
}

s23() {
    section "23. two OTC-WiFi, the other with a password the router no longer takes (what a power cut between the delete and the last sync leaves): NetworkManager still joins"
    baseline || return
    local good stale t0
    good="$(our_uuid)"
    # The worst order: the stale one tried first (a higher priority).
    nmcli connection add type wifi con-name OTC-WiFi ifname "$DEV_IF" ssid home wifi-sec.key-mgmt wpa-psk \
        wifi-sec.psk stale-password-23 connection.autoconnect yes connection.autoconnect-priority 11 >/dev/null 2>&1
    stale="$(nmcli -t -f NAME,UUID connection show | sed -n 's/^OTC-WiFi://p' | grep -vx "$good")"
    # As at a restart.
    systemctl restart NetworkManager
    t0=$SECONDS
    check "connected again by itself" wait_on home 180
    info "after $((SECONDS - t0)) s on $(nmcli -g GENERAL.CONNECTION device show "$DEV_IF") $(nmcli -t -f UUID,DEVICE connection show --active | grep ":$DEV_IF\$" | cut -d: -f1) (good $good, stale $stale)"
    journalctl -u NetworkManager --since "-200s" --no-pager -o cat | grep -E "auto-activating|no-secrets" | sed 's/^/    NM: /' | head -6
    nmcli connection delete uuid "$stale" >/dev/null 2>&1
    check "back to one OTC-WiFi" [ "$(ours)" = 1 ]
}

journal() {
    section "the journal names no network and no password"
    check "journal clean" bash -c '! journalctl -u otc-wifi-join --no-pager | grep -aE "right-pass|new-pass|wrong-pass|other-pass|sae-p|cable-pass|hidden-pass|acl-pass|nodhcp-pass|powercut-pass|some-pass|hidden-net|wpa3-net|open-net|acl-net|nodhcp-net|sae-net|sp.ce|elsewhere|\bhome\b|ñ"'
    journalctl -u otc-wifi-join --no-pager -o cat | grep '^\[otc-wifi-join\]' | sort | uniq -c | sort -rn | head -20 | sed 's/^/    /'
}

# --- run ---------------------------------------------------------------------
echo "NetworkManager $(nmcli --version | awk '{print $NF}'), $(dpkg-query -W -f '${Package} ${Version}' wpasupplicant 2>/dev/null), hostapd $(dpkg-query -W -f '${Version}' hostapd 2>/dev/null), kernel $(uname -r)"
mkdir -p /var/lib/otc-hwsim
setup_network
setup_runner
nmcli radio wifi on
if [ "$#" -eq 0 ]; then
    set -- s01 s12 s02 s03 s04 s14 s09 s11 s19 s10 s20 s15 s16 s05 s06 s07 s08 s13 s18 s21 s23 s17 journal
fi
case "$1" in
    powercut) powercut "${2:-}" ;;
    powercut_check) powercut_check ;;
    *) for s in "$@"; do "$s"; done ;;
esac
ap_stop
echo
echo "=== $failed failed"
exit "$failed"
