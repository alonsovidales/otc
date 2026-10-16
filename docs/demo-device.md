# The demo device

A public device anyone can sign in to, to look around Off The Cloud before buying the hardware:
`demo.off-the.cloud`, on a dedicated server (not a Pi). It runs the released device software with
`[otc] demo = true`, which refuses every change (`websocket/demo.go`): uploads, deletes, renames,
people, collections, posts, sharing, friends, settings, the password, users, updates, restarts,
storage, Wi-Fi and the bridge. Refusals carry `error_code` "demo" and `dev.err.demo` ("This is a
demo device: changes are turned off."). What the apps send by themselves after signing in (their
language, push tokens, notifications seen) is answered ok and stored nowhere, so visitors don't
mix each other up or get the demo's pushes; the home-network endpoint is answered as absent.

The device tells the apps before sign-in (`PubKey.demo`, `PubKey.demo_password`) and after
(`Status.demo`): the sign-in screens offer "Try the demo" with the published password, and the apps
show a banner, hide what would change something and never upload the phone's photos to it.
`Status` reads like a Pi's: 8 GB of memory, a 64 GB card, two 2 TB disks mirrored and in sync, with
the library's real usage.

## Setting it up

1. **Server**: Debian 13 (Ubuntu's OpenCV is too old for gocv), x86_64, with a sudo user that logs
   in with an SSH key: the installer turns off root and password logins.
2. **Install** with the one-line installer, as a demo server:

   ```
   curl -fsSL https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/verified-install.sh \
     | sudo OTC_DEMO=1 bash -s -- demo
   ```

   `OTC_DEMO=1` implies `OTC_SKIP_RAID=1` (the server's own disks and arrays are never touched),
   leaves out the RAID watcher, turns off the Wi-Fi watchdog, keeps 60 standby connections to the
   bridge (every visitor's session holds one) and keeps the `demo` lines below across re-runs.
3. **Firewall**: nothing needs to reach the server but SSH. The device listens on 80, 8080 and
   8443 on every interface; allow 22 in, keep loopback (ffmpeg reads videos from 127.0.0.1:8080)
   and drop the rest.
4. **Owner password and bridge**: the installer's `OTC_OWNER_PASSWORD_FILE` sets the owner
   password, which becomes the published one. The `demo` name is registered on the owner's account
   page ("register a name for a manual install"), and `sudo otc-demo-identity` on the server stores
   the identity it shows (asked for, never on a command line) and switches the device onto the
   bridge.
5. **Content**, with demo mode still off: turn face recognition on, then, through an SSH tunnel to
   port 8080 (open the web app as `127.0.0.1`, never `localhost`, which talks to Cala):
   - `otc-put` uploads the library (`echo PASSWORD | otc-put -address ws://127.0.0.1:18080/ws
     ~/src/otc-demo-content/library /`);
   - once the device has analysed it (`pending_analysis` empty), `otc-demo-setup` sets the profile,
     names each person after the name most of their photos list, deletes the rest (the dog,
     strangers) and makes the collections and posts;
   - `docs/demo-device/content/friends.py` builds three made-up friends (addresses under
     `demo.off-the.cloud`, which no bridge name can take and which don't resolve, so the friend
     sync fails quietly), their posts and their comments and likes; copy its media into
     `unenc-storage-path` (owner otc, mode 600) and run its `seed.sql` with `sudo mysql otc`. It
     also gives the family's posts their real dates.
6. **Demo mode on**: add under `[otc]` in `/etc/otc_dev.ini`, then `sudo systemctl restart otc`:

   ```
   demo=true
   demo-password=<the owner password>
   ```

## Keeping it up to date

Updates can't be started from the apps (the demo refuses them). Over SSH:
`date -u +%FT%TZ | sudo -u otc tee /var/lib/otc/update.request`, the same trigger Settings writes.
To change the content, take demo mode off (comment the line, restart), change it, and turn it on
again.
