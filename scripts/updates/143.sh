#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 143. Run as root by scripts/update.sh, before the new binary is
# built, while the otc service keeps running (update.sh restarts it only at
# the very end). Every part below is self-contained and idempotent.
set -euo pipefail

# ---------------------------------------------------------------------------
# Part: device reliability - the files table's unused indexes, and MariaDB's
# buffer pool sized by the device's memory.
#
# 1. `files` loses its indexes on `modified` and `size`. No query filters or
#    sorts the files by either - the size backfill and the overrides find
#    their rows by hash or path, and the users' storage total sums the whole
#    table either way - while every row a sync inserts updated both. db.sql
#    no longer has them. Dropping a secondary index is done in place (no
#    copy of the table); a query holding the table delays it at most
#    lock_wait_timeout, and then this script fails and the next update runs
#    it again. On the primary database and on every per-user one (issue
#    #82), a name reaching root's mysql command line only in the shape the
#    device generates (^otc_[0-9a-f]{32}$). DROP INDEX IF EXISTS: a database
#    without them is left as it is.
#    No (created, path) index is added: `path` is a VARCHAR(768) in utf8mb4,
#    3,072 bytes, which is InnoDB's whole key limit, so MariaDB refuses any
#    index adding a column to it ("Specified key was too long", ERROR 1071 -
#    checked on 11.8.6).
# 2. InnoDB's buffer pool - MariaDB's default is 128 MB - is sized by the
#    device's memory in a conf.d drop-in: 256 MB with 4 GB, 768 MB with 8 GB,
#    1 GB from 12 GB; under 3 GB the default stays. install.sh writes the
#    same file on new devices and the container image sets it at start. It
#    takes effect when MariaDB next starts, at the device's next restart:
#    not here, because the otc service and its users' instances keep
#    running through the migrations, and a MariaDB restart now would cut
#    their queries in the middle of uploads and syncs; nor live, because
#    MariaDB 11.8 (Raspberry Pi OS Trixie) can't grow the pool past the size
#    it started with (innodb_buffer_pool_size_max). The file is rewritten
#    only when what it should say changes.
# ---------------------------------------------------------------------------

rel143_drop_indexes() {
    local db="$1"
    echo "release 143: dropping the unused files indexes in ${db}"
    mysql "$db" <<'SQL'
SET SESSION lock_wait_timeout = 300;
ALTER TABLE files DROP INDEX IF EXISTS `modified`, DROP INDEX IF EXISTS `size`;
SQL
}

rel143_drop_indexes otc

for db in $(mysql -N -B -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    # The otc service can write this table: only a name the device itself
    # generates (dao checkGenerated, install.sh) reaches root's mysql
    # command line, where anything starting with - would be an option.
    if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
        echo "release 143: skipping a users row whose db_name this device did not generate"
        continue
    fi
    if ! mysql -N -B -e "SHOW DATABASES LIKE '$db'" | grep -qxF -- "$db"; then
        echo "release 143: skipping $db: no such database"
        continue
    fi
    rel143_drop_indexes "$db"
done

# The buffer pool for this much memory, in MB ("" keeps MariaDB's default).
rel143_pool_mb() {
    local mem_kb mem_mb
    mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || true)"
    case "$mem_kb" in
        ''|*[!0-9]*) return 0 ;;
    esac
    mem_mb=$((mem_kb / 1024))
    if [ "$mem_mb" -lt 3000 ]; then
        return 0
    elif [ "$mem_mb" -lt 6144 ]; then
        echo 256
    elif [ "$mem_mb" -lt 12288 ]; then
        echo 768
    else
        echo 1024
    fi
}

rel143_conf_dir=/etc/mysql/mariadb.conf.d
rel143_conf="$rel143_conf_dir/70-otc-memory.cnf"
rel143_pool="$(rel143_pool_mb)"
if [ -z "$rel143_pool" ]; then
    echo "release 143: under 3 GB of memory (or unknown): MariaDB keeps its own buffer pool"
elif [ ! -d "$rel143_conf_dir" ]; then
    echo "release 143: no $rel143_conf_dir (not Debian's MariaDB): buffer pool left as it is"
else
    rel143_want="$(printf '%s\n' \
        "# Off The Cloud (release 143): InnoDB's buffer pool by the device's memory -" \
        "# 256 MB with 4 GB, 768 MB with 8 GB, 1 GB from 12 GB. Written by the" \
        "# updates and install.sh; applies from MariaDB's next start." \
        "[mysqld]" \
        "innodb_buffer_pool_size = ${rel143_pool}M")"
    if [ "$(cat "$rel143_conf" 2>/dev/null || true)" != "$rel143_want" ]; then
        rel143_tmp="$(mktemp "$rel143_conf_dir/.70-otc-memory.XXXXXX")"
        printf '%s\n' "$rel143_want" > "$rel143_tmp"
        chmod 0644 "$rel143_tmp"
        mv -f "$rel143_tmp" "$rel143_conf"
        sync
        echo "release 143: MariaDB's buffer pool will be ${rel143_pool} MB from its next start ($rel143_conf)"
    else
        echo "release 143: MariaDB's buffer pool already set to ${rel143_pool} MB"
    fi
fi

echo "release 143 applied (device reliability part): unused files indexes dropped"

# ---------------------------------------------------------------------------
# Part: root scripts and the install path - three sections, each
# idempotent: run twice, it changes nothing the second time. install.sh
# does the same on new devices.
#   1. The image's network script on devices imaged before 2026-09-29.
#   2. The storage: a disk missing at boot no longer stops the boot, and
#      the service and MariaDB wait for it.
#   3. The device's database account: only what it uses.
# ---------------------------------------------------------------------------

src="${OTC_VERIFIED_SRC:-}"

# ---------------------------------------------------------------------------
# 1. network_setup.py on devices imaged before 2026-09-29
# ---------------------------------------------------------------------------
# Their copy of the image's root network daemon raises the open "Off The
# Cloud" setup hotspot whenever the device loses its network after setup
# (and reads a join request in the otc user's directory following links).
# No release replaced it: update.sh never reinstalled it before this one.
# The current script never raises the hotspot (only with
# OTC_ALLOW_HOTSPOT=1, which nothing sets) and takes one left up down. Only
# on a device made from the image, which has the script, and set up. The
# hotspot's saved NetworkManager profile goes too ("OTC-Setup", an access
# point with the SSID "Off The Cloud"); the owner's own networks are never
# touched - a profile is removed only if it is an access point.
if [ -f /etc/otc/.install-complete ] && [ -f /usr/local/bin/network_setup.py ]; then
    if [ -z "$src" ] || [ ! -f "$src/scripts/network_setup.py" ]; then
        echo "release 143: no verified source with network_setup.py (OTC_VERIFIED_SRC=${src:-unset})" >&2
        exit 1
    fi
    new=/usr/local/bin/network_setup.py.new.$$
    install -m 0755 "$src/scripts/network_setup.py" "$new"
    sync "$new" 2>/dev/null || sync
    mv -Tf "$new" /usr/local/bin/network_setup.py
    # NetworkManager not running (nmcli fails) leaves no profile to remove.
    profiles="$(nmcli -g UUID,TYPE connection show 2>/dev/null || true)"
    while IFS=: read -r uuid type; do
        [ "$type" = 802-11-wireless ] && [[ "$uuid" =~ ^[0-9a-fA-F-]{36}$ ]] || continue
        mode="$(nmcli -g 802-11-wireless.mode connection show uuid "$uuid" 2>/dev/null || true)"
        [ "$mode" = ap ] || continue
        name="$(nmcli -g connection.id connection show uuid "$uuid" 2>/dev/null || true)"
        ssid="$(nmcli -g 802-11-wireless.ssid connection show uuid "$uuid" 2>/dev/null || true)"
        if [ "$name" = OTC-Setup ] || [ "$ssid" = "Off The Cloud" ]; then
            if nmcli connection delete uuid "$uuid" >/dev/null 2>&1; then
                echo "release 143: removed the setup hotspot's profile ($uuid)"
            else
                echo "release 143: WARNING: could not remove the setup hotspot's profile ($uuid)"
            fi
        fi
    done <<< "$profiles"
    systemctl try-restart network-setup.service >/dev/null 2>&1 || true
    echo "release 143: network_setup.py is the current one"
fi

# ---------------------------------------------------------------------------
# 2. The storage at boot: nofail, and nothing starts without it
# ---------------------------------------------------------------------------
# /dev/md0's fstab line had plain "defaults": with a disk missing or slow
# at boot the device stopped in emergency mode - no network, no SSH - and
# only a monitor and keyboard could say why. With nofail (and up to 3
# minutes for the mirror to appear) it boots without its storage; the
# service and MariaDB (whose datadir is on the mirror) then don't start
# until it is mounted - nofail no longer orders the mount before
# local-fs.target, so without these drop-ins they could start on the empty
# directory under the mount point. The drop-ins go first, then fstab.
# Where nothing is mounted there (a single-disk device) the drop-ins ask
# for nothing but the root filesystem. Only the md0 line is changed, and
# only its options: any it already has stay.
if [ -f /etc/otc/.install-complete ]; then
    for unit in otc mariadb; do
        dir="/etc/systemd/system/$unit.service.d"
        mkdir -p "$dir"
        printf '# Off The Cloud: start only with the storage mounted (fstab has nofail).\n[Unit]\nRequiresMountsFor=/mnt/storage\n' \
            > "$dir/otc-storage.conf.tmp.$$"
        mv -Tf "$dir/otc-storage.conf.tmp.$$" "$dir/otc-storage.conf"
    done
    systemctl daemon-reload

    fstab_new="$(mktemp /etc/fstab.otc.XXXXXX)"
    awk '
        /^[[:space:]]*#/ || NF < 4 { print; next }
        $1 == "/dev/md0" && $2 == "/mnt/storage" {
            o = $4
            if (o !~ /(^|,)nofail(,|$)/) o = o ",nofail"
            if (o !~ /(^|,)x-systemd\.device-timeout=/) o = o ",x-systemd.device-timeout=3min"
            if (o != $4) { $4 = o }
        }
        { print }' /etc/fstab > "$fstab_new"
    if cmp -s "$fstab_new" /etc/fstab; then
        rm -f "$fstab_new"
    else
        chmod 644 "$fstab_new"
        sync "$fstab_new" 2>/dev/null || sync
        mv -Tf "$fstab_new" /etc/fstab
        systemctl daemon-reload
        echo "release 143: /mnt/storage's fstab line has nofail"
    fi
fi

# ---------------------------------------------------------------------------
# 3. The device's database account: only what it uses
# ---------------------------------------------------------------------------
# otc@localhost held ALL PRIVILEGES ON *.* WITH GRANT OPTION (FILE, SUPER,
# SHUTDOWN, every database on the server). What it uses (dao/
# provisioning.go): its own database; everything, with GRANT OPTION, on
# the per-user databases it creates and drops (named otc_<32 hex>; the
# pattern is otc_%, as in a GRANT the _ of otc_<uuid> is itself a wildcard,
# which only a pattern with that wildcard covers - the escaped otc\_% was
# refused); CREATE USER for their accounts and RELOAD for FLUSH PRIVILEGES. The narrow grants come first, so the
# running service never goes without what it uses; then the global ones
# go. Quoted heredoc: the backticks are SQL. A device without the account
# (none was ever made without one) is left alone.
# MariaDB not answering fails the release (it is tried again), as every
# release with SQL does.
accounts="$(mysql -N -B -e "SELECT COUNT(*) FROM mysql.user WHERE User = 'otc' AND Host = 'localhost'")"
if [ "$accounts" = 1 ]; then
    mysql <<'SQL'
GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';
GRANT ALL PRIVILEGES ON `otc_%`.* TO 'otc'@'localhost' WITH GRANT OPTION;
REVOKE ALL PRIVILEGES ON *.* FROM 'otc'@'localhost';
REVOKE GRANT OPTION ON *.* FROM 'otc'@'localhost';
GRANT CREATE USER, RELOAD ON *.* TO 'otc'@'localhost';
FLUSH PRIVILEGES;
SQL
    echo "release 143: the device's database account holds only what it uses"
fi

echo "release 143 applied (root scripts part): network script, storage at boot, database account"
