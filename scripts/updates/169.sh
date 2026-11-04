#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 169.
#
# Each change below is a section of its own, headed "# --- <area>: <what>
# ---", and each one is idempotent and safe on every older release (this
# script runs on a device after every release before it, in order). set -e
# is off on purpose: each section says what its failure means - exit 1
# stops the update here, to be run again by the next one; a warning lets
# the rest go on.
#
# Run as root by scripts/update.sh.
set -uo pipefail

# --- dev-release: TCP keeps its window on idle connections ----------------
#
# A TCP connection that sat idle for a moment starts again from slow start
# on Linux, as if it were new. The bridge connections the device keeps
# open for the apps do sit idle between two requests, and each 4 MiB piece
# of a video sent over one then spent several round trips reaching full
# speed. The window TCP learnt on the connection is now kept
# (net.ipv4.tcp_slow_start_after_idle=0, /etc/sysctl.d/80-otc-network.conf),
# from now and at every boot. The same function as scripts/install.sh's
# (updater/release169_test.go). Not in a container, whose network is its
# host's. A failure only warns: transfers work, only slower after a pause.

otc_network_tuning() {
    local conf=/etc/sysctl.d/80-otc-network.conf want
    want='# Off The Cloud (scripts/install.sh, updates/169.sh): a connection that sat
# idle - the bridge connections the device keeps open for the apps - keeps
# the window TCP learnt on it instead of starting again from slow start.
net.ipv4.tcp_slow_start_after_idle = 0'
    if [ "$(cat "$conf" 2>/dev/null)" != "$want" ]; then
        { mkdir -p /etc/sysctl.d && printf '%s\n' "$want" > "$conf.tmp.$$" && chmod 0644 "$conf.tmp.$$" \
            && mv -f "$conf.tmp.$$" "$conf"; } || { rm -f "$conf.tmp.$$"; return 1; }
    fi
    sysctl -q -w net.ipv4.tcp_slow_start_after_idle=0
}

if [ -f /.dockerenv ] || [ "${OTC_CONTAINER:-}" = "1" ]; then
    echo "release 169: in a container, the host's network settings are left alone"
elif otc_network_tuning; then
    echo "release 169: TCP keeps its window on connections that sat idle"
else
    echo "WARNING: release 169 could not set net.ipv4.tcp_slow_start_after_idle - transfers through the bridge start slower after a pause"
fi

# --- dev-release: release 168's month counts, where they are missing ------
#
# Release 168 added Images' month counts (photo_months, photo_month_hashes,
# settings.photo_months_for, settings.photo_months_run) to every database,
# and a database where that failed was only reported: the device counted
# its months the slow way there for good. The same statements again,
# idempotent; as in 168, a database that can't take them is reported and
# left (a binary newer than its schema carries on without them). The list
# of users must be read, though: a query that fails is the database
# failing, not "no other users" (it used to read as that), and stops the
# update, which the next one runs again. Run on the primary database and on
# every per-user database (issue #82), each of which carries its own schema.

dev_release_months_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS photo_months (`month` INT NOT NULL, `images` INT NOT NULL DEFAULT 0, `media` INT NOT NULL DEFAULT 0, PRIMARY KEY (`month`)) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS photo_month_hashes (`hash` VARCHAR(64) NOT NULL, `images` INT NULL, `media` INT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS photo_months_for MEDIUMTEXT NULL;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS photo_months_run VARCHAR(32) NULL;
SQL
    then
        echo "WARNING: release 169 could not add the month counts to ${db}; it counts them the old way"
    fi
}

dev_release_months() {
    local tables dbs db found
    dev_release_months_to otc
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 169: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 169: could not read this device's users" >&2
        return 1
    fi
    for db in $dbs; do
        # The otc service can write this table: only a name the device
        # itself generates (dao checkGenerated, install.sh) reaches root's
        # mysql command line, where anything starting with - would be an
        # option.
        if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
            echo "skipping a users row whose db_name this device did not generate"
            continue
        fi
        if ! found="$(mysql -N -B -e "SHOW DATABASES LIKE '$db'")"; then
            echo "release 169: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_release_months_to "$db"
    done
}

dev_release_months || exit 1
echo "release 169: the month counts are in every database that can take them"
