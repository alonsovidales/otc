#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 151: Smart search (beta) - photos found by describing them.
#
# A new table and a settings column - see db.sql's photo_embeddings and
# settings.smart_search_enabled for the shape. Smart search is off by
# default (smart_search_enabled 0): nothing is described until the user
# turns it on in Settings, and turning it off deletes every description.
#
# Also the config's [embedder] section, pointing at SigLIP 2's files in
# /usr/local/models/siglip2-base/ (install.sh's EMBED_DIR). update.sh
# fetches them itself, right after the release scripts, at every update
# while any is missing: Smart search is optional - without them it can't be
# turned on - so a download that fails only waits for the next update.
#
# Idempotent: IF NOT EXISTS on the table and the column, the section added
# only when absent.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS photo_embeddings (
  `hash` VARCHAR(64) NOT NULL,
  `model` VARCHAR(64) NOT NULL,
  `vec` VARBINARY(2048) NOT NULL,
  `made` DATETIME NOT NULL,
  PRIMARY KEY (`hash`)
) ENGINE=InnoDB;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS smart_search_enabled TINYINT(1) NOT NULL DEFAULT 0;
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

# The config's [embedder] section, once. The device reads files that aren't
# there (yet) as no model: Smart search can't be turned on until they are.
EMBED_DIR=/usr/local/models/siglip2-base
added=0
for ini in /etc/otc_*.ini; do
    [ -f "$ini" ] && [ ! -L "$ini" ] || continue
    if grep -q '^\[embedder\]' "$ini"; then
        continue
    fi
    tmp="$(mktemp "$ini.XXXXXX")"
    cat -- "$ini" > "$tmp"
    printf '\n# Smart search (beta): SigLIP 2, the photos'"'"' and the searches'"'"' model.\n# Optional - without its files Smart search can'"'"'t be turned on.\n[embedder]\nimage-model-path=%s/image.onnx\ntext-model-path=%s/text.onnx\ntokenizer-path=%s/tokenizer.model\n' "$EMBED_DIR" "$EMBED_DIR" "$EMBED_DIR" >> "$tmp"
    chown --reference="$ini" "$tmp"
    chmod --reference="$ini" "$tmp"
    sync "$tmp" 2>/dev/null || sync
    mv -Tf "$tmp" "$ini"
    added=$((added + 1))
done

echo "release 151 applied: photo_embeddings, settings.smart_search_enabled; [embedder] added to $added config file(s)"
