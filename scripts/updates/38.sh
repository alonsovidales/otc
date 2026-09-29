#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 38: when each friend's device was last heard from. A device name
# that changes hands used to inherit the previous device's friendships; a
# re-link after a week without contact now goes through the normal friend
# request. Existing friendships start as "seen now", so friends that are in
# touch keep working; the clock starts from this update. Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS last_seen DATETIME NULL;
UPDATE social_friendship SET last_seen = NOW() WHERE last_seen IS NULL AND status = 'accepted';
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 38 applied: social_friendship.last_seen"
