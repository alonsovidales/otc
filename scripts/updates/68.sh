#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 68: MariaDB must be able to reach its own data again. Release 58
# made every storage folder 0750, the storage root included - and on a
# device set up from the image the root is the RAID mount, which also holds
# MariaDB's datadir (/mnt/storage/mysql). MariaDB kept running, but would
# not have started again after the next reboot. Others may now pass
# through (o+x) every folder above the datadir, without listing or reading
# anything. Idempotent.
set -euo pipefail

datadir=$(awk -F= '/^[[:space:]]*datadir[[:space:]]*=/{gsub(/[[:space:]]/,"",$2); print $2}' /etc/mysql/mariadb.conf.d/*.cnf /etc/mysql/my.cnf 2>/dev/null | tail -1)
case "$datadir" in
  /mnt/*)
    d=$(dirname "$datadir")
    while [ "$d" != "/" ] && [ "$d" != "/mnt" ]; do chmod o+x "$d"; d=$(dirname "$d"); done
    echo "release 68 applied: MariaDB's datadir $datadir is reachable"
    ;;
  *) echo "release 68: MariaDB's datadir (${datadir:-default}) isn't on the storage, nothing to do" ;;
esac
