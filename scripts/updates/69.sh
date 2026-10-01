#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 69 (issue #180): shared galleries, and a list of share links in
# Settings. shared_links gains, on the primary and every per-user
# database: kind (archive/gallery), description (encrypted with the
# owner's key), files, opens, last_opened, expires; size becomes a BIGINT.
# The link itself is still never stored. Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
-- Issue #180: share links list (kind, description, opens) and galleries.
ALTER TABLE shared_links MODIFY size BIGINT NOT NULL;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS kind VARCHAR(16) NOT NULL DEFAULT 'archive';
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS description BLOB NULL;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS files INT NOT NULL DEFAULT 0;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS opens INT NOT NULL DEFAULT 0;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS last_opened DATETIME NULL;
ALTER TABLE shared_links ADD COLUMN IF NOT EXISTS expires DATETIME NULL;
CREATE INDEX IF NOT EXISTS shared_links_uuid ON shared_links (uuid);
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 69 applied: shared galleries"
