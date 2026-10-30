# The demo device

A public device anyone can sign in to, to look around Off The Cloud before buying the hardware:
`demo.off-the.cloud`, on a dedicated server (not a Pi). It runs the released device software with
`[otc] demo = true`, which refuses every change (`websocket/demo.go`): uploads, deletes, renames,
people, collections, posts, sharing, friends, settings, the password, users, updates, restarts,
storage, Wi-Fi and the bridge. Refusals carry `error_code` "demo" and `dev.err.demo` ("This is a
demo device: changes are turned off."). What the apps send by themselves after signing in (their
language, push tokens, notifications seen) is answered ok and stored nowhere, so visitors don't
mix each other up or get the demo's pushes; the home-network endpoint is answered as absent.

It is for the web app only: the website links to `https://demo.off-the.cloud`, and the phone apps
have no demo support. The device tells the web app before sign-in
(`PubKey.demo`, `PubKey.demo_password`) and after (`Status.demo`): signed out, the page offers "Try
the demo" with the published password; signed in, a banner says it is a demo. Every screen and
setting looks as on any device (Logs aside); a change tried shows "This is a demo device: changes
are turned off." instead of happening, and likes stay on the visitor's page alone. `Status` reads
like a Pi's: 8 GB of memory, a 64 GB card, two 2 TB disks mirrored and in sync, with the library's
real usage.

## Setting it up

1. **Server**: Debian 13 (its OpenCV is the one gocv needs; on Ubuntu 24.04 the installer builds
   OpenCV first), x86_64, with a sudo user that logs in with an SSH key: the installer turns off
   root and password logins.
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
   password, which becomes the published one. Then put the device on the bridge as `demo`, in
   either of two ways: on the device's Settings > Bridge with `bridge-addr` emptied first
   (a device that thinks it is on the bridge offers no sign-in), or by registering `demo` on the
   account page ("register a name for a manual install") and running `sudo otc-demo-identity` on the
   server, which asks for the identity shown there (never on a command line) and switches the
   device onto the bridge.
5. **Content**, with demo mode still off: turn face recognition on, then, through an SSH tunnel to
   port 8080 (open the web app as `127.0.0.1`, not `localhost`: on `localhost` a development
   build of the web app connects to its development device instead):
   - `otc-put` uploads the library made as described in `docs/demo-device/content/README.md`
     (`echo PASSWORD | otc-put -address ws://127.0.0.1:18080/ws <content>/library /`);
   - once the device has analysed it (`pending_analysis` empty), `otc-demo-setup` turns Places on
     (the map in Images, off on every other device until its user turns it on), sets the profile,
     names each person after the name most of their photos list, deletes the rest (the dog,
     strangers) and makes the collections and posts. A demo set up before Places gets it with
     `otc-demo-setup -places-only -address …` (password on stdin), with demo mode off for it as
     for the rest of this step. It turns Smart search (beta) on too - a device without its
     model downloads it first (since release 152 nothing else fetches it; it says so and goes
     on); `-smart-search-only` does just that, for a demo set up before it - the device then
     describes the photos in the background, and the demo allows text searches but refuses the
     switch;
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
