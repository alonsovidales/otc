#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# servercheck.sh - health and intrusion check of the bridge cluster
# (issue #144), run from the owner's Mac every 6 hours (launchd, see
# bridge/cluster/README.md) - not on the servers: a compromised server could
# tamper with a check that runs on it. This one only reads, over SSH, and
# compares each server's security fingerprint with a baseline kept here.
#
#   servercheck.sh            run the checks, notify if anything is wrong
#   servercheck.sh --mail     the same, and email the report (launchd's
#                             scheduled run; a run by hand only notifies)
#   servercheck.sh --accept   take the current fingerprints as the baseline
#                             (after a change you made on purpose)
set -uo pipefail

HOSTS=(bridge1 bridge2 redis)
# The report is emailed through Gmail with an app password kept in the
# macOS Keychain (service otc-servercheck-smtp, account MAILTO) - see
# bridge/cluster/README.md. Without it, only the notification.
MAILTO=vidales.miguelez@gmail.com
DIR="$HOME/Library/Application Support/otc-servercheck"
LOGDIR="$HOME/Library/Logs/otc-servercheck"
mkdir -p "$DIR/baseline" "$LOGDIR"
STAMP=$(date +%Y%m%d-%H%M)
REPORT="$LOGDIR/$STAMP.txt"
ACCEPT=0; MAIL=0
for a in "$@"; do
  case $a in
    --accept) ACCEPT=1 ;;
    --mail)   MAIL=1 ;;
  esac
done
PROBLEMS=()

say()  { echo "$*" >> "$REPORT"; }
bad()  { PROBLEMS+=("$*"); say "  !! $*"; }

# What each host must be running.
services_for() {
  case $1 in
    bridge1|bridge2) echo "otc_bridge mysql wg-quick@wg0 ufw veeammaservice" ;;
    redis)           echo "redis-server wg-quick@wg0 ufw" ;;
  esac
}

# Runs on the server (read-only, as root via sudo). Prints KEY<TAB>value
# lines: HEALTH lines are judged here, FP lines are the security
# fingerprint compared with the baseline.
remote_script='
set -u
h() { printf "HEALTH\t%s\t%s\n" "$1" "$2"; }
fp() { printf "FP\t%s\t%s\n" "$1" "$2"; }
for s in $SERVICES; do h "service:$s" "$(systemctl is-active $s 2>/dev/null)"; done
h disk_used_pct "$(df --output=pcent / | tail -1 | tr -dc 0-9)"
h load1 "$(cut -d" " -f1 /proc/loadavg)"
h mem_avail_mb "$(awk "/MemAvailable/{print int(\$2/1024)}" /proc/meminfo)"
h uptime_days "$(awk "{print int(\$1/86400)}" /proc/uptime)"
if grep -q "^md" /proc/mdstat 2>/dev/null; then h raid "$(grep -c "\[UU\]" /proc/mdstat)/$(grep -c "^md" /proc/mdstat) in sync"; fi
for d in $(lsblk -dn -o NAME | grep -E "^(nvme|sd)"); do
  # Each command that can hang gets its own limit, and says so when it hits it.
  smart=$(timeout 30 smartctl -H -A /dev/$d 2>/dev/null); src=$?
  if [ $src -eq 124 ]; then h "smart:$d" TIMEOUT; else h "smart:$d" "$(echo "$smart" | grep -q PASSED && echo PASSED || echo FAILED)"; fi
  w=$(echo "$smart" | awk -F: "/Percentage Used/{gsub(/[ %]/,\"\",\$2); print \$2}")
  [ -n "$w" ] && h "wear_pct:$d" "$w"
done
for p in $(wg show wg0 peers 2>/dev/null); do
  last=$(wg show wg0 latest-handshakes | awk -v p=$p "\$1==p{print \$2}")
  h "wg_handshake_age_s" "$(( $(date +%s) - ${last:-0} ))"
done
if systemctl is-active -q mysql 2>/dev/null; then
  rs=$(timeout 10 mysql --connect-timeout=5 -e "SHOW REPLICA STATUS\G" 2>/dev/null); mrc=$?
  # Empty is normal on the primary; only a timeout is news.
  if [ $mrc -eq 124 ]; then h replica_io timeout
  elif [ -n "$rs" ]; then
    h replica_io "$(echo "$rs" | awk "/Replica_IO_Running:/{print \$2}")"
    h replica_sql "$(echo "$rs" | awk "/Replica_SQL_Running:/{print \$2}")"
    h replica_lag_s "$(echo "$rs" | awk "/Seconds_Behind_Source:/{print \$2}")"
  fi
fi
if systemctl is-active -q redis-server 2>/dev/null; then
  r=$(REDISCLI_AUTH=$(cat /root/otc-cluster/redis.pass) timeout 5 redis-cli -h 127.0.0.1 ping 2>&1); [ $? -eq 124 ] && r="no answer in 5 s"
  h redis_ping "$r"
fi
if [ -d /etc/letsencrypt/live/off-the.cloud ]; then
  end=$(openssl x509 -enddate -noout -in /etc/letsencrypt/live/off-the.cloud/cert.pem | cut -d= -f2)
  h cert_days_left "$(( ($(date -d "$end" +%s) - $(date +%s)) / 86400 ))"
fi
since="6 hours ago"
h ssh_logins_6h "$(journalctl -u ssh --since "$since" --no-pager 2>/dev/null | grep -c "Accepted publickey")"
h ssh_login_ips_6h "$(journalctl -u ssh --since "$since" --no-pager 2>/dev/null | grep "Accepted publickey" | grep -oE "from [0-9a-f.:]+" | awk "{print \$2}" | sort -u | tr "\n" " ")"
h ssh_failed_6h "$(journalctl -u ssh --since "$since" --no-pager 2>/dev/null | grep -cE "Failed|Invalid user")"
# A process whose executable was deleted: after a package update the file
# is back under the same path (the process just needs a restart); a binary
# that deleted itself and is gone - the usual way malware hides - is not.
gone=""; stale=""
for e in /proc/[0-9]*/exe; do
  t=$(readlink "$e" 2>/dev/null) || continue
  case "$t" in *" (deleted)") f=${t% (deleted)}; if [ -e "$f" ]; then stale="$stale ${f##*/}"; else gone="$gone ${e#/proc/}:$f"; fi ;; esac
done
h deleted_exe_gone "$gone"
h needs_restart "$(echo $stale | tr " " "\n" | sort -u | tr "\n" " ")"
h modified_system_files "$(dpkg -V 2>/dev/null | grep -vE "^..5......  c " | grep -E " /(usr/)?s?bin/| /lib" | awk "{print \$NF}" | tr "\n" " ")"
# The security fingerprint: anything here changing is either something we
# did (then --accept) or someone else did.
# Loopback listeners are left out: nothing off the machine can reach them,
# and the backup agent opens and closes some (127.0.0.1:2500) as it runs.
fp listening "$(ss -lntuH | awk "{print \$1, \$5}" | sed -E "s/%[a-z0-9]+//" | grep -vE " (127\.[0-9.]+|\[::1\]|\[::ffff:127\.[0-9.]+\]):[0-9]+$" | sort -u | tr "\n" ";")"
fp uid0_users "$(awk -F: "\$3==0{print \$1}" /etc/passwd | tr "\n" " ")"
fp login_users "$(awk -F: "\$7 !~ /(nologin|false)$/{print \$1}" /etc/passwd | sort | tr "\n" " ")"
# Both names sshd reads by default, in every home (nologin users too: their keys still open tunnels).
for f in $( { awk -F: "{print \$6}" /etc/passwd; echo /root; ls -d /home/* /var/lib/* 2>/dev/null; } | sort -u | sed -e "s|/*\$|/.ssh/authorized_keys|" -e p -e "s|\$|2|" | sort -u ); do [ -f "$f" ] && fp "authorized_keys:$f" "$(sha256sum < "$f" | cut -c1-16)"; done
fp sudoers "$(cat /etc/sudoers /etc/sudoers.d/* 2>/dev/null | sha256sum | cut -c1-16)"
fp sshd_config "$(sshd -T 2>/dev/null | sort | sha256sum | cut -c1-16)"
fp crontabs "$(cat /etc/crontab /etc/cron.d/* /var/spool/cron/crontabs/* 2>/dev/null | sha256sum | cut -c1-16)"
fp systemd_units "$(ls /etc/systemd/system/*.service /etc/systemd/system/*.timer /etc/systemd/system/*.path 2>/dev/null | sort | tr "\n" " ")"
# Their content, drop-ins and enable links too, one line each so a diff names the unit (not *.mount: snapd rewrites those).
for u in $(find /etc/systemd/system /etc/systemd/user /usr/local/lib/systemd -mindepth 1 \( -type f -o -type l \) \( -name "*.service" -o -name "*.timer" -o -name "*.path" -o -name "*.socket" -o -name "*.conf" \) 2>/dev/null | sort); do if [ -L "$u" ]; then fp "unit_link:$u" "$(readlink "$u")"; else fp "unit:$u" "$(sha256sum < "$u" | cut -c1-16)"; fi; done
fp ufw_rules "$(ufw status 2>/dev/null | sort | sha256sum | cut -c1-16)"
for b in /usr/bin/otc_bridge /usr/local/sbin/* /usr/local/bin/*; do [ -f "$b" ] && fp "binary:$b" "$(sha256sum < "$b" | cut -c1-16)"; done
# Not the module list - the kernel loads modules on demand (firewall,
# networking), so it changes on its own. A kernel rootkit is a module that
# taints the kernel and is unsigned or comes from no package; the ZFS that
# Ubuntu ships also taints it (CDDL: P, O) but is signed and packaged, so
# it is not counted. Nor is a DKMS build of a packaged source (the Veeam
# backup agent: bdevfilter, veeamblksnap): unsigned by nature, but only when
# the loaded file is byte for byte what DKMS built from a source directory
# a package installed - a module swapped afterwards is still reported.
dkms_built() {
  local f=$1 d rel name rest ver
  for d in /var/lib/dkms/*/*/"$(uname -r)"/*/module/"${f##*/}"; do
    [ -f "$d" ] || continue
    rel=${d#/var/lib/dkms/}; name=${rel%%/*}; rest=${rel#*/}; ver=${rest%%/*}
    dpkg -S "/usr/src/$name-$ver" >/dev/null 2>&1 && cmp -s "$d" "$f" && return 0
  done
  return 1
}
bad_mods=""
for m in /sys/module/*; do
  t=$(cat "$m/taint" 2>/dev/null); [ -n "$t" ] || continue
  n=${m##*/}; f=$(modinfo -F filename "$n" 2>/dev/null | head -1)
  if [ -n "$f" ] && dkms_built "$f"; then continue; fi
  case "$t" in *E*) bad_mods="$bad_mods $n(unsigned)"; continue ;; esac
  if [ -z "$f" ] || ! dpkg -S "$f" >/dev/null 2>&1; then bad_mods="$bad_mods $n(no package)"; fi
done
h untrusted_modules "$bad_mods"
'

check_host() {
  local host=$1 out rc tmp pid dog timed_out=0
  say ""; say "== $host"
  # At most 240 s on the server (timeout there), and 300 s here: a command
  # stuck in I/O can keep the session open past the remote limit, and macOS
  # has no timeout(1). Otherwise one hung command stopped the check for good
  # (launchd starts no new run while one is still going).
  tmp=$(mktemp)
  ssh -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=4 "$host" \
    "sudo SERVICES='$(services_for "$host")' timeout -k 15 240 bash -s" <<< "$remote_script" > "$tmp" 2>&1 &
  pid=$!
  ( sleep 300 && kill $pid ) >/dev/null 2>&1 &
  dog=$!
  wait $pid; rc=$?
  pkill -P $dog sleep 2>/dev/null
  out=$(cat "$tmp"); rm -f "$tmp"
  # 124/137: the remote limit; 143: the watchdog here.
  case $rc in 124|137|143) timed_out=1 ;; esac
  if [ $timed_out -eq 0 ] && { [ $rc -ne 0 ] || [ -z "$out" ]; }; then bad "$host: unreachable over SSH (${out:0:120})"; return; fi

  # Health.
  while IFS=$'\t' read -r kind key val; do
    [ "$kind" = HEALTH ] || continue
    say "  $key: $val"
    case $key in
      service:*) [ "$val" = active ] || bad "$host: ${key#service:} is $val" ;;
      disk_used_pct) [ "${val:-0}" -lt 85 ] || bad "$host: disk ${val}% full" ;;
      mem_avail_mb) [ "${val:-0}" -gt 1024 ] || bad "$host: only ${val} MB memory available" ;;
      raid) [[ "$val" =~ ^([0-9]+)/([0-9]+) ]] && [ "${BASH_REMATCH[1]}" = "${BASH_REMATCH[2]}" ] || bad "$host: RAID degraded ($val)" ;;
      smart:*) [ "$val" = PASSED ] || bad "$host: SMART health $val on ${key#smart:}" ;;
      wear_pct:*) [ "${val:-0}" -lt 90 ] || bad "$host: ${key#wear_pct:} at ${val}% of its rated endurance" ;;
      wg_handshake_age_s) [ "${val:-999999}" -lt 300 ] || bad "$host: a WireGuard peer silent for ${val}s" ;;
      replica_io|replica_sql) [ "$val" = Yes ] || bad "$host: MySQL replication $key=$val" ;;
      replica_lag_s) [ "$val" != NULL ] && [ "${val:-0}" -lt 60 ] 2>/dev/null || bad "$host: replica ${val}s behind" ;;
      redis_ping) [ "$val" = PONG ] || bad "$host: Redis answers '$val'" ;;
      cert_days_left) [ "${val:-0}" -gt 20 ] || bad "$host: certificate expires in ${val} days (renewal failing?)" ;;
      ssh_failed_6h) [ "${val:-0}" -lt 500 ] || bad "$host: ${val} failed SSH attempts in 6h" ;;
      untrusted_modules) [ -z "${val// /}" ] || bad "$host: kernel modules that are unsigned or from no package: $val" ;;
      deleted_exe_gone) [ -z "${val// /}" ] || bad "$host: processes running from executables that no longer exist: $val" ;;
      modified_system_files) [ -z "${val// /}" ] || bad "$host: system files differ from their packages: $val" ;;
    esac
  done <<< "$out"

  # The fingerprint lines come last: from a cut-short run they would raise
  # a false alarm, or (--accept, a first run) save a truncated baseline.
  if [ $timed_out -eq 1 ]; then
    bad "$host: check did not finish within 240 s (a command hung on the server); security fingerprint not compared"
    return
  fi

  # Security fingerprint against the baseline.
  local cur="$DIR/baseline/$host.current" base="$DIR/baseline/$host"
  grep $'^FP\t' <<< "$out" | cut -f2- | sort > "$cur"
  if [ $ACCEPT -eq 1 ] || [ ! -f "$base" ]; then
    cp "$cur" "$base"; say "  (security baseline saved)"
  else
    local d; d=$(diff "$base" "$cur")
    if [ -n "$d" ]; then
      bad "$host: security fingerprint changed (if you made this change: servercheck.sh --accept)"
      say "$(sed 's/^/     /' <<< "$d")"
    else
      say "  security fingerprint: unchanged"
    fi
  fi
}

say "OTC bridge cluster check $(date)"
for h in "${HOSTS[@]}"; do check_host "$h"; done

# From outside: the public surface.
say ""; say "== from outside"
for u in https://off-the.cloud/ https://cala.off-the.cloud/ https://pit.off-the.cloud/; do
  code=$(curl -s -o /dev/null -m 20 -w "%{http_code}" "$u"); say "  $u: $code"
  [ "$code" = 200 ] || bad "$u answers $code"
done
for ip in 37.187.141.41 149.202.83.7; do
  code=$(curl -s -o /dev/null -m 20 -w "%{http_code}" --resolve off-the.cloud:443:$ip https://off-the.cloud/); say "  node $ip: $code"
  if [ "$code" != 200 ]; then bad "node $ip answers $code"; continue; fi
  # The certificate the node serves, not redis's certbot copy: a failed push
  # by the deploy hook, or a pair certReloader couldn't load, shows only here.
  # Only after a 200, so s_client (no timeout of its own) has a live node.
  pem=$(openssl s_client -connect "$ip:443" -servername off-the.cloud </dev/null 2>/dev/null | openssl x509 2>/dev/null)
  if [ -z "$pem" ]; then bad "node $ip: could not read its certificate"; continue; fi
  say "  node $ip certificate until: $(openssl x509 -noout -enddate <<< "$pem" | cut -d= -f2)"
  openssl x509 -noout -checkend $((20*86400)) <<< "$pem" >/dev/null || bad "node $ip serves a certificate that expires within 20 days (deploy hook push failed, or certReloader could not load the new pair?)"
done
dns=$(dig +short off-the.cloud @dns10.ovh.net | sort | tr "\n" " ")
say "  DNS off-the.cloud: $dns"
[ "$dns" = "149.202.83.7 37.187.141.41 " ] || bad "DNS for off-the.cloud is '$dns'"
for hp in 37.187.141.41:3306 149.202.83.7:3306 37.187.141.41:8444 149.202.83.7:8444 51.83.103.72:6379 51.83.103.72:3306; do
  if nc -z -G 5 "${hp%:*}" "${hp#*:}" 2>/dev/null; then bad "$hp is reachable from the internet"; else say "  $hp closed"; fi
done

say ""
if [ ${#PROBLEMS[@]} -eq 0 ]; then
  say "RESULT: all good"
else
  say "RESULT: ${#PROBLEMS[@]} problem(s)"
  printf '  - %s\n' "${PROBLEMS[@]}" >> "$REPORT"
  msg=$(printf '%s; ' "${PROBLEMS[@]}" | cut -c1-220)
  # As arguments, not in the AppleScript source: a quote or backslash in a
  # problem (read from the servers) made it a syntax error, and no alert.
  osascript -e 'on run argv' -e 'display notification (item 1 of argv) with title (item 2 of argv) subtitle (item 3 of argv) sound name "Basso"' -e 'end run' -- "$msg" "OTC servers: ${#PROBLEMS[@]} problem(s)" "Report: $STAMP" 2>/dev/null
fi
cp "$REPORT" "$LOGDIR/latest.txt"

send_mail() {
  local pass subject netrc msg
  # Google shows app passwords in groups of four; the spaces aren't part
  # of it (and would end it in the netrc file).
  pass=$(security find-generic-password -s otc-servercheck-smtp -a "$MAILTO" -w 2>/dev/null | tr -d '[:space:]') || return 0
  [ -n "$pass" ] || return 0
  if [ ${#PROBLEMS[@]} -eq 0 ]; then subject="OTC servers: all good"; else subject="OTC servers: ${#PROBLEMS[@]} problem(s)"; fi
  netrc=$(mktemp); msg=$(mktemp); chmod 600 "$netrc" "$msg"
  # The password goes to curl in a file, never on its command line.
  printf 'machine smtp.gmail.com login %s password %s\n' "$MAILTO" "$pass" > "$netrc"
  {
    printf 'From: OTC servercheck <%s>\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n' "$MAILTO" "$MAILTO" "$subject" "$(LC_ALL=C date -R)"
    printf 'MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n'
    sed 's/$/\r/' "$REPORT"
  } > "$msg"
  if curl -s --ssl-reqd --url smtps://smtp.gmail.com:465 --netrc-file "$netrc" \
       --mail-from "$MAILTO" --mail-rcpt "$MAILTO" --upload-file "$msg" -m 60; then
    echo "  (emailed to $MAILTO)" >> "$LOGDIR/latest.txt"
  else
    osascript -e 'display notification "Could not email the report - see the log" with title "OTC servercheck"' 2>/dev/null
  fi
  rm -f "$netrc" "$msg"
}
[ $MAIL -eq 1 ] && [ $ACCEPT -eq 0 ] && send_mail
# Keep a month of reports.
find "$LOGDIR" -name '2*.txt' -mtime +31 -delete 2>/dev/null
[ ${#PROBLEMS[@]} -eq 0 ]
