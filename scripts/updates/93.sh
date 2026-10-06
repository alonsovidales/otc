#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 93 (issue #187): files of 2 GiB or more showed a wrong size,
# because the size columns were an int and the size wrapped. On the
# primary and every per-user database the size columns of files,
# file_versions and social_publications_files become BIGINT (MODIFY keeps
# the index on files.size), and settings gains sizes_backfilled: the new
# binary corrects the sizes stored wrapped once, from the blobs, and sets
# it. Idempotent: MODIFY changes nothing on a column already BIGINT.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE files MODIFY `size` BIGINT NOT NULL;
ALTER TABLE file_versions MODIFY `size` BIGINT NOT NULL;
ALTER TABLE social_publications_files MODIFY `size` BIGINT NOT NULL;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sizes_backfilled TINYINT(1) NOT NULL DEFAULT 0;
SQL
}

apply_to otc

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
    apply_to "$db"
done

echo "release 93 applied: file sizes are BIGINT"
