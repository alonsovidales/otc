#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 163.
#
# Collection Rating (owner, 2026-11-02): a collection's photos rated from 1
# to 5 stars with a comment each, by the owner and by anyone with the
# collection's shared link when it was made with "Let people rate the
# photos" - for a photographer sharing pictures with a customer.
#
# image_groups.rating turns it on for a collection (off keeps the ratings,
# hidden); photo_ratings holds one row per photo and collection, the stars
# readable (the collection is filtered and sorted by them) and the comment
# sealed to the user's public key, kept in the vault with its private key
# sealed with the user's own key (rating_public, rating_private: made at
# the next sign-in). image_groups.rating_key is what a rated link's
# visitors read the comments with (photo_ratings.shared_comment), and
# shared_links records a gallery's collection, whether it takes ratings,
# and its last alert - see db.sql and files_manager/rating.go.
#
# Idempotent: ADD COLUMN / CREATE TABLE IF NOT EXISTS.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE image_groups ADD COLUMN IF NOT EXISTS rating TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE image_groups ADD COLUMN IF NOT EXISTS rating_key BLOB NULL;
CREATE TABLE IF NOT EXISTS photo_ratings (`group_id` VARCHAR(36) NOT NULL, `hash` VARCHAR(64) NOT NULL, `stars` TINYINT NOT NULL DEFAULT 0, `comment` BLOB NULL, `shared_comment` BLOB NULL, `link` VARCHAR(64) NULL, `updated` DATETIME NOT NULL, PRIMARY KEY (`group_id`, `hash`), KEY (`hash`)) ENGINE=InnoDB;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS group_id VARCHAR(36) NULL;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS rating TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS rating_alerted DATETIME NULL;
ALTER TABLE vault ADD COLUMN IF NOT EXISTS rating_public VARBINARY(32) NULL;
ALTER TABLE vault ADD COLUMN IF NOT EXISTS rating_private BLOB NULL;
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

echo "release 163 applied: image_groups.rating, image_groups.rating_key, photo_ratings, shared_links.group_id, shared_links.rating, shared_links.rating_alerted, vault.rating_public, vault.rating_private"
