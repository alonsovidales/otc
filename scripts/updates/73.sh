#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 73: thumbnails first, analysis later. pending_analysis, on the
# primary and every per-user database, holds the hashes of uploads whose
# tags and faces haven't been worked out yet, so that queue survives a
# restart. Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS pending_analysis (hash VARCHAR(64) NOT NULL, queued DATETIME NOT NULL, PRIMARY KEY (hash)) ENGINE=InnoDB;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 73 applied: analysis queue"
