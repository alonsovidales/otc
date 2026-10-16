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
4. **Owner password and bridge**: sign in once (through an SSH tunnel to port 8080) to set the
   owner password, which becomes the published one. The `demo` name is claimed for the owner's
   bridge account from Settings > Bridge; the owner signs in there.
5. **Content**, with demo mode still off: turn face recognition on, upload the sample library and
   files, wait for processing, name the people, make the collections and posts.
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
