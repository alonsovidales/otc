#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Settings > Restart device in the container. The device only checks that
# this file exists (devicerestart) and drops /var/lib/otc/reboot.request;
# otc-entrypoint, which watches for it, restarts the device's process and
# leaves the database and the machine running. There is no machine to
# restart from inside a container, so this does nothing itself.
echo "in the container, otc-entrypoint restarts the device when it sees /var/lib/otc/reboot.request"
