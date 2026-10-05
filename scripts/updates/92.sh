#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 92: one like per domain on a post or a comment. A friend's unlike
# was stored as one more like (and a double tap could count twice), so the
# same domain could hold several like rows and the counters drifted. On the
# primary and every per-user database: a friend's rows (only ever written
# by the friend sync, one per like or unlike, in turn) that are even in
# number end in an unlike, so they all go; of the duplicates left the
# earliest is kept; a unique key stops new ones; and the like counters are
# recounted from the rows. Idempotent: under the unique key every domain
# has one row.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
DELETE l FROM social_publication_likes l JOIN (
  SELECT pub_uuid, friend_domain FROM social_publication_likes
  WHERE friend_domain IN (SELECT domain FROM social_friendship)
  GROUP BY pub_uuid, friend_domain HAVING COUNT(*) % 2 = 0
) d USING (pub_uuid, friend_domain);
DELETE l1 FROM social_publication_likes l1 JOIN social_publication_likes l2
  ON l1.pub_uuid = l2.pub_uuid AND l1.friend_domain = l2.friend_domain
  AND (l1.dt > l2.dt OR (l1.dt = l2.dt AND l1.uuid > l2.uuid));
ALTER TABLE social_publication_likes ADD UNIQUE INDEX IF NOT EXISTS like_once (pub_uuid, friend_domain);
UPDATE social_publications p SET likes = (SELECT COUNT(*) FROM social_publication_likes l WHERE l.pub_uuid = p.uuid);
DELETE l FROM social_publication_comment_likes l JOIN (
  SELECT comment_uuid, friend_domain FROM social_publication_comment_likes
  WHERE friend_domain IN (SELECT domain FROM social_friendship)
  GROUP BY comment_uuid, friend_domain HAVING COUNT(*) % 2 = 0
) d USING (comment_uuid, friend_domain);
DELETE l1 FROM social_publication_comment_likes l1 JOIN social_publication_comment_likes l2
  ON l1.comment_uuid = l2.comment_uuid AND l1.friend_domain = l2.friend_domain
  AND (l1.dt > l2.dt OR (l1.dt = l2.dt AND l1.uuid > l2.uuid));
ALTER TABLE social_publication_comment_likes ADD UNIQUE INDEX IF NOT EXISTS comment_like_once (comment_uuid, friend_domain);
UPDATE social_publications_comments c SET likes = (SELECT COUNT(*) FROM social_publication_comment_likes l WHERE l.comment_uuid = c.uuid);
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

echo "release 92 applied: one like per domain"
