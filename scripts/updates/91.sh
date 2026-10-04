#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 91: photo searches answer 30 photos a page instead of 5 ([tagger]
# max-images-search). Five made every grid and picker ask many times over,
# and a picker that only asks again on scroll stopped at 10. Only the old
# default is changed. Idempotent.
set -euo pipefail

for f in /etc/otc_*.ini; do
    [ -f "$f" ] || continue
    if grep -qE '^[[:space:]]*max-images-search[[:space:]]*=[[:space:]]*5[[:space:]]*$' "$f"; then
        sed -i -E 's/^[[:space:]]*max-images-search[[:space:]]*=[[:space:]]*5[[:space:]]*$/max-images-search=30/' "$f"
        echo "photo search page size raised in $f"
    fi
done

echo "release 91 applied"
