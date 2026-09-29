#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 27: Android push notifications (issue #125).
#
# The table the device keeps the Android app's Firebase Cloud Messaging
# tokens in, on the primary database and every per-user database - the
# same shape as apns_tokens. Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS fcm_tokens (
  `token` varchar(512) NOT NULL,
  `created` datetime NOT NULL,
  PRIMARY KEY (`token`)
) ENGINE=InnoDB;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 27 applied: fcm_tokens"
