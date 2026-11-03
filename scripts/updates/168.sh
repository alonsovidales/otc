#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 168 (issue #204): the device's service is no longer tailscaled's
# operator.
#
# Release 2 made the otc user Tailscale's operator, and install.sh and the
# root runner did so again whenever Funnel was on, so that the service
# could drive tailscaled (root) through the CLI - which let it do far more
# than Funnel on its own web port: change the account or the tailnet,
# routes and exit nodes, serve, Tailscale SSH. From this release the root
# runner (scripts/tailscale-runner/otc-tailscale-runner.sh) runs the few
# fixed commands Funnel needs itself - the sign-in with an auth key too -
# and the service only asks it, one word at a time.
#
# This puts that runner in place first - the one it replaces would make
# otc the operator again were Funnel turned on during the rest of this
# update - and then takes the otc user's operator away (the runner's
# --drop-operator): at once where tailscaled runs; where it is stopped
# (Funnel off) but its saved state still names otc, tailscaled runs just
# long enough for that, so no later "on" starts a daemon the service can
# drive. update.sh installs the same runner again later in this update.
#
# Idempotent: the same files installed again, and an operator already gone
# is left alone. Not taking it away only warns - the runner takes it away
# whenever it starts tailscaled, and a device whose update fails here would
# get no fix at all.
set -euo pipefail

src="${OTC_VERIFIED_SRC:-}"
dir="$src/scripts/tailscale-runner"
if [ -z "$src" ] || [ ! -f "$dir/otc-tailscale-runner.sh" ]; then
    echo "release 168: no verified source with scripts/tailscale-runner (OTC_VERIFIED_SRC=${src:-unset})" >&2
    exit 1
fi

# put <mode> <src> <dst>: written beside the destination and renamed over
# it, so a run of the old runner never reads half of the new one.
put() {
    if ! install -m "$1" "$2" "$3.new.$$" || ! mv -Tf "$3.new.$$" "$3"; then
        rm -f "$3.new.$$"
        return 1
    fi
}
put 0755 "$dir/otc-tailscale-runner.sh" /usr/local/bin/otc-tailscale-runner
put 0644 "$dir/otc-tailscale.service" /etc/systemd/system/otc-tailscale.service
put 0644 "$dir/otc-tailscale.path" /etc/systemd/system/otc-tailscale.path
systemctl daemon-reload
systemctl enable --now otc-tailscale.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-tailscale.path"

if ! command -v tailscale >/dev/null 2>&1; then
    echo "release 168 applied: Tailscale is not installed here, so it has no operator"
    exit 0
fi
if /usr/local/bin/otc-tailscale-runner --drop-operator; then
    echo "release 168 applied: only root drives tailscaled now"
else
    echo "WARNING: release 168 could not take tailscaled's operator away from the otc user - the runner tries again whenever Funnel is turned on"
fi
