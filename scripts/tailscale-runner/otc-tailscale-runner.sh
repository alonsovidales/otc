#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of Tailscale Funnel (issue #80), and since issue #204 the
# only thing on the device that changes anything in tailscaled.
#
# tailscaled runs as root, and only while Funnel is on. The device
# (otc.service, unprivileged with NoNewPrivileges) can't start a system
# service, and it no longer drives tailscaled either: releases before 168
# made the otc user tailscaled's operator, which let the service do
# everything the CLI does - change the account or the tailnet, routes and
# exit nodes, serve, Tailscale SSH - when all it needs is Funnel on its own
# web port. Now only root is, and the device asks this runner for one of
# three things in /var/lib/otc/tailscale.request (tailscalefunnel):
#
# on:     start and enable tailscaled, take the otc user's operator away if
#         an older release left it, join the tailnet if the node hasn't -
#         with the owner's auth key when the device left one in
#         /run/otc/tailscale-auth.key - and publish the device:
#         `tailscale funnel --bg --yes --https=443 localhost:8080`. A node
#         that still needs a person to authorise it stops before Funnel,
#         with its login page in the answer.
# off:    `tailscale funnel reset`, then stop and disable tailscaled. The
#         node stays in the tailnet (its state is kept); it is just not
#         running until Funnel is turned on again.
# status: what tailscaled says, changing nothing.
#
# The answer goes to /var/lib/otc/tailscale-status.json with what Settings
# shows: whether the node has joined (logged_in), whether Funnel publishes
# the device (funnel_on), its public address (public_url) and a login page
# waiting for a person (login_url).
#
# --drop-operator (install.sh, release 168's script) only takes the otc
# user's operator away. A stopped tailscaled whose saved state still names
# otc is started for as long as that takes, so no later "on" starts a
# daemon the service can drive.
#
# Trust boundary: the request is one of three words, and nothing from it
# reaches a command line; every command line here is fixed. The auth key is
# taken only from a regular file of the otc user's with one link, renamed
# first into this unit's own directory (root's, 0700, on the /run tmpfs) so
# the otc user can no longer reach the name, and only when it is printable
# ASCII. It reaches `tailscale up` as a file of root's (--auth-key=file:),
# never on a command line - every account can read those in /proc - and
# never in a log or the answer.
set -uo pipefail
export LC_ALL=C

REQUEST=/var/lib/otc/tailscale.request
STATUS_FILE=/var/lib/otc/tailscale-status.json
AUTH_KEY_FILE=/run/otc/tailscale-auth.key
STATE_FILE=/var/lib/tailscale/tailscaled.state
# The device's own plain HTTP server, which Funnel publishes on 443 (it
# takes only 443, 8443 or 10000, and 443 needs no port in the URL).
# tailscaled terminates TLS itself.
LOCAL_PORT=8080
PUBLIC_PORT=443

# ts <seconds> <args>...: the tailscale CLI, killed if it takes longer - a
# wedged daemon must not keep the unit activating (TimeoutStartSec=3min).
ts() {
    local secs="$1"
    shift
    timeout -k 5 "$secs" tailscale "$@"
}

# The fields of `tailscale status --json` this needs, one per line.
STATUS_PY='import json, sys
d = json.load(sys.stdin)
me = d.get("Self") if isinstance(d.get("Self"), dict) else {}
for v in (d.get("BackendState"), d.get("AuthURL"), me.get("DNSName")):
    print(v.replace("\n", " ") if isinstance(v, str) else "")'
# The operator in `tailscale debug prefs`.
OPERATOR_PY='import json, sys
v = json.load(sys.stdin).get("OperatorUser")
print(v if isinstance(v, str) else "")'
# The operators tailscaled's saved state names (a JSON object of base64
# values, a profile's preferences among them), one per line; exit 1 when
# the file can't be read.
STATE_PY='import base64, json, sys
try:
    with open(sys.argv[1], "rb") as f:
        values = list(json.load(f).values())
except Exception:
    sys.exit(1)
for v in values:
    try:
        p = json.loads(base64.b64decode(v, validate=True))
    except Exception:
        continue
    if isinstance(p, dict) and isinstance(p.get("OperatorUser"), str):
        print(p["OperatorUser"])'

# daemon_answers / wait_answers <seconds>: tailscaled answers (now / within
# that long - it takes a moment after a start).
daemon_answers() { ts 10 status --json >/dev/null 2>&1; }
wait_answers() {
    local _
    for _ in $(seq 1 "$1"); do
        daemon_answers && return 0
        sleep 1
    done
    return 1
}

# names_otc <names>: one of the account names (a line each) is the otc
# user's.
names_otc() {
    local name uid
    uid="$(id -u otc 2>/dev/null)" || return 1
    while IFS= read -r name; do
        [ -n "$name" ] && [ "$(id -u -- "$name" 2>/dev/null)" = "$uid" ] && return 0
    done <<< "$1"
    return 1
}

# otc_operator: the running tailscaled's operator is the otc user - release
# 2, install.sh and this runner made it one before release 168 - or can't
# be told, which counts as the same. An operator an owner chose by hand
# for an account of their own stays.
otc_operator() {
    local names
    names="$(ts 15 debug prefs 2>/dev/null | python3 -I -c "$OPERATOR_PY" 2>/dev/null)" || return 0
    names_otc "$names"
}

# clear_operator: takes the otc user's operator away from the running
# tailscaled; fails only when it is still there.
clear_operator() {
    otc_operator || return 0
    ts 15 set --operator= >/dev/null 2>&1 || return 1
    echo "took tailscaled's operator away from the otc user"
}

if [ "${1:-}" = --drop-operator ]; then
    command -v tailscale >/dev/null 2>&1 || { echo "Tailscale is not installed: no operator to take away"; exit 0; }
    if systemctl is-active --quiet tailscaled; then
        wait_answers 20 || { echo "tailscaled runs but doesn't answer: its operator could not be checked" >&2; exit 1; }
        clear_operator || { echo "tailscaled's operator could not be taken away from the otc user" >&2; exit 1; }
        exit 0
    fi
    if [ ! -s "$STATE_FILE" ]; then
        echo "tailscaled has no saved state: no operator to take away"
        exit 0
    fi
    if names="$(python3 -I -c "$STATE_PY" "$STATE_FILE" 2>/dev/null)" && ! names_otc "$names"; then
        echo "tailscaled's saved state names no operator of the otc user's"
        exit 0
    fi
    # Stopped (Funnel off) with otc still in its state, or a state that
    # can't be read: started for as long as it takes, then stopped again.
    rc=0
    if timeout 90 systemctl start tailscaled >/dev/null 2>&1 && wait_answers 20; then
        clear_operator || rc=1
    else
        rc=1
    fi
    timeout 90 systemctl stop tailscaled >/dev/null 2>&1 || true
    [ "$rc" -eq 0 ] || echo "tailscaled's operator could not be taken away from the otc user" >&2
    exit "$rc"
fi

# The directory is the otc user's: read without following a symlink and
# without blocking on a FIFO put in the file's place (as
# otc-update-stopped.sh reads its status), 8 bytes at most.
want="$(dd if="$REQUEST" iflag=nofollow,nonblock bs=8 count=1 status=none 2>/dev/null | tr -dc '[:lower:]')"
rm -f "$REQUEST"

# This unit's RuntimeDirectory (root's, 0700, removed when the run ends);
# made here when run by hand. /run is root's alone, so nothing can be
# planted there first.
WORK_DIR="${RUNTIME_DIRECTORY:-/run/otc-tailscale}"
[ -d "$WORK_DIR" ] || mkdir -m 0700 "$WORK_DIR" 2>/dev/null
if [ ! -d "$WORK_DIR" ] || [ -L "$WORK_DIR" ] || [ "$(stat -c '%u' "$WORK_DIR" 2>/dev/null)" != 0 ]; then
    WORK_DIR=""
else
    chmod 0700 "$WORK_DIR"
fi
trap '[ -z "$WORK_DIR" ] || rm -f "$WORK_DIR/auth-key" "$WORK_DIR/auth-key.taken"' EXIT

# The owner's auth key, when the device left one: taken whatever the
# request, so a key never outlives the request it came with, and used only
# by "on" on a node that hasn't joined. auth_key_bad: a file was there but
# held no usable key.
auth_key=""
auth_key_bad=false
take_auth_key() {
    local taken="$WORK_DIR/auth-key.taken" otc_uid meta re
    [ -e "$AUTH_KEY_FILE" ] || [ -L "$AUTH_KEY_FILE" ] || return 0
    auth_key_bad=true
    if [ -z "$WORK_DIR" ]; then
        rm -rf -- "$AUTH_KEY_FILE"
        return 0
    fi
    rm -rf -- "$taken"
    # Both are on the /run tmpfs: an atomic rename, which moves a symlink
    # rather than following it.
    if ! mv -T -- "$AUTH_KEY_FILE" "$taken" 2>/dev/null; then
        rm -rf -- "$AUTH_KEY_FILE"
        return 0
    fi
    otc_uid="$(id -u otc 2>/dev/null)"
    meta="$(stat -c '%F:%u:%h:%s' -- "$taken" 2>/dev/null)"
    re="^regular file:$otc_uid:1:([0-9]+)$"
    if [ -n "$otc_uid" ] && [[ "$meta" =~ $re ]] && [ "${BASH_REMATCH[1]}" -le 1024 ]; then
        auth_key="$(dd if="$taken" iflag=nofollow,nonblock bs=1024 count=1 status=none 2>/dev/null)"
    fi
    rm -rf -- "$taken"
    if [ "${#auth_key}" -gt 512 ] || ! [[ "$auth_key" =~ ^[[:graph:]]+$ ]]; then
        auth_key=""
        return 0
    fi
    auth_key_bad=false
}
take_auth_key

# observe: what tailscaled says now. running: it answers; backend: its
# BackendState (Running once the node has joined); auth_url: a login page
# waiting for a person; dns_name: the node's name; funnel_on: Funnel
# publishes the device's port. Status 1 when the daemon doesn't answer.
running=false backend="" auth_url="" dns_name="" funnel_on=false
observe() {
    local json fields serve
    running=false backend="" auth_url="" dns_name="" funnel_on=false
    # Stopped - Funnel off, most devices: the CLI would take seconds to
    # give up on the socket.
    systemctl is-active --quiet tailscaled || return 1
    json="$(ts 15 status --json --peers=false 2>/dev/null)" || return 1
    running=true
    fields="$(printf '%s' "$json" | python3 -I -c "$STATUS_PY" 2>/dev/null)" || return 0
    { IFS= read -r backend; IFS= read -r auth_url; IFS= read -r dns_name; } <<< "$fields"
    [[ "$backend" =~ ^[A-Za-z]{1,32}$ ]] || backend=""
    if ! [[ "$auth_url" =~ ^https?://[[:graph:]]+$ ]] || [ "${#auth_url}" -gt 1024 ]; then
        auth_url=""
    fi
    dns_name="${dns_name%.}"
    if ! [[ "$dns_name" =~ ^[A-Za-z0-9.-]+$ ]] || [ "${#dns_name}" -gt 253 ]; then
        dns_name=""
    fi
    # Only a node in the tailnet can be published. The wording of `funnel
    # status` has changed between releases: only the port is looked for.
    if [ "$backend" = Running ]; then
        serve="$(ts 15 funnel status 2>/dev/null)" || true
        [[ "$serve" != *":$LOCAL_PORT"* ]] || funnel_on=true
    fi
    return 0
}

# settle: observe until tailscaled has made up its mind - just started, it
# says Starting (or nothing yet) for a moment even for a node in the
# tailnet, and `tailscale up` there would only redo the sign-in.
settle() {
    local _
    for _ in $(seq 1 15); do
        observe || return 1
        case "$backend" in
            "" | NoState | Starting) sleep 1 ;;
            *) return 0 ;;
        esac
    done
    return 0
}

# json_string <value>: value as a JSON string. Backslashes and quotes are
# escaped and control characters dropped, so nothing leaves the string.
json_string() {
    local v="${1//\\/\\\\}"
    v="${v//\"/\\\"}"
    printf '"%s"' "$(printf '%s' "$v" | tr -d '\000-\037')"
}

# args <name> <value>...: a status code's arguments as a JSON object of
# strings.
args() {
    local out=""
    while [ "$#" -ge 2 ]; do
        out="${out:+$out,}\"$1\":$(json_string "$2")"
        shift 2
    done
    printf '{%s}' "$out"
}

# Temp file moved into place: the directory is the otc user's, and a plain
# "> $STATUS_FILE" as root would follow a symlink put there. dd's
# conv=excl (O_CREAT|O_EXCL) refuses a name planted ahead of it, umask 022
# leaves no chmod to redirect, and mv replaces a symlink, never follows it.
#
# status <state> <message> [<code> [<args>]], with what observe saw last:
# message is the English that devices from before codes show; code names
# the same text in the device's catalog (dev.status.tailscale.<code>,
# devtext's table) and args, from args() above, its arguments - so a newer
# device shows it in its owner's language (docs/i18n.md, "Text written
# before any request"). request is the word answered.
status() {
    local tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM" code="${3:-}" code_args="${4:-}" logged_in=false public_url=""
    [ -n "$code_args" ] || code_args='{}'
    if [ "$backend" = Running ]; then
        logged_in=true
        [ -z "$dns_name" ] || public_url="https://$dns_name"
    fi
    printf '{"state":"%s","message":%s,"code":"%s","args":%s,"request":"%s","running":%s,"logged_in":%s,"funnel_on":%s,"public_url":%s,"login_url":%s,"updated":"%s"}\n' \
        "$1" "$(json_string "$2")" "$code" "$code_args" "$want" "$running" "$logged_in" "$funnel_on" \
        "$(json_string "$public_url")" "$(json_string "$auth_url")" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

# fail <message> [<code> [<args>]]: answers failed, with what tailscaled
# says now, and stops.
fail() {
    echo "$want failed: $1" >&2
    observe
    status failed "$1" "${2:-}" "${3:-}"
    exit 1
}

# command_error <output> <exit status>: a tailscale command's failure in
# one line, worded as the device worded it before ("tailscale: exit status
# 1: <what it printed>"), without the auth key.
command_error() {
    local out="$1"
    [ -z "$auth_key" ] || out="${out//"$auth_key"/[auth key]}"
    out="$(printf '%s' "$out" | tr '\t\r\n' '   ' | tr -s ' ' | cut -c1-400)"
    out="${out# }"
    out="${out% }"
    case "$2" in
        124 | 137) printf 'tailscale timed out%s' "${out:+: $out}" ;;
        *) printf 'tailscale: exit status %s%s' "$2" "${out:+: $out}" ;;
    esac
}

# join: `tailscale up`, with the auth key as a file of root's. --timeout
# because without a key `up` waits for a person to authorise the node:
# bounded, it comes back, and the login stays pending in the daemon, where
# observe finds it as AuthURL. Sets join_out and join_rc.
join_out="" join_rc=0
join() {
    local key_file="$WORK_DIR/auth-key"
    local key_arg=()
    if [ -n "$auth_key" ]; then
        rm -f "$key_file"
        # printf is a builtin: the key is never any process's argument.
        if ! (umask 077; printf '%s' "$auth_key" > "$key_file"); then
            join_out="the auth key could not be handed over" join_rc=1
            return
        fi
        key_arg=("--auth-key=file:$key_file")
    fi
    join_out="$(ts 45 up --timeout=10s "${key_arg[@]}" 2>&1)"
    join_rc=$?
    rm -f "$key_file"
}

if ! command -v tailscale >/dev/null 2>&1; then
    status failed "Tailscale is not installed on this device" not_installed
    exit 1
fi

case "$want" in
on)
    if ! timeout 90 systemctl enable --now tailscaled >/dev/null 2>&1; then
        fail "tailscaled could not be started" start_failed
    fi
    wait_answers 20
    # A daemon the service could still drive doesn't stay up.
    if ! clear_operator; then
        echo "tailscaled's operator could not be taken away from the otc user: stopping it again" >&2
        timeout 90 systemctl disable --now tailscaled >/dev/null 2>&1 || true
        fail "tailscaled could not be started" start_failed
    fi
    settle
    if [ "$backend" != Running ]; then
        if $auth_key_bad; then
            fail "could not join the tailnet: the auth key could not be read" join_failed "$(args detail "the auth key could not be read")"
        fi
        join
        observe
        if [ "$backend" != Running ]; then
            # A key that didn't work says why; without one, a person
            # authorising the node on its login page is the way on.
            if [ -z "$auth_key" ]; then
                [ -n "$auth_url" ] || auth_url="$(grep -o 'https://login\.tailscale\.com/[^[:space:]]*' <<< "$join_out" | head -1)"
                if [ -n "$auth_url" ]; then
                    echo "on: the node waits for a person to authorise it"
                    status "done" "on"
                    exit 0
                fi
            fi
            detail="$(command_error "$join_out" "$join_rc")"
            fail "could not join the tailnet: $detail" join_failed "$(args detail "$detail")"
        fi
        echo "on: joined the tailnet"
    fi
    # --bg so it stays across restarts of the daemon and reboots.
    out="$(ts 30 funnel --bg --yes "--https=$PUBLIC_PORT" "localhost:$LOCAL_PORT" 2>&1)"
    rc=$?
    if [ "$rc" -ne 0 ]; then
        detail="$(command_error "$out" "$rc")"
        fail "could not enable Funnel: $detail" enable_failed "$(args detail "$detail")"
    fi
    observe
    echo "on: Funnel publishes port $LOCAL_PORT"
    status "done" "on"
    ;;
off)
    if observe; then
        out="$(ts 30 funnel reset 2>&1)"
        rc=$?
        if [ "$rc" -ne 0 ]; then
            detail="$(command_error "$out" "$rc")"
            fail "could not disable Funnel: $detail" disable_failed "$(args detail "$detail")"
        fi
    fi
    timeout 90 systemctl disable --now tailscaled >/dev/null 2>&1 || true
    observe
    echo "off: Funnel reset and tailscaled stopped"
    status "done" "off"
    ;;
status)
    observe
    status "done" "status"
    ;;
*)
    status failed "unknown request" unknown_request
    exit 1
    ;;
esac
