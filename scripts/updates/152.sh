#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 152.
#
# Smart search on by default (settings.smart_search_enabled defaults to 1,
# see db.sql): every library is turned on once, the ones release 151 left
# off included, and the column then defaults to on, so a library created
# later starts with it on too. Its model isn't fetched here: the device
# downloads it itself, in the background at a capped speed, once Smart
# search is on and the model isn't there (files_manager/smart_model.go), and
# describes the photos after the next sign-in.
#
# Idempotent: the column's default is the mark - a database whose column
# already defaults to on was turned on before, and a library its user turned
# off since is left alone. A database without the column (151's script not
# applied) gets it, on.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

smart_search_on() {
    local db="$1" default
    mysql "$db" -e 'ALTER TABLE settings ADD COLUMN IF NOT EXISTS smart_search_enabled TINYINT(1) NOT NULL DEFAULT 1'
    default="$(mysql "$db" -N -B -e "SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'smart_search_enabled'")"
    if [ "$default" = 0 ]; then
        # On first, then the default: a run cut between the two does the
        # first again.
        mysql "$db" <<'SQL'
UPDATE settings SET smart_search_enabled = 1;
ALTER TABLE settings ALTER COLUMN smart_search_enabled SET DEFAULT 1;
SQL
        echo "${db}: Smart search turned on"
    else
        echo "${db}: Smart search already on by default"
    fi
}

smart_search_on otc

for db in $(mysql -N -B -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    # The otc service can write this table: only a name the device itself
    # generates (dao checkGenerated, install.sh) reaches root's mysql
    # command line, where anything starting with - would be an option.
    if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
        echo "skipping a users row whose db_name this device did not generate"
        continue
    fi
    if ! mysql -N -B -e "SHOW DATABASES LIKE '$db'" | grep -qxF -- "$db"; then
        echo "skipping $db: no such database"
        continue
    fi
    smart_search_on "$db"
done

echo "release 152 applied: Smart search on by default"
