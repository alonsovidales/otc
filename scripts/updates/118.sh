#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 118: the device keeps the user's language (docs/i18n.md).
#
# On the primary and every per-user database (issue #82), each of which
# carries its own schema:
#   settings.language           the language chosen for every app, '' for
#                               Automatic (SetLanguage, owner only)
#   settings.last_ui_language   the language of the app that last
#                               registered for pushes
#   notifications.msg_key, msg_args, msg_lines
#                               an alert as a catalog key and arguments,
#                               next to the English title/details
#   notifications.update_version
#                               one update alert per version
# Nothing is filled in: every row keeps meaning what it meant (Automatic,
# English alerts). See db.sql's comments on each column. The new binary
# reads the defaults on a database this hasn't reached.
#
# Idempotent: every column is ADD COLUMN IF NOT EXISTS, so a database that
# already has them (a fresh install, where db.sql created them) is left as
# it is.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS `language` VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN IF NOT EXISTS `last_ui_language` VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `msg_key` VARCHAR(96) NULL;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `msg_args` MEDIUMTEXT NULL;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `msg_lines` MEDIUMTEXT NULL;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS `update_version` VARCHAR(16) NULL;
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

echo "release 118 applied: settings.language, settings.last_ui_language, notifications.msg_key, msg_args, msg_lines, update_version"
