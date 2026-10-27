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
