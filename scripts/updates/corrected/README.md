# Corrected release scripts

A release script, `scripts/updates/<N>.sh`, is never edited once it is released: its SHA-256 is
line N of the signed manifest (`scripts/updates/VERSIONS`), which every device checks before it
runs the script as root.

When a released script turns out to be wrong, its corrected version goes here, as `<N>.sh`.
`scripts/update.sh` checks release N's original script against the manifest as always, and then
runs the corrected one in its place. The corrected script comes from the same verified release
source (`src.tar.gz`, whose hash the manifest also signs), so it is trusted exactly as much as the
original was. A device that already ran release N is not affected: scripts only ever run on the
devices still missing their release.

| release | what was wrong |
|---|---|
| 5 | installed the update runner from the repository's branch, unsigned |
| 47 | root edited and chowned the logs by name, in a directory the service's user owns |
| 58 | root ran the storage's chmods over a tree the service's user owns |
| 158 | a run cut between adding a column and filling it was never finished |
| 161 | a device whose apt failed never got another update |

The releases before 92 that loop over the device's users are run with update.sh's own `mysql`
(`scripts/update-runner/release-mysql.sh`) instead of a corrected copy each: it lets only the
database names the device generates reach the client. Every release script runs with it, and a
failed read of the users list, or of whether a user's database exists, stops that release instead
of reading as "no other users" or "no such database".

A corrected script follows the rules of any release script: idempotent, safe on every older
release, and only ever run as part of an update.
