#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 157.
#
# A person's face chosen by the owner (owner, 2026-10-31): a face from one
# of their photos, or a picture of the owner's own, cropped in the app.
# people.cover_chosen keeps the choice from being replaced by the medoid as
# more photos of them come in, and people.cover_image holds the picture,
# encrypted at rest like the faces' thumbnails - see db.sql.
#
# Idempotent: ADD COLUMN IF NOT EXISTS.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE people ADD COLUMN IF NOT EXISTS cover_chosen TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE people ADD COLUMN IF NOT EXISTS cover_image MEDIUMBLOB NULL;
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

echo "release 157 applied: people.cover_chosen, people.cover_image"
