#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 47, as release 169 corrected it: update.sh runs this in place of
# updates/47.sh, which stays as it was signed (see README.md here).
#
# Release 47: the service logged its database password on every
# connection ("connecting to DB: otc:<password>@tcp(...)"), in a log every
# account on the device could read. The binary no longer does; this blanks
# the password out of the logs already written (the current one and the
# rotated *.old ones), makes them readable by the service only, and makes
# /etc/otc_<env>.ini readable by root and the service only.
#
# As it was, root edited, chowned and chmodded the logs by name in
# /var/log/otc, which the otc user owns: a symlink put in a log's place -
# before the edit, or after sed's rename - made root copy the file it
# pointed at into a log the otc user could read, or hand it that file. The
# logs are the otc user's, so they are now worked on as that user, who can
# only ever reach what is already its own; only regular files are touched.
#
# Idempotent.
set -euo pipefail

shopt -s nullglob
if id otc >/dev/null 2>&1; then
    for f in /var/log/otc/otc.log /var/log/otc/otc.log_*.old; do
        if [ ! -f "$f" ] || [ -L "$f" ]; then
            continue
        fi
        runuser -u otc -- sed -i -E 's#(connecting to DB: [^:]+:)[^@]*@#\1***@#' "$f" \
            || echo "WARNING: could not blank the database password out of $f"
        runuser -u otc -- chmod 600 "$f" || echo "WARNING: could not make $f readable by the service only"
    done
else
    echo "release 47: no otc user, so no service logs to clean"
fi

# The config holding that password was readable by every account too. In
# root's /etc.
for f in /etc/otc_*.ini; do
    chown root:otc "$f"
    chmod 640 "$f"
done

echo "release 47 applied: database password removed from the logs"
