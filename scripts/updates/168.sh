#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 168. Two parts: the device's service is no longer tailscaled's
# operator (issue #204), and Images' month counts kept as the library
# changes (issue #206).
#
# Issue #204: release 2 made the otc user Tailscale's operator, and
# install.sh and the root runner did so again whenever Funnel was on, so
# that the service could drive tailscaled (root) through the CLI - which
# let it do far more than Funnel on its own web port: change the account
# or the tailnet, routes and exit nodes, serve, Tailscale SSH. From this
# release the root runner (scripts/tailscale-runner/otc-tailscale-runner.sh)
# runs the few fixed commands Funnel needs itself - the sign-in with an
# auth key too - and the service only asks it, one word at a time.
#
# This puts that runner in place first - the one it replaces would make
# otc the operator again were Funnel turned on during the rest of this
# update - and then takes the otc user's operator away (the runner's
# --drop-operator): at once where tailscaled runs; where it is stopped
# (Funnel off) but its saved state still names otc, tailscaled runs just
# long enough for that, so no later "on" starts a daemon the service can
# drive. update.sh installs the same runner again later in this update.
# Not taking the operator away only warns - the runner takes it away
# whenever it starts tailscaled, and a device whose update fails here
# would get no fix at all.
#
# Issue #206: Images' month counts (the date scrubber) without a pass over
# the whole library at every Images opening - kept as the library changes
# instead (dao/months.go): photo_months has, per month, how many photos
# have their oldest copy in it; photo_month_hashes each content's months,
# what a change to its rows takes away there; settings.photo_months_for
# the folders kept out of Images the counts were built for (NULL: not
# built) and settings.photo_months_run the process keeping them. Nothing
# is counted here: the device builds them in the background at its first
# Images opening, and counts the old way until then. A database it can't
# change is reported and left: the device counts its months the old way
# (a binary newer than its schema carries on without the columns). Run on
# the primary database and on every per-user database (issue #82), each
# of which carries its own schema.
#
# Idempotent: the same runner files installed again, an operator already
# gone left alone, CREATE TABLE / ADD COLUMN IF NOT EXISTS.
set -uo pipefail

# --- Issue #204: only root drives tailscaled -------------------------------

# put <mode> <src> <dst>: written beside the destination and renamed over
# it, so a run of the old runner never reads half of the new one.
put() {
    if ! install -m "$1" "$2" "$3.new.$$" || ! mv -Tf "$3.new.$$" "$3"; then
        rm -f "$3.new.$$"
        return 1
    fi
}

tailscale_operator() {
    local src="${OTC_VERIFIED_SRC:-}"
    local dir="$src/scripts/tailscale-runner"
    if [ -z "$src" ] || [ ! -f "$dir/otc-tailscale-runner.sh" ]; then
        echo "release 168: no verified source with scripts/tailscale-runner (OTC_VERIFIED_SRC=${src:-unset})" >&2
        return 1
    fi
    put 0755 "$dir/otc-tailscale-runner.sh" /usr/local/bin/otc-tailscale-runner || return 1
    put 0644 "$dir/otc-tailscale.service" /etc/systemd/system/otc-tailscale.service || return 1
    put 0644 "$dir/otc-tailscale.path" /etc/systemd/system/otc-tailscale.path || return 1
    systemctl daemon-reload || return 1
    systemctl enable --now otc-tailscale.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-tailscale.path"

    if ! command -v tailscale >/dev/null 2>&1; then
        echo "release 168: Tailscale is not installed here, so it has no operator"
        return 0
    fi
    if /usr/local/bin/otc-tailscale-runner --drop-operator; then
        echo "release 168: only root drives tailscaled now"
    else
        echo "WARNING: release 168 could not take tailscaled's operator away from the otc user - the runner tries again whenever Funnel is turned on"
    fi
}

tailscale_operator || exit 1

# --- Issue #206: Images' month counts --------------------------------------

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    if ! mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS photo_months (`month` INT NOT NULL, `images` INT NOT NULL DEFAULT 0, `media` INT NOT NULL DEFAULT 0, PRIMARY KEY (`month`)) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS photo_month_hashes (`hash` VARCHAR(64) NOT NULL, `images` INT NULL, `media` INT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS photo_months_for MEDIUMTEXT NULL;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS photo_months_run VARCHAR(32) NULL;
SQL
    then
        echo "WARNING: release 168 could not add the month counts to ${db}; it counts them the old way"
    fi
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

echo "release 168 applied: tailscaled's operator, photo_months, photo_month_hashes, settings.photo_months_for, settings.photo_months_run"
