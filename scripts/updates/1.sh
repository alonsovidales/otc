#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 1 (issue #94): schema catch-up.
#
# Every device installed before in-place updates existed has no
# /etc/otc/version, so it starts here and runs this first. Its job is to
# bring such a device's schema level with what install.sh would create
# today - the same statements install.sh's own apply_schema_migrations
# runs, which until now only reached a device by re-running the installer.
#
# Idempotent by construction: every statement is IF NOT EXISTS, so this is
# a no-op on a device that is already current (including a fresh install,
# where db.sql just created all of it).
#
# Run as root by scripts/update.sh. Applied to the primary database and to
# every per-user database (issue #82), since each of those carries its own
# copy of the same schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying schema to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS face_recognition_enabled TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS notifications_started TINYINT(1) NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS people (
  id VARCHAR(36) NOT NULL,
  name VARCHAR(150) NOT NULL DEFAULT '',
  created DATETIME NOT NULL,
  cover_face_id VARCHAR(36) DEFAULT NULL,
  cohesion FLOAT DEFAULT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB;
ALTER TABLE people ADD COLUMN IF NOT EXISTS cover_face_id VARCHAR(36) DEFAULT NULL;
ALTER TABLE people ADD COLUMN IF NOT EXISTS cohesion FLOAT DEFAULT NULL;
SQL
}

apply_to otc

# Issue #82: the per-user databases. Absent on a single-user device, in
# which case there is simply nothing to loop over.
for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 1 applied"
