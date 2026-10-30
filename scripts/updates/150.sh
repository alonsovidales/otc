#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 150: On this day (Memories).
#
# Two new tables and a settings column - see db.sql's photo_quality,
# memories_hidden and settings.memories_enabled for the shape. On this day
# is on by default (memories_enabled 1): the device measures how the photos
# it holds look, in the background, after the next sign-in. Until they
# exist the device reads it as off.
#
# Also the config's [quality] section, pointing at the photo quality model
# (install.sh's QUALITY_ONNX). update.sh fetches the model itself, right
# after the release scripts, at every update while it is missing: it is
# optional - without it photos are measured by sharpness and exposure alone
# - so a download that fails only waits for the next update.
#
# Idempotent: IF NOT EXISTS on each table and column, the section added only
# when absent.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS photo_quality (
  `hash` VARCHAR(64) NOT NULL,
  `aesthetic` FLOAT NULL,
  `sharpness` FLOAT NOT NULL,
  `dark` FLOAT NOT NULL,
  `bright` FLOAT NOT NULL,
  `brightness` FLOAT NOT NULL,
  `width` INT NOT NULL,
  `height` INT NOT NULL,
  `camera` TINYINT(1) NULL,
  `scored` DATETIME NOT NULL,
  PRIMARY KEY (`hash`)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS memories_hidden (
  `kind` VARCHAR(16) NOT NULL,
  `value` VARCHAR(64) NOT NULL,
  `hidden` DATETIME NOT NULL,
  PRIMARY KEY (`kind`, `value`)
) ENGINE=InnoDB;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS memories_enabled TINYINT(1) NOT NULL DEFAULT 1;
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

# The config's [quality] section, once. The device reads a model file that
# isn't there (yet) as no model.
QUALITY_ONNX=/usr/local/models/nima_mobilenet_aesthetic.onnx
added=0
for ini in /etc/otc_*.ini; do
    [ -f "$ini" ] && [ ! -L "$ini" ] || continue
    if grep -q '^\[quality\]' "$ini"; then
        continue
    fi
    tmp="$(mktemp "$ini.XXXXXX")"
    cat -- "$ini" > "$tmp"
    printf '\n# On this day (Memories): the photo quality model. Optional - a device\n# without the file measures photos by sharpness and exposure alone.\n[quality]\nmodel-path=%s\n' "$QUALITY_ONNX" >> "$tmp"
    chown --reference="$ini" "$tmp"
    chmod --reference="$ini" "$tmp"
    sync "$tmp" 2>/dev/null || sync
    mv -Tf "$tmp" "$ini"
    added=$((added + 1))
done

echo "release 150 applied: photo_quality, memories_hidden, settings.memories_enabled; [quality] added to $added config file(s)"
