#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 51 (issue #174): unfriending can delete everything synced from
# that friend, and ask the friend's device to delete what this one shared.
# Adds, on the primary and every per-user database:
#   social_friendship.forget_requested - "leaving", waiting for the friend
#   social_publications_comments.author_domain - whose comment it is; the
#     device owner's own comments are filled in from the settings
#   events.target - an event for one friend only
# Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE social_friendship ADD COLUMN IF NOT EXISTS forget_requested DATETIME NULL;
ALTER TABLE social_publications_comments ADD COLUMN IF NOT EXISTS author_domain VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN IF NOT EXISTS target VARCHAR(128) NULL;
UPDATE social_publications_comments SET author_domain = (SELECT subdomain FROM settings LIMIT 1)
  WHERE own_comment = 1 AND author_domain = '';
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 51 applied: unfriending can delete shared data"
