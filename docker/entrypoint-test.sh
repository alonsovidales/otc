#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Tests of docker/entrypoint.sh's own logic, on any Linux host - no Docker,
# no root: a copy of it with its paths in a temporary directory is loaded,
# and the database's tools and setpriv are stood in for.
#
#   bash docker/entrypoint-test.sh
#
#   1. A failed start waits before the container stops: 30 s, doubling with
#      each failure in a row up to 30 minutes, an hour at least after a
#      model download that failed; forgotten once the device has run 10
#      minutes; outside `run` (set-password, dump, restore), no wait.
#   2. The nightly database copy runs in the background, in a group of its
#      own: the loop goes on meanwhile, a second copy isn't started, one
#      asked for by hand meanwhile is refused, and a stop ends it - the
#      dump itself too - at once; a copy that ends is read at the next
#      look, said when it failed.
#   3. The device is started through otc-sandbox, setpriv and its
#      environment after it (without the binary, as before).
#   4. MariaDB, as_otc's commands and the log's tail run through otc-sandbox
#      too, once its check answered ok; without the binary, or where the
#      check fails (an emulated architecture), as before.
#
# The loaded script reads the variables set here (BACKOFF, APP_STARTED,
# LAST_DUMP) and calls the functions stood in for; `a && b || fail` is the
# check's own shape.
# shellcheck disable=SC2034,SC2329,SC2015
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
fail() { echo "FAIL: $*" >&2; exit 1; }
cleanup() {
    [ -s "$work/dump.pid" ] && kill -KILL "$(cat "$work/dump.pid")" 2>/dev/null
    rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/bin" "$work/storage" "$work/state" "$work/conf" "$work/share"
echo 169 > "$work/share/release"
sed -e "s#^STORAGE=/mnt/storage\$#STORAGE=$work/storage#" \
    -e "s#^STATE=/var/lib/otc\$#STATE=$work/state#" \
    -e "s#^CONF=/etc/otc\$#CONF=$work/conf#" \
    -e "s#^SHARE=/usr/share/otc\$#SHARE=$work/share#" \
    -e "s#/run/otc-dump.lock#$work/dump.lock#" \
    "$here/entrypoint.sh" > "$work/entrypoint.sh"
chmod 0755 "$work/entrypoint.sh"
for p in "STORAGE=$work/storage" "STATE=$work/state" "CONF=$work/conf" "SHARE=$work/share" "$work/dump.lock"; do
    grep -qF "$p" "$work/entrypoint.sh" || fail "the copy has no $p"
done

# setpriv: the command after its options, as this user. mariadb-dump as
# FAKE_DUMP says: slow (its pid kept, then a minute's sleep), ok, or fail.
cat > "$work/bin/setpriv" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
    case "$1" in
        --reuid|--regid|--ruid|--rgid|--euid|--egid|--groups) shift 2 ;;
        --*) shift ;;
        *) break ;;
    esac
done
exec "$@"
EOF
cat > "$work/bin/mariadb-admin" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$work/bin/mysql" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$work/bin/mariadb-dump" <<EOF
#!/bin/sh
case "\${FAKE_DUMP:-ok}" in
    slow) echo \$\$ > "$work/dump.pid"; exec sleep 60 ;;
    fail) echo "mariadb-dump: Got error" >&2; exit 2 ;;
    *) echo "-- dump of \$*" ;;
esac
EOF
chmod 0755 "$work/bin"/*
export PATH="$work/bin:$PATH"

# shellcheck disable=SC1091
source "$work/entrypoint.sh"

# --- 1. A failed start waits before the container stops --------------------
pause() { echo "$1" >> "$work/paused"; }
stop_all() { :; }
last_pause() { tail -n 1 "$work/paused" 2>/dev/null; }
BACKOFF=1
for want in 30 60 120 240 480 960 1800 1800; do
    rc=0
    ( die "boom" ) >/dev/null 2>&1 || rc=$?
    [ "$rc" = 1 ] || fail "die ended with $rc"
    [ "$(last_pause)" = "$want" ] || fail "waited $(last_pause) s, not $want"
done
[ "$(cat "$START_FAILS")" = 8 ] || fail "the count is $(cat "$START_FAILS")"
said=$( ( die "boom" ) 2>&1 )
case "$said" in
    *"ERROR: boom"*"stops in 1800 s"*"failed start 9 in a row"*) ;;
    *) fail "die said: $said" ;;
esac
# A model that keeps failing: an hour at least, whatever the count.
rm -f "$START_FAILS"
( download_failed "could not download the model" ) >/dev/null 2>&1
[ "$(last_pause)" = 3600 ] || fail "a failed download waited $(last_pause) s"
# Forgotten once the device has run 10 minutes, not before.
echo 3 > "$START_FAILS"
APP_STARTED=$(date +%s)
start_succeeded
[ -e "$START_FAILS" ] || fail "forgotten after a few seconds"
APP_STARTED=$(( $(date +%s) - STABLE_S ))
start_succeeded
[ ! -e "$START_FAILS" ] || fail "not forgotten after $STABLE_S s"
( die "boom" ) >/dev/null 2>&1
[ "$(last_pause)" = 30 ] || fail "after a good run, waited $(last_pause) s"
# A count that isn't a number, or a link: as none.
rm -f "$START_FAILS"; echo nonsense > "$START_FAILS"
( die "boom" ) >/dev/null 2>&1
[ "$(last_pause)" = 30 ] || fail "with a bad count, waited $(last_pause) s"
# Outside run: no wait.
BACKOFF=0
: > "$work/paused"
( die "boom" ) >/dev/null 2>&1
[ ! -s "$work/paused" ] || fail "a die outside run waited"
echo "ok: a failed start waits, and longer each time"

# --- 2. The nightly copy in the background ----------------------------------
pause() { sleep "$1" & wait $! 2>/dev/null; }
date() { case "${1:-}" in +%H) echo 03 ;; *) command date "$@" ;; esac; }
wait_dump() {
    local i
    for ((i = 0; i < 100; i++)); do
        alive "$DUMP_PID" || return 0
        sleep 0.1
    done
    fail "the copy didn't end"
}

export FAKE_DUMP=slow
LAST_DUMP=
t=$(command date +%s)
nightly_dump
[ -n "$DUMP_PID" ] && alive "$DUMP_PID" || fail "no copy started"
[ $(( $(command date +%s) - t )) -le 2 ] || fail "the loop waited for the copy"
for ((i = 0; i < 50; i++)); do [ -s "$work/dump.pid" ] && break; sleep 0.1; done
dump=$(cat "$work/dump.pid" 2>/dev/null)
[ -n "$dump" ] && kill -0 "$dump" 2>/dev/null || fail "the dump isn't running"
first=$DUMP_PID
nightly_dump
[ "$DUMP_PID" = "$first" ] || fail "a second copy was started"
said=$("$work/entrypoint.sh" dump 2>&1) && fail "a copy by hand ran beside the nightly one"
case "$said" in *"a database copy is being written already"*) ;; *) fail "by hand: $said" ;; esac
t=$(command date +%s)
stop_dump >/dev/null
[ -z "$DUMP_PID" ] || fail "the copy is still known"
[ $(( $(command date +%s) - t )) -le 4 ] || fail "stopping the copy took $(( $(command date +%s) - t )) s"
kill -0 "$dump" 2>/dev/null && fail "the dump outlived the stop"
alive "$first" && fail "the copy outlived the stop"

export FAKE_DUMP=ok
LAST_DUMP=
nightly_dump
wait_dump
nightly_dump > "$work/said"
[ -z "$DUMP_PID" ] || fail "an ended copy is still known"
grep -q WARNING "$work/said" && fail "a good copy was said to fail: $(cat "$work/said")"
day=$(command date +%F)
[ -s "$work/storage/db-dumps/$day/otc.sql.gz" ] && [ -e "$work/storage/db-dumps/$day/.complete" ] \
    || fail "no complete copy in db-dumps/$day: $(ls -la "$work/storage/db-dumps/$day" 2>&1)"
gzip -dc "$work/storage/db-dumps/$day/otc.sql.gz" | grep -q -- "-- dump of" || fail "the copy holds no dump"
# Asked again in that hour: once a day.
nightly_dump
[ -z "$DUMP_PID" ] || fail "a second copy the same day"

export FAKE_DUMP=fail
LAST_DUMP=
nightly_dump
wait_dump
nightly_dump > "$work/said"
grep -q "WARNING: the nightly database copy failed" "$work/said" || fail "a failed copy wasn't said: $(cat "$work/said")"
echo "ok: the nightly copy runs beside the loop, and a stop ends it"

# --- 3. The device started through its system-call filter ------------------
# setsid stood in for: what start_app would run, written down.
printf '#!/bin/sh\nprintf "%%s\\n" "$*" > "%s"\n' "$work/started" > "$work/bin/setsid"
printf '#!/bin/sh\n' > "$work/bin/otc-sandbox"
chmod 0755 "$work/bin/setsid" "$work/bin/otc-sandbox"
install_live_config() { :; }
read_ports
SANDBOX=$work/bin/otc-sandbox
MIRROR_ARRAY=md0 MIRROR_DEV1=8:16 MIRROR_DEV2=8:32 MIRROR_LABEL1=a MIRROR_LABEL2=b
start_app >/dev/null
wait "$APP_PID"
want="env --default-signal HOME=$work/state USER=otc LOGNAME=otc OTC_MIRROR_ARRAY=md0 OTC_MIRROR_DEV1=8:16 OTC_MIRROR_DEV2=8:32 OTC_MIRROR_LABEL1=a OTC_MIRROR_LABEL2=b $work/bin/otc-sandbox setpriv --reuid 7310 --regid 7310 --init-groups --no-new-privs /usr/bin/otc dev"
[ "$(cat "$work/started")" = "$want" ] || fail "the device was started as: $(cat "$work/started")"
SANDBOX=$work/none
MIRROR_ARRAY=
start_app >/dev/null
wait "$APP_PID"
want="env --default-signal HOME=$work/state USER=otc LOGNAME=otc setpriv --reuid 7310 --regid 7310 --init-groups --no-new-privs /usr/bin/otc dev"
[ "$(cat "$work/started")" = "$want" ] || fail "without otc-sandbox, the device was started as: $(cat "$work/started")"
echo "ok: the device starts under otc-sandbox"

# --- 4. MariaDB and the otc user's helpers under the filter too ------------
# otc-sandbox stood in for: its check answers as FAKE_PROBE says, and what it
# runs is written down before it is run.
cat > "$work/bin/otc-sandbox" <<EOF
#!/bin/sh
[ "\$1" = --probe ] && { echo "\${FAKE_PROBE:-ok}"; exit 0; }
printf '%s\n' "\$*" >> "$work/sandboxed"
exec "\$@"
EOF
# mariadbd: its arguments written down, running until it is stopped.
cat > "$work/bin/mariadbd" <<EOF
#!/bin/sh
printf '%s\n' "\$*" > "$work/mariadbd"
exec sleep 30
EOF
printf '#!/bin/sh\necho "This installation is already upgraded: no need to run mariadb-upgrade"\n' > "$work/bin/mariadb-upgrade"
chmod 0755 "$work/bin/otc-sandbox" "$work/bin/mariadbd" "$work/bin/mariadb-upgrade"
install() { :; }
mkdir -p "$DATADIR/mysql"
SANDBOX=$work/bin/otc-sandbox
export FAKE_PROBE=ok
probe_sandbox
[ "${SANDBOXED[*]}" = "$SANDBOX" ] || fail "otc-sandbox's check said ok, but it isn't used: '${SANDBOXED[*]}'"
: > "$work/sandboxed"
[ "$(as_otc echo hello)" = hello ] || fail "as_otc under otc-sandbox"
[ "$(cat "$work/sandboxed")" = "setpriv --reuid 7310 --regid 7310 --init-groups --no-new-privs echo hello" ] \
    || fail "as_otc ran: $(cat "$work/sandboxed")"
: > "$work/sandboxed"
start_mariadb >/dev/null
mariadb_pid=$DB_PID
for ((i = 0; i < 50; i++)); do [ -s "$work/mariadbd" ] && break; sleep 0.1; done
grep -q "^mariadbd --user=mysql --port=$DB_PORT " "$work/sandboxed" \
    || fail "MariaDB wasn't started through otc-sandbox: $(cat "$work/sandboxed")"
grep -q "^--user=mysql --port=$DB_PORT --innodb-buffer-pool-size=" "$work/mariadbd" || fail "mariadbd got: $(cat "$work/mariadbd")"
kill -TERM "$mariadb_pid" 2>/dev/null
stop_mariadb
# A check that fails, or no binary: everything as before, without it.
for how in fails missing; do
    if [ "$how" = fails ]; then FAKE_PROBE="seccomp: invalid argument"; else SANDBOX=$work/none; fi
    probe_sandbox
    [ "${#SANDBOXED[@]}" = 0 ] || fail "otc-sandbox is used though its check $how: '${SANDBOXED[*]}'"
    : > "$work/sandboxed"
    [ "$(as_otc echo hello)" = hello ] || fail "as_otc without otc-sandbox ($how)"
    [ ! -s "$work/sandboxed" ] || fail "as_otc went through otc-sandbox ($how): $(cat "$work/sandboxed")"
done
echo "ok: MariaDB and the otc user's helpers run under otc-sandbox"
echo "PASS"
