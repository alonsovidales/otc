#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The mysql a release script runs with. update.sh's run_release hands every
# scripts/updates/<N>.sh it runs (as root) a mysql function that runs this
# file with bash, the real client in OTC_RELEASE_MYSQL. Everything a release
# asks for reaches that client unchanged, but for two things the scripts
# released before could not do by themselves, and can no longer be changed
# to do (their hashes are in the signed manifest):
#
# - Releases before 92 (OTC_RELEASE_MYSQL_NAMES=1) passed each
#   users.db_name - a column the otc service can write - straight to
#   mysql's command line, where a name starting with - is an option:
#   --defaults-extra-file= made root's client read a file the otc user
#   wrote, which can make the client load code of that user's. Those
#   scripts only ever pass -N, -B, -e <statement> and one database: otc, or
#   an additional user's otc_<32 hex digits>, the only names the device
#   generates. Anything else is skipped, as release 92 and later skip it,
#   and so is a user's database that doesn't exist, where those scripts
#   used to fail.
# - Every release read the users list with `SELECT db_name FROM users`
#   behind 2>/dev/null || true, so a database that didn't answer (MariaDB
#   restarted by a security update, an error) read as "no other users",
#   and the release was recorded as applied with every other user's
#   database left behind for good. A failure of that query other than "no
#   users table" (error 1146, a device from before additional users) is
#   noted by creating the file OTC_RELEASE_MYSQL_FAILED names: update.sh
#   then runs the release again once the database answers, or stops the
#   update there.
set -uo pipefail

real="${OTC_RELEASE_MYSQL:-}"
if [ -z "$real" ] || [ ! -x "$real" ]; then
    echo "release-mysql: no mysql client to run (OTC_RELEASE_MYSQL)" >&2
    exit 127
fi

# What the call is: the database named, whether it reads the users list,
# and whether it has anything the scripts before 92 never pass.
args=("$@")
n=${#args[@]}
db=""
users_query=0
unexpected=0
for ((i = 0; i < n; i++)); do
    case "${args[i]}" in
        -e)
            i=$((i + 1))
            if [ "$i" -lt "$n" ]; then
                if [ "${args[i]}" = "SELECT db_name FROM users" ]; then
                    users_query=1
                fi
            else
                unexpected=1
            fi
            ;;
        -N | -B) ;;
        -*) unexpected=1 ;;
        *)
            if [ -n "$db" ]; then
                unexpected=1
            fi
            db="${args[i]}"
            ;;
    esac
done

if [ "${OTC_RELEASE_MYSQL_NAMES:-0}" = 1 ]; then
    # Not echoed: the name is text the otc service wrote.
    if [ "$unexpected" = 1 ]; then
        echo "skipping a users row whose db_name this device did not generate" >&2
        exit 0
    fi
    if [ -n "$db" ] && [ "$db" != otc ]; then
        if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
            echo "skipping a users row whose db_name this device did not generate" >&2
            exit 0
        fi
        if ! found="$("$real" -N -B -e "SHOW DATABASES LIKE '$db'")"; then
            echo "could not ask the database whether $db exists" >&2
            exit 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database" >&2
            exit 0
        fi
    fi
fi

if [ "$users_query" = 1 ]; then
    # Its output as it is; its error read, then passed on.
    exec 3>&1
    err="$("$real" "$@" 2>&1 >&3 3>&-)"
    rc=$?
    exec 3>&-
    if [ -n "$err" ]; then
        printf '%s\n' "$err" >&2
    fi
    if [ "$rc" -ne 0 ] && [ -n "${OTC_RELEASE_MYSQL_FAILED:-}" ] && ! grep -q '^ERROR 1146 ' <<<"$err"; then
        : >"$OTC_RELEASE_MYSQL_FAILED"
    fi
    exit "$rc"
fi

exec "$real" "$@"
