#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 47: the service logged its database password on every
# connection ("connecting to DB: otc:<password>@tcp(...)"), in a log every
# account on the device could read. The binary no longer does; this blanks
# the password out of the logs already written (the current one and the
# rotated *.old ones), makes them readable by the service only, and makes
# /etc/otc_<env>.ini readable by root and the service only.
# Idempotent.
set -euo pipefail

shopt -s nullglob
for f in /var/log/otc/otc.log /var/log/otc/otc.log_*.old; do
    owner=$(stat -c %U:%G "$f")
    sed -i -E 's#(connecting to DB: [^:]+:)[^@]*@#\1***@#' "$f"
    chown "$owner" "$f"
    chmod 600 "$f"
done

# The config holding that password was readable by every account too.
for f in /etc/otc_*.ini; do
    chown root:otc "$f"
    chmod 640 "$f"
done

echo "release 47 applied: database password removed from the logs"
