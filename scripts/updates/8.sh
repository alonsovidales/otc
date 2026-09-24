#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 8 (issue #132): upload-only folders and file versions.
#
# Two new tables, nothing altered - see db.sql's upload_only_folders and
# file_versions comments for the shape.
#
# Idempotent: both are IF NOT EXISTS, so a device that already has them
# (a fresh install, where db.sql created them) does nothing here.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying schema to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS upload_only_folders (
  path VARCHAR(768) NOT NULL,
  PRIMARY KEY (path)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS file_versions (
  path VARCHAR(768) NOT NULL,
  hash VARCHAR(64) NOT NULL,
  mime VARCHAR(150) NOT NULL,
  size INT NOT NULL,
  created DATETIME NOT NULL,
  modified DATETIME NOT NULL,
  replaced DATETIME NOT NULL,
  KEY (path),
  KEY (hash)
) ENGINE=InnoDB;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 8 applied: upload_only_folders, file_versions"
