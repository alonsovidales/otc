# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

OTC ("Off The Cloud") is a self-hosted NAS + ethical social network. A Go server (`otc`) runs on a
Raspberry Pi with a RAID1 disk pair, storing photos/videos/documents and serving a React web app,
iOS/macOS Swift apps, and a Windows/macOS sync client. Devices are not directly reachable from the
internet, so a separate relay service (`bridge/`) proxies WebSocket connections between a device and
its friends'/owner's clients using a subdomain like `<device>.off-the.cloud`.

Two independent Go modules share one `go.mod`/proto definition:
- **Device (root)**: `bin/otc.go` — the NAS + social server that runs on the Pi.
- **Bridge** (`bridge/`): `bridge/bin/otc_bridge.go` — the public relay server (its own `dao`,
  `websocket`, `api`, deployed separately to a cloud VM, not the Pi).

## Build & deploy commands

All builds/deploys are driven by `make`, and most targets `ssh`/`scp` straight to a live device —
they are not local-only build steps. `makefile` targets `TARGET` (edit this to the device's
hostname/IP, e.g. `pit.otc`) over SSH as user `otc`.

```
make all      # clean + regenerate protobuf + sync source + build web + build & restart device binary
make otc      # cross-compile the device binary for linux/arm64 (see toolchain note below) and scp it to TARGET
make pi       # build ON the device itself over SSH (stop otc, go build, restart otc)
make web      # npm run build (web/) then copy dist/ into ios app and scp to TARGET (not the bridge - see issue #95)
make pb       # regenerate proto/generated/*.go, web/src/proto/*.ts, and app/*/OffTheCloud/*.pb.swift from proto/messages.proto
make sync     # rsync the whole repo to the device (excludes handled by rsync flags)
make clean    # remove generated protobuf, the otc binary, and ios web-dist
make image    # issue #38: build the flashable Pi image ON TARGET (scripts/build_image.sh) and copy it to dist/
make image-publish  # upload dist/off-the-cloud-rpi-lite-arm64.img.xz to the rolling "image" GitHub release
```

Without a Pi at hand the image builds in the Lima VM just as well (arm64 Ubuntu with losetup and
chroot): `limactl copy -r scripts otc:/tmp/otcrepo/scripts`, then in the VM `sudo
WORK=$HOME/image-build SRC_REPO=/tmp/otcrepo bash /tmp/otcrepo/scripts/build_image.sh` - WORK on
the VM's disk, not under `/tmp`, which is a RAM disk too small for the decompressed image - and
`limactl copy` the `.img.xz` and its `.sha256` into `dist/` for `make image-publish`. The base is
one dated Raspberry Pi OS Lite release pinned with the SHA-256 Raspberry Pi publishes beside it
(`BASE_URL`/`BASE_SHA256` in build_image.sh, checked before anything is unpacked; a cached base from
another pin is fetched again): a newer base is a deliberate edit there.

The image (issue #38) is stock Raspberry Pi OS Lite plus `scripts/setup_wizard.py` (a root,
stdlib-only web wizard on port 80: WiFi, device name reserved on the bridge via its public
`/api/name-available` + `/api/claim`, disks, then runs `install.sh` and shows its `[n/10]` steps as
a progress bar) and `scripts/network_setup.py` (root: unblocks the radio, makes the wizard's Wi-Fi
join, keeps any hotspot down). Since the 2026-09-29 security advisory setup is over Bluetooth only
(#137, below): the wizard listens on 127.0.0.1 (`OTC_SETUP_BIND`) and is reached only through
setup_ble.py, and the open "Off The Cloud" hotspot (a virtual `uap0` beside `wlan0`; one radio
means one channel, so a join over it was pinned to 2.4 GHz) is raised only with
`OTC_ALLOW_HOTSPOT=1`, which nothing sets - its code (captive DNS, `ap_allowed_on`, the band pin,
the wizard's hotspot texts) stays for that switch alone. network_setup.py takes down a hotspot left
over from an older image on every poll before setup and rechecks once a minute after it
(`ap_recheck_s`), lifting the 2.4 GHz pin once (`lift_band_limit`); a Bluetooth join uses any band
(`any_band` in the join request). network_setup.py makes its join only before setup - never once
root's `/etc/otc/.install-complete` exists (`setup_done()`'s own marker is in the otc user's
`/var/lib/otc`) - opens the request with O_NOFOLLOW (a regular file of at most 4 KiB), and
removes anything else found under that name (a directory too) without reading it. Release 143 puts
it on devices imaged before 2026-09-29 (whose copy raised the open hotspot whenever the network was
lost) and deletes the hotspot's NetworkManager profile (an access point named `OTC-Setup` or with that
SSID); `update.sh` reinstalls it wherever `/usr/local/bin/network_setup.py` exists. After setup the
Wi-Fi changes only through Settings › Wi-Fi (`network`, below) or the console. The wizard changes state only through `update_state()` under
`_state_lock` (never held across bridge calls) and writes files with `atomic_write`. `raid_watch.py`
runs no mdadm query or repair unless md0 is in `/proc/mdstat`, and backs off failed adds.
Fallback if the phone still loses the page: the device reports its LAN address to the bridge under
a one-time token (`POST /api/setup-beacon`, bridge DB `setup_beacons`, 10-minute expiry) and the
page polls `GET /api/setup-lookup`. Recovery safety: an array assembled without an mdadm.conf (the wizard's own check, a fresh
image) comes up as `/dev/md127`, not `md0`; `install.sh` adopts any active array on its disks and
reassembles it as `md0`, never wipes a disk that still has a RAID superblock, and with
`OTC_RECOVERY=1` (the wizard's Recover) refuses to build a fresh array at all - the wizard only
sets `OTC_RAID_CONFIRM_WIPE` on a fresh install. A mirror down to one disk is recovered too: the
wizard names that disk as both `OTC_DISK1` and `OTC_DISK2`, which only `OTC_RECOVERY=1` accepts,
and the adopted array is reassembled with `--run` (without it a degraded array is only gathered,
inactive). The storage's fstab line has `nofail,x-systemd.device-timeout=3min` (install.sh, release
143 for older devices: only the md0 line's options change) and `otc.service`/`mariadb.service` get
`RequiresMountsFor=/mnt/storage` drop-ins (`otc-storage.conf`): a disk missing or slow at boot no
longer stops the boot in emergency mode with no network or SSH. The device comes up without its
storage; the service and MariaDB don't start (never on the empty directory under the mount point),
with the disks missing raid_watch shows both LEDs red, and the apps see the device offline until the
disks are back and it is restarted. A fourth: the Pi has no battery-backed clock, so an install that starts
before NTP syncs sees every Debian repository as "not live until <date>" and apt fails with
code 100 - `install.sh`'s `ensure_clock` waits for NTP and falls back to a verified HTTPS
Date header (forward only). Three Pi 5 pitfalls learnt the hard way: the image strips SSH host keys, so enabling ssh on an older card needs `ssh-keygen -A` first (newer images regenerate them from a ssh.service drop-in); NetworkManager's WiFi
switch ships off on stock Raspberry Pi OS (`nmcli radio wifi on`), and an idle `wlan0` reports
channel 34 (5170 MHz) - never copy a channel from an interface that isn't connected. The wizard also handles a re-imaged
device: it assembles any existing RAID1 array (`mdadm` is the one package baked into the image)
and, if it holds an OTC database, offers recovery - `install.sh` reassembles instead of wiping and
the identity in that database wins, so no name is asked; after installing it polls the bridge's
`/api/device-online` and only asks for a name if the recovered device never shows up. It contains
no otc code, so it only needs rebuilding when those scripts change. Issue #137: `scripts/setup_ble.py`
(unit `otc-setup-ble.service`, same lifetime as the wizard, needs the image's `python3-dbus` +
`python3-gi`) offers the *same* wizard over Bluetooth LE for the apps - one GATT service
(`0f7c5e70-…-0001`; write chunks of a JSON request in, notify chunks of a raw-DEFLATE JSON
answer out, MTU-3 per chunk) that forwards every request to the wizard on 127.0.0.1:80.
`BluetoothSetupView` (iOS `.swift` / Android `.kt`) shows the wizard's page in a web view: the
page itself comes through a scheme handler / `shouldInterceptRequest`, and a script injected
into `<head>` swaps `fetch()` for a call into the app (`WKScriptMessageHandlerWithReply` /
`@JavascriptInterface`), because neither web view hands POST bodies to an interceptor
reliably. The view watches `/api/state` for `install.phase == "online"` and then offers "Use
this device", which fills the onboarding form's endpoint (bridge name → wss, no bridge →
`ws://otc.local:8080/ws`). `python3 scripts/setup_ble.py --selftest` checks the framing and
forwarding without Bluetooth; for the phones, `scratchpad`'s `blesim.swift` (a CoreBluetooth
peripheral on the Mac forwarding to a dry-run wizard, e.g. the Lima VM's on port 8090) stands
in for a device - it needs Bluetooth permission for the terminal, which macOS prompts for. The
Bluetooth setup is open by design and only exists before the install completes.
The link recovers by itself: on iOS/macOS any state but poweredOn forgets the peripheral
(`forgetLink`) and a device already picked is rescanned for once Bluetooth is back; Android checks
every GATT step, drops and rescans on a failure, with a 30 s connect watchdog.
The owner password is chosen on the wizard's name step, and since both channels are readable by
anyone nearby it never travels in the clear: the wizard makes a one-off RSA-2048 key at start
(`SealKey`, stdlib Miller-Rabin), the page seals the password with RSA-OAEP-SHA-256 written in
plain JS (`sealWith` - no WebCrypto on a plain-HTTP page), the wizard opens it into a root-only
tmpfs file, and `install.sh` feeds it to `otc <env> init-owner-password` (as `otc`, before the
service first starts; `bin/ownerpassword.go` - a device that already has one keeps it) and
shreds the file. In the app the page also hands it to the app (`window.otcSetupPassword`,
memory only), so "Open my device" signs in without typing. Since the device drops its Bluetooth
setup service once installed, a phone locked through the install never sees the "online" state;
so the app also saves the domain (from `/api/state` once named) and that password as a *pending
setup* (Keychain / EncryptedSharedPreferences `setup_endpoint`/`setup_password`) as soon as it has
both, Onboarding fills its form from it, and `persist()` of a configured store (or Log Out)
clears it. The same step has an optional,
collapsed "Your profile and face recognition" section (issue #178): name, about text, a picture
cropped in the page to a circle (canvas pan/zoom, sent as a <=96 KB JPEG, base64) and a face
recognition checkbox, unticked. The wizard writes them to a root-only tmpfs JSON
(`OTC_SETUP_PROFILE_FILE`), and `install.sh` feeds it to `otc <env> init-profile` (`bin/otc.go`)
before the owner password - empty fields keep the defaults, and all of it can be changed later in
Settings. On Android the setup WebView needs its `WebChromeClient.onShowFileChooser` for the
picture field; WKWebView handles file inputs itself on iOS, but on macOS only through a
`WKUIDelegate` (`runOpenPanelWith`, an `NSOpenPanel` for images - without it "Choose File" does nothing). Several devices can be set up at once from several phones: each advertises
`OTC <id>` (`device_id()`: the last 4 hex digits of the Pi serial), the setup page shows that ID
with a "Blink its light" link (`/api/identify` flashes the ACT LED for 15 s), and the apps list
every device found in the first 2.5 s when there is more than one (strongest signal first),
connecting straight away when there is only one. A dropped link reconnects only to the device
picked. Leaving the setup screen sends `/__otc/release` (handled by setup_ble itself), which
frees the phone binding unless the install has started, so a device opened by mistake can
still be set up from another phone. The image has no SSH; its
console login is `otc-debug` / `off-the-cloud` (with sudo), set in `build_image.sh` and documented
in README.md under "Console access" - the only way into a device like Cala short of enabling SSH
from that console.

The bridge has its own `bridge/makefile` (`make -C bridge bridge`) which builds
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0` and deploys to every cluster node (`NODES ?= bridge1
bridge2`, SSH aliases for `ubuntu@37.187.141.41` / `ubuntu@149.202.83.7`), one at a time so the
other keeps serving. Since issue #144 (2026-10-01) the bridge is a cluster - two KS-5 nodes behind
DNS round robin (`@`, `www`, `*` at a 60 s TTL), MySQL primary on bridge1 and replica on bridge2,
Redis on the old KS-B (51.83.103.72, `redis`), everything internal over WireGuard; see
`bridge/cluster/README.md`. The old server no longer runs the bridge. The Mac's scheduled
`bridge/cluster/servercheck.sh` also checks each node's served certificate and fingerprints
`authorized_keys2` and systemd units; a host gets 240 s (plus a 300 s watchdog on the Mac), a run
cut short skips the fingerprint comparison, and a new *kind* of fingerprint line (`unit:`,
`unit_link:`) joins the baseline without an alarm.
The same `make -C bridge bridge` then runs `fleet`: it builds `otc-fleet-agent`
(`bridge/fleetagent`) and installs it with its unit on all three hosts (`FLEET_HOSTS`) - the admin
panel's Fleet tab and its email alerts (`bridge/fleet`; README "Fleet tab and alerts", which has
the one-time Redis ACL user and MySQL monitoring user to create first).

**Toolchain requirements** (not present by default in a generic dev container):
- `go.mod` requires Go **1.25+**; the system `go` may be much older (check with `go version` before
  assuming `go build`/`go vet`/`go test` will work).
- The device binary needs **CGO** and links against **ONNX Runtime** for `images_tagger` (RAM++ image
  tagging model, via `github.com/yalue/onnxruntime_go`). Cross-compiling for arm64 (the `otc` make
  target) expects an aarch64 cross-compiler (`aarch64-unknown-linux-gnu-gcc`) and an ONNX Runtime
  aarch64 build unpacked at `~/ort-aarch64/onnxruntime-linux-aarch64-<version>/`. Building/running
  natively on the device instead avoids needing the cross toolchain (see `make pi`).
- The device binary also needs **CGO** + a real **OpenCV** install (headers/libs, found via
  `pkg-config`, not just a runtime `.so`) for `face_recognition` (issue #52's "People" search, via
  `gocv.io/x/gocv` - pinned to v0.40.0, matching OpenCV 4.10.x). `apt-get install libopencv-dev
  pkg-config` on Debian 13/Raspberry Pi OS; where apt's OpenCV is older than 4.10 (Debian 12, Ubuntu
  24.04, Linux Mint 22) `scripts/install.sh` builds 4.10.0 from source into `/usr/local` (pinned by
  hash, only gocv's modules); on macOS, `brew install opencv@4` (plain `brew install
  opencv` currently installs OpenCV 5, which gocv v0.40.0 doesn't build against) and add
  `$(brew --prefix opencv@4)/lib/pkgconfig` to `PKG_CONFIG_PATH` if `pkg-config --exists opencv4`
  doesn't already find it.
- `make pb` needs `npx protoc` with the Go, Go-gRPC, ts-proto, and Swift protoc plugins available
  (`go install google.golang.org/protobuf/cmd/protoc-gen-go@<go.mod version>`, `go install
  google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`, `brew install swift-protobuf`; ts-proto comes
  from `web/node_modules` after `npm ci --prefix web` - the pb target puts both on its PATH).

**Tests**: most device and bridge packages have `_test.go` files (`face_recognition` needs the
OpenCV install above). The MySQL ones (`bridge/accounts` `TestInactivityPassMySQL`, `bridge/dao`'s
`TestAccountPasswordMySQL`, `TestEmailTokensMySQL`, `TestListAdminDevicesMySQL` under
`ONLY_FULL_GROUP_BY`) skip unless `OTC_TEST_MYSQL_DSN` points at the Lima VM's bridge schema
(with `&time_zone=%27%2B00%3A00%27`, as the bridge's own DSN). A
package var a test shortens must be restored only after the handlers reading it are done (a plain
`defer` raced under `-race`): bridge tests use `setVar` with a test bridge that waits for its
handlers; httptest's `Close` doesn't wait for hijacked websockets. `websocket/write_reply_test.go`'s
slow reader dials with gorilla's default buffers: with 16 KiB at both ends Linux loopback slows to
about 150 KB/s and a 64 KiB piece can miss the shortened `cReplyStall`, which no real peer would
see.

**Web app** (`web/`, Vite + React 19 + TypeScript + react-router). To *see* it on a device from a
terminal session, `scripts/dev/webshot.mjs` drives a headless Chrome over the DevTools protocol
(sign in, open a tab, hover/click one element, PNG per step) - through an SSH tunnel to loopback,
because macOS's local-network permission blocks a terminal-launched Chrome from LAN addresses and
Chrome doesn't resolve `.otc` names; see the header comment.
Start every headless Chrome with `--mute-audio` and every Android emulator with `-no-audio`
(webshot.mjs and docs/promo/tools/shot.mjs do): this Mac is the owner's desk machine, and an
unmuted test (a video in the viewer, the audio device opening) beeps through its speaker. Kill the
browsers, emulators and VMs a test starts when it ends.
Against a real device without releasing: `VITE_DEVICE=pit.off-the.cloud npm run build --prefix web`,
then `npx vite preview` in `web/`. Under `npm run dev` (Vite's development mode - never `DEV`, which
a build with `NODE_ENV=development` also sets) and at `localhost:4173` (`vite preview`, held to that
port by `vite.config.ts`) the app connects to `wss://$VITE_DEVICE/ws`, or Cala if the variable is
unset; anywhere else, localhost
included (the container image, an SSH tunnel), it uses the page's own host (`web/src/net/endpoint.ts`,
`node web/scripts/endpoint-test.mjs`).

The iOS simulator has no tap or type from the command line, and this Xcode has no Simulator
window to script. Facebook's `idb` drives it instead: `brew install facebook/fb/idb-companion` and
`fb-idb` in `~/.venvs/idb`, then `idb_companion --udid <sim>` and `idb connect localhost 10882`.
Use `idb ui describe-all` / `ui tap x y` / `ui text`, with points, not pixels; the tab bar's
buttons aren't in the accessibility tree, so tap them by position. Decline photo access in a
simulator signed in to a real device, or its sample photos get uploaded to it. Android's emulator
(`otc_pixel`) is driven with `adb shell input` and `uiautomator dump`.
```
npm run dev --prefix web       # local dev server
npm run build --prefix web     # tsc -b && vite build
npm run lint --prefix web      # eslint .
```

## Architecture

### Protocol: one WebSocket, protobuf-framed RPC

There is no REST API for app functionality (only `/check_healty` and static file serving exist as
plain HTTP). Everything else — file sync, photo search, social feed, friendships, settings, bridging —
goes over a single WebSocket endpoint (`/ws`) using protobuf messages defined in
`proto/messages.proto`.

- Every request is a `ReqEnvelope{ id, oneof payload }`; every response is a matching
  `RespEnvelope{ id, error, error_message, oneof payload }`. The `id` correlates async
  request/response over the one socket.
- Regenerate bindings with `make pb` after editing `proto/messages.proto` — generated Go lives in
  `proto/generated/`, generated TS in `web/src/proto/`, generated Swift in the iOS/macOS app dirs.
  Don't hand-edit generated files.
- Adding a new RPC = add a message + a case in both the `ReqEnvelope`/`RespEnvelope` `oneof`s in the
  proto, regenerate, then add a `case *pb.ReqEnvelope_ReqXxx:` in the connection handler switch (see
  `websocket/websocket.go`, `processNonAuthRequest`/the authenticated equivalent) and a client-side
  call in `web/src/net/ws.ts`. Also classify it in `websocket/demo.go`'s `demoRules` (allow,
  answer ok without storing, or refuse): `TestDemoRulesCoverEveryRequest` fails until it is.
- **Demo device** (`[otc] demo = true`, docs/demo-device.md): a public device that refuses every
  change with `error_code` "demo" (`dev.err.demo`), gated in `processMessage` before the three
  switches. It publishes `PubKey.demo`/`demo_password` before sign-in and `Status.demo` after, leaves
  `Status`/`Settings.language` unset (each visitor keeps their own, as with an older device),
  answers GetLocalEndpoint `local_unavailable`, refuses a sign-in that would set the owner password,
  and reports a Pi-like Status (8 GB, two 2 TB disks in sync; real library usage). It answers what
  Settings reads (bridge without the server's address, Tailscale, the update check kept ten minutes
  per language, empty disk and Wi-Fi lists, a Wi-Fi status of `managed` on a cable so Settings ›
  Wi-Fi shows - JoinWifi is refused); GetLogs stays refused. The demo is for the web app
  only (the owner, 2026-10-16: no demo in the phone apps; the website links to demo.off-the.cloud):
  `net/demo.ts` follows PubKey/Status.demo; signed out, "Try the demo" signs in with
  `demo_password`; signed in, a banner. Every control and Settings section shows (Logs aside), and
  a change tried shows the notice `web.shell.demo.refused` before any dialog (`refuseOnDemo`), while
  `ws.ts` answers any change request that still gets that far as the device would, unsent; likes
  stay on the page alone.
- The frontend's `web/src/net/ws.ts` / `useWS.ts` wrap the same protobuf envelope pattern for the
  browser client.
- Replies over 64 KiB (`cReplyPiece`) leave the device as one fragmented message in 64 KiB pieces
  (`writeReply`), and the bridge fragments its writes to clients above 256 KiB: every client must
  keep accepting fragmented messages.
- Passwords never cross in the clear (issue #2): each connection gets an ephemeral RSA-2048 key
  (`GetPubKey`, `rsaKeyPool`), and the clients seal `ReqAuth`'s and `ReqChangeKey`'s keys with
  RSA-OAEP-SHA-256 (`pwCrypto.ts`, `PwCrypto.swift`, `PwCrypto.kt`, otc-sync's `pwcrypto.go`). So
  are `ReqBridgeSignIn.password_sealed` and `ReqSetupTailscale.auth_key_sealed` (2026-10 review,
  `sealedOr`): the device still reads the plain `password`/`auth_key` when no sealed one comes (older
  pages; the phones' Tailscale sections still send `auth_key` plain), the web sends only the sealed
  one (the device serving the page is the one it talks to), and `tailscale up` reads the key from a
  0600 file on the TMPDIR tmpfs (`--auth-key=file:`, `authKeyArg`), never its argv. That
  key is not authenticated: the bridge terminates TLS, so a compromised bridge node could hand out
  its own key and read the password at sign-in. The owner reviewed this on 2026-10-05 and accepts
  it - the bridge nodes are the owner's own and `servercheck.sh` watches them; pinning a device
  identity was judged not viable. Reviews should not raise it again.
- File sizes (issue #187): `File.size64` (int64, field 11) is a file's size in bytes; `File.size`
  keeps that size wrapped to int32, exactly what it carried before, for the apps already in the
  stores. Devices before release 93 leave `size64` at 0, so every reader takes `size64` when it is
  non-zero and falls back to the signed `size`. Device: set a `pb.File`'s size only through
  `dao.SetFileSize(f, n)` and read it with `dao.FileSize(f)` - never set `Size` alone, and never
  scan a size column into the int32 field (a BIGINT above MaxInt32 fails the whole scan;
  websocket's `Init` has a `fileSize` alias because its `dao` parameter hides the package).
  Clients read it through one helper each: web `fileSize()` (`web/src/net/fileSize.ts`; ts-proto
  makes `size64` a required bigint, so a hand-built File literal needs `size64: 0n`), iOS
  `Msg_File.fileSize` (`formatByteCount(Int64)` for display), Android `net/FileSize.kt`
  (`byteSize`, and `sizeMatches(local)`, which compares the wrapped `local.toInt()` with `size`
  only when `size64` is unset - PhotoSync's size shortcut), the Mac's `FileMsg.fileSize`
  (`WSClient.swift`) and otc-sync's `engine.fileSize`. Never read `.size` of a File directly.
- Photo search pages (2026-10-06): `SearchPhotos.limit` (field 8) is the most photos a page may
  hold. The web's grids send 12 on the request that starts a search (no token) so the first page
  paints fast over a slow upload, and no limit when continuing with a token. The iPhone and Android
  apps keep a persistent LRU cache of grid thumbnails (release 113; iOS `ThumbDiskCache.swift` +
  `GridThumbLoader.swift`, Android `ui/common/ThumbDiskCache.kt` + `GridThumbs.kt`): keyed by content
  hash with a kind (small/big/unknown - small replaces either, big replaces unknown), scoped per
  device endpoint, 1 GB by default (Settings > Thumbnail cache: 250 MB-5 GB, usage, Clear), wiped at
  Log Out. They ask pages with `omit_thumbnails` - 12 on a first page until this device has shown
  it omits them, then 60, and 120 when continuing - and fetch only the tiles they lack with
  `GetThumbnails` (only tiles on screen or within reach of the scroll, batches of 24, on-screen
  first, Patience kind "tiles"); an old device ignores omit and its inline thumbnails go into the
  cache. A cached big/unknown one is re-asked once when a row says a small one exists, and one that
  comes back big again isn't re-asked for 7 days; a deleted photo's entry is discarded. iOS reads
  and writes nothing while protected data is unavailable (locked phone, files are
  completeUntilFirstUserAuthentication). Full-size photos (2026-10-24, `FullPhotoCache` on both,
  in `Caches/full-photos` / `cacheDir/full-photos`): the viewer keeps GetFile's bytes (a HEIC comes
  as JPEG) by the answer's `File.hash`, looked up by the item's, so reopening a photo decodes it
  from the phone, nothing asked. A tenth of the Thumbnail cache size picked (the thumbnails get the
  other 90%, Settings' usage line adds both), least recently used out, one photo at most a quarter;
  session only - emptied at launch, by Clear and at Log Out. The open photo is fetched alone; once
  it shows and the viewer stays on it 250 ms, the next then the previous (one at a time, a
  neighbour on its way waited for, never around a video) - started with it they slowed it down. The
  web does the same in memory (Full-size photos on the web, below). The device answers min(limit, its default `[tagger]
  max-images-search`, 30), never more (a page without thumbnails may be larger, below). Older
  devices ignore it. Grids that start a new search bump a search generation, so a page from the
  old search lands nowhere. A grid's tiles can come as small thumbnails (`small_thumbnails`,
  release 111 - see Thumbnails under `files_manager`).
  Pages without thumbnails (release 113): `SearchPhotos.omit_thumbnails` (field 10) answers the
  same page - same rows, order, token, `have`/serve-again and keep-out behaviour - with every
  File's `content` left out, for the phones' persistent thumbnail cache (by content hash, small or
  big), which then asks `GetThumbnails` for what it lacks. The #147 check is a stat
  (`gridThumbnailState`: the big thumbnail exists; with `small_thumbnails` also whether a small
  one is stored - queued for `thumbnail_fix.go` when not - which the row's `thumbnail_small`
  reports), never a read: a thumbnail there but unreadable is listed (and then absent from
  `GetThumbnails`, or a small one answered by the big one) where a page with thumbnails leaves it
  out - the only difference. Such a page may be larger: `pageSize` honours a limit up to
  `cMaxPageWithoutThumbnails` (200 rows, ~40 KB and two stats a row; with thumbnails a limit only
  ever shrinks the page, and 0 is the default either way; `SearchPhotos.limit`'s comment says so
  too). A token's pages may mix both kinds. Every `newSearchTest` case (serve-again, #147,
  keep-out, retries, jump, delete) runs in both kinds (`bothPageKinds`);
  `TestImageSearchPagesThroughItsToken` and `TestImageSearchLimitShrinksOnlyItsPage` run with
  thumbnails only, and `TestImageSearchPagesMixingOmittedThumbnails` pages and sizes with omit;
  `websocket`'s `TestSearchPhotosPassesItsThumbnailFlags` (in a child process: it loads a config,
  which `cfg` can't unload) checks the handler doesn't swap the two flags. Devices before 113
  ignore the flag and send content in pages of their default: clients take the content when an
  entry has it and fetch only entries without.
  A page asked again is served again (2026-10-08): every grid retries a failed page with the same
  token and `have` (`SearchPhotos.have`, field 6: photos it holds), and the device used to answer
  with the page after it, so a page whose answer was lost (a timeout dropping a late answer, a
  socket closed mid-reply, the bridge's 90 s answer) left a hole in the grid for good. A token
  (`files_manager/search_tokens.go`, `searchCursor`) now remembers where its last page started
  (`prev`), the `have` it was asked with (`before`) and the count it leaves (`after` = before +
  its photos). `resume`: `have == after` goes on; `have == before` serves that page again from
  `prev` (a #147 row without a thumbnail then may now come, and the page then ends earlier: what
  no longer fits comes first on the next page, nothing skipped), but only once the client is in
  step (`synced`: that page was asked for with the previous page's `after`; a page of a search
  resumed from an unknown token that brought only photos the client had must go on), at most
  `cMaxServedAgain` (5) times in a row, and only if no file was deleted since that page was served
  (`delFile` counts every delete, `noteDelete`: a client lowers `have` by what it deletes, so one
  that deleted as many photos as the page brought asks with `before` too); `have == 0` (clients
  that don't send it, older apps) and any other count go on, as before. The rows from `prev` on
  stay in the token (`pageCursor` compacts only what came before), and `keepOut` (#192) filters
  them too. Each search takes a sequence number as it starts (`begin`), and `store` refuses a
  cursor behind the stored one by (`pages`, `seq`): a request finishing late can't move a token
  back, and of two requests for the same page (the first still being served when the client's
  retry came) the token keeps the one started later - the retry, whose answer the client keeps.
  A last page stores nothing, so asking it again goes on from the same place. Every grid and
  picker sends `have` with token requests (web `PhotoGallery`, `NewPostPicker`,
  `DevicePhotoPicker`; Android `PhotoGalleryViewModel`, `NewPostPickerView`, `DevicePhotoPicker`;
  iOS `PhotoGallery`, `NewPostPicker`, `DevicePhotoPickerView`); with no token it means nothing.
- A photo at several paths (issue #201: typically downloaded to a phone or computer and synced
  back, with the date it came back): Images lists it once, at its **oldest copy** (earliest
  `created`, then the smallest path) among the copies it may show (outside the folders kept out
  of Images; under the folder for a folder gallery). Chosen when listing; no stored date is
  rewritten, Files lists every path with its own. `dao.SearchMedia` reads every such row newest
  first and, at one instant, by path backwards (a tag search by score first: tags and people are
  settled per hash in derived tables, so a tag's score is no longer multiplied by the copies and
  nothing relies on lax GROUP BY) and keeps each hash's last row (`oldestCopies`). `before` stays a
  per-row SQL filter, which is equivalent; #194's `newer_than` must filter after `oldestCopies`,
  never per row. The date buckets count each photo once, in its oldest copy's month (`min(created)`
  per hash), and collections count and draw their cover per member (EXISTS), whatever the copies.
  Every SearchPhotos entry carries `File.copies` (14, `optional`): the `files` rows holding its
  hash, kept-out ones included, versions not, counted per page as served (`setCopies` ->
  `dao.CountCopies`, an index lookup). A count that fails doesn't fail the page: it is logged and
  the page served with `copies` unset on every entry (so the apps offer no "every copy" for it),
  its token moving as usual.
  `DelFile.every_copy` (2, `optional`) -> `files_manager.DelEveryCopy`: every path with the hash
  (`dao.PathsOfHash`), each through `delFile` (its versions, the blob with the last, a
  `noteDelete` each), one `reconcileAnalysis`, no flag cleared; all or nothing - a copy (the asked
  path included) under an upload-only folder refuses it as `CopyUploadOnlyError`, `error_code`
  `copy_upload_only`, message `dev.files.copy_upload_only` naming the outermost such folder (never
  logged at Info). `DelFile.hash` (3, `optional`) is the content the client showed for the path
  (the tile's `File.hash`, which is the row's `files.hash`, the plain content's SHA-256): when set
  and the path is a file now holding other content (synced over, edited), nothing is deleted -
  `DelEveryCopy` checks it before `PathsOfHash`, `DelPath` before the upload-only check, with or
  without every_copy - and the answer is `ContentChangedError`, `error_code` `changed`, message
  `dev.files.content_changed` ("refresh and try again"). Ignored when empty and for a folder's
  path. A folder's path is DelPath as before; older devices ignore both fields. Measured
  on 112k rows with 14k photos at several paths (Lima VM, MariaDB 11.8): browsing 291 ms of SQL as
  before (282), the buckets 154 ms (were 327), two people 1.8 ms (5.4), a page's copies 0.3-0.6 ms.
- Scrolling up after a jump (issue #194): `SearchPhotos.newer_than` (11) asks for the photos dated
  strictly after the instant, served OLDEST first, and every answer to it has
  `ListOfFiles.oldest_first` (6) true - its last page and an empty one too; devices before #194
  ignore the field and answer newest first with it false, which the apps drop (and stop asking
  until the next sign-in). `dao.SearchMediaNewerThan` runs the plain search's query (no date in
  it) and keeps the photos whose oldest copy is after the instant, after `oldestCopies`, turned
  round (a tag search, by score, is sorted by date then path). With the same instant `before`
  and `newer_than` split the library exactly: both compare at the second (`searchCutoff` -
  `created` is a DATETIME; MariaDB truncates a fraction, MySQL may round it into the next second).
  Unlike `before`, the client sends it on every page and it pages like any search: token, `have`
  (only what the upward search brought), served again, keepOut, #147, limit, small/omit. A token
  remembers its instant (`searchCursor.newerThan`, `sameDirection`): one of the other direction
  or of another instant counts as unknown (searched again past `have`). It wins over `before`.
  An upward search costs one plain browse query when it starts (280-365 ms of SQL on the 112k-row
  library above; the filter 1 ms), and its token holds only the photos above the jump. Every
  `newSearchTest` case also runs going up (`everySearchKind`: the upward mock is dated so its
  pages are the case's paths in order, with rows at the cutoff's second and a photo synced back
  above it that must never show); `TestImageSearchNewerAndJumpSplitTheLibrary` (and its `MySQL`
  twin, end to end) and dao's `TestSearchMediaNewerThanSplitsWithBeforeMySQL` check that the two
  halves are the plain search, each photo once at the same copy; websocket's
  `TestSearchPhotosNewerThanAnswersOldestFirst` (a child process) checks the handler.
- Home network (issue #190): at home the apps reach the device directly over a **pinned TLS**
  connection; anywhere else, or on any failure, through the bridge as before.
  - Device: `lantls` keeps an ECDSA P-256 key and a self-signed cert (CN/SAN `otc-lan`, 100 years)
    in `<storage-path>/.lan-tls/` (0700/0600), created once and NEVER regenerated: the apps hold
    its pin, SHA-256 of the leaf DER. `api/local_tls.go` serves the same mux (`/ws`, `/media/`,
    static) over TLS 1.2+, HTTP/1.1 only, on `[otc-api] lan-tls-port` (default HTTP port + 363:
    8443, or 8444 for a child on 8081). `GetLocalEndpoint` (owner sessions only, in
    `processAuthRequest`) answers `LocalEndpoint{addresses, port, cert_sha256}`. Addresses are
    private IPv4 and ULA on up interfaces, never loopback, link-local, the setup hotspot (uap0 and
    10.42.0.0/24, pinned in `network_setup.py`; change both together), Tailscale or docker. It
    answers `local_unavailable` when there is no listener or address (the apps then forget the
    endpoint) and a bare error until `api.Init` has wired it. Older devices answer
    `unknown_payload`.
  - Apps (iOS `HomeNetwork.swift`, Android `net/HomeNetwork.kt` + `NetworkWatch.kt`, macOS
    `LocalRoute.swift`, otc-sync `wsclient/local.go`): after a sign-in through the configured
    address they ask `GetLocalEndpoint` and keep it bound to that address. Keychain / otc_secrets /
    `<config dir>/local.json` (0600). They keep only RFC 1918 or ULA literals (at most 8), drop it on
    `unknown_payload`/`local_unavailable`, and keep it on any other error. Every connect races all
    addresses as `wss://<addr>:<port>/ws` within 2.5 s, accepting ONLY a leaf certificate whose DER
    SHA-256 equals the pin; then sign-in runs as usual (GetPubKey + the RSA-sealed password: safe
    once the pin matched, and it works with the internet down). Route re-checks happen on a network
    change, the foreground (Mac: the popover, at most every 120 s) and a wake, never while a
    transfer runs (`TransferActivity` / `OTCConnection.transfer {}` / `folderBusy`). A check put
    off is recorded and runs after. Settings shows "Connected over your home network" or
    "Connected through off-the.cloud".
  - Video: `/media/<token>` is relative, so the base follows the route. Android plays through
    ExoPlayer on the pinned OkHttp client; iOS streams local media through an
    `AVAssetResourceLoader` on a custom scheme over the pinned URLSession, and bridge media plays as
    before. Never add a trust-all path outside the pinned clients (Play flags a verifier that
    always returns true). The web can't pin a self-signed key and stays on the bridge (or
    `http://otc.local:8080` at home).
  - Checked live on 2026-10-06 against Pit: endpoint 192.168.50.201:8443; a wrong pin was refused
    before anything was sent; sign-in over the home network took 0.3 s; a 4 MB range of a video
    returned HTTP 206 in 1.1 s.

### Device-side package layout (root Go module)

Flat, one-package-per-concern, wired together in `bin/otc.go`:

- `container` — the Docker image (`docker/`, beta): `OTC_CONTAINER=1`, set by the image only, and a
  no-op everywhere else. Sizes memory from the cgroup (`MemoryTotal`: the limit, `OTC_MEMORY_GB`, 8 GB
  without a limit), checks whether a release's image is on ghcr.io (`ImageLookup`: one token per
  check; "not published" - 401, 403 and 404 included - kept 15 minutes, a registry that failed left
  alone for 5), and holds the first-password rules (`HomeAddress`, `OTC_SETUP_UNTIL` in Unix
  seconds, at most 31 minutes ahead). Callers gate on `container.Enabled()`: `websocket/container.go`
  (`containerGate` beside `demoGate`: no disks or Wi-Fi; a first password only over a direct
  connection from a home address with no proxy headers and no other site's Origin (`indirect`) -
  the primary's within the window, an additional user's (`OTC_SUPERVISED_CHILD`) at any time or
  through the bridge - and refused when the vault can't be read; `[otc-api] trusted-proxies` for
  X-Forwarded-For; user ports from the HTTP port, checked against the host), the updater
  (image-gated releases, no Apply; a registry that can't be asked marks the check
  `ImageCheckFailed`, which keeps the alert and shows Settings' check error), raidwatch and RAID
  status (off, or the mirror's array in mirror mode, below), files_manager's sizing (`GOMAXPROCS`,
  ffmpeg threads), fatal listeners for the primary (`listenersFatal`; a user's instance logs a busy
  port and keeps serving through the bridge) and no `:80` in `api`, extra interface skips in `lantls`.
  `PubKey.container` and `RespUpdateInfo.container` tell the clients. Publishing is local, never
  GitHub Actions (the owner, 2026-10-26: Actions cost too much): `scripts/docker-release.sh
  [--no-publish] [--arch amd64|arm64] [N]` on the Mac, after `release.sh`. It verifies
  `VERSIONS.sig`, builds from the release's own `src.tar.gz` + `web-dist.tar.gz` (hashes checked)
  with `docker/docker-bake.hcl` (the build file, test and client from HEAD by `git archive`), arm64
  natively and amd64 under Rosetta (which shows amd64 containers an x86 `/proc/cpuinfo`, SSE4.1
  included), runs `docker/smoke/test-image.sh` on each (healthy, version, web app, set-password,
  `docker/smoke` sign-in client, restart, no panic), then `test-image.sh --mirror` (the same on two
  blank 3 GiB loop disks made by a privileged helper, plus the mount, `/proc/mdstat` and a down/up
  reassembly; a release without `docker/mirror` is refused), pushes each by digest only after both
  passed and checks its layers against the tested image, then tags `:<N>` (and `:beta`/`:latest` for
  the newest release); a release already on ghcr.io is left alone. It all runs in a Colima VM of its
  own (profile `otc-image`, 8 CPUs/12 GB/80 GB, `--vz-rosetta`) with its own `DOCKER_CONFIG`
  (`~/.otc/image-build/docker`) and a docker-container buildx builder (`otc-image`) whose cache keeps
  the toolchain and OpenCV between releases; the default Colima VM, its containers and `~/.docker`
  are never touched. The VM is stopped at the end if the script started it. Publishing needs gh's
  token with `write:packages` (`gh auth refresh -h github.com -s write:packages`, the owner's to
  run) and logs out of ghcr.io at the end. The test script runs on any Linux Docker host.
  Mirror mode (opt-in: `docker/compose.mirror.yaml` through `COMPOSE_FILE`, which sets
  `OTC_MIRROR=1`): `.env` names two whole disks (`OTC_MIRROR_DISK1/2`, by-id paths Docker maps to
  `/dev/otc-disk1/2`; their basenames are the labels) and, by the same strings, any that may be wiped
  when not blank (`OTC_MIRROR_ERASE`). `otc-mirror` (`docker/mirror`, pure Go, root,
  `/usr/local/sbin`) `prepare`s the RAID1 - adopts an active array on them, assembles their
  superblocks, or builds one only on free disks that are blank or listed, never touching another
  array's - and mounts it on `/mnt/storage` over the empty host folder. The entrypoint runs it before
  preflight (`--no-create` for restore), reads its `key=value` stdout without eval and dies when it
  fails (never falling back to the folder; a `/mnt/storage/mysql` volume is refused, the mirror would
  hide it), supervises `watch --array` (state, rebuild progress and the Pi's grow, in
  `/var/log/otc-storage/storage.log`) and runs `release` after MariaDB in `stop_all`. Only the
  device's environment gets `OTC_MIRROR_ARRAY`, `OTC_MIRROR_DEV1/2` (major:minor) and
  `OTC_MIRROR_LABEL1/2` (`container.MirrorArray`/`MirrorDisks`); otc-mirror itself takes
  `OTC_MIRROR_DEV1/2` as device paths (a test override), so the entrypoint never exports them. The
  override adds the two devices, `device_cgroup_rules`
  `b 9:* rmw`, `SYS_ADMIN` + `MKNOD` + `SYS_RESOURCE` (online `resize2fs`) and AppArmor/SELinux off
  (Docker's AppArmor profile refuses mount; the devices in the long `source`/`target` form, as USB
  disks' by-id names hold a `:`): root in that container is close to root on the host, while the
  device still runs with no capabilities. Rules otc-mirror keeps (each from a review or the VM):
  only arrays named `otc`/`*:otc` are used (another system's raid1 never), and once
  `config/storage-mode` names this device's mirror (`OTC_MIRROR_UUID`) only that one; `mkfs.ext4`
  only on an array built in that run, never on one whose filesystem can't be read (a read error on a
  degraded mirror hides it from blkid and wipefs); a disk is "blank" only if its first and last MiB
  read back; `OTC_MIRROR_ERASE` only by `/dev/disk/by-id/` name; a former member is re-added only if
  its superblock's Array State doesn't show it ran without a disk the mirror runs on now (split
  copies are left for the owner); a faulty member doesn't make the array foreign; `mdadm --grow`
  runs under `unshare --mount` on a private read-write sysfs (Docker's `/sys` is read-only); a disk
  may be `/dev/null` in `.env` to run on the other alone (never to build). The entrypoint's
  `config/storage-mode` (`folder` or `mirror <uuid>`) refuses a start on the other storage.
- `cfg` — INI config loader (`etc/otc_<env>.ini`, falling back to `/etc/otc_<env>.ini`); `env` is
  `os.Args[1]` (defaults to `"dev"`). All other packages pull settings via `cfg.GetStr/GetInt/...`.
- `dao` — the only package that talks to MySQL/MariaDB directly (schema in `db/db.sql`: `files`,
  `file_tags`, `social_publications` + likes/comments, `social_friendship`, `settings`, `profile`,
  `shared_links`, `vault`, `events`, `people`, `faces`, `image_groups` + `image_group_files`,
  `upload_only_folders` + `file_versions`, `out_of_images_folders` + `skipped_analysis`, `notifications`). Business logic in other packages should go
  through `dao`, not raw SQL. Issue #64: a device-side error the owner should know about (a
  photo that could not be processed, an upload that never reached the disk) goes into
  `notifications` as type `Error` through `dao.AddErrorNotification(alerttext.Alert)` (the alert
  writers are in `dao/notifications.go`) - grouped, so an error
  within five minutes of an open Error row joins it (`details` gains a line, `occurrences`
  goes up, the row is unread again) rather than adding a row - never a standalone row
  (`actor_domain` `(standalone)`: `UpsertErrorNotification`'s memory and set-aside Alerts);
  `files_manager.alert(what, path, err)` is the one call site helper (`what` an
  `i18n.DevAlertFile*` constructor, the error's own words the raw detail). Every alert is an
  `alerttext.Alert` (`alerttext.New`/`WithRaw`, never a literal; a new one needs a `dev.alert.*`
  key with `"stored": true` and a row in `everyAlertKey()`): `title`/`details` get the English as
  before, `msg_key`/`msg_args`/`msg_lines` the keys, which `ListNotifications` renders in the
  request's language (`dao.LocalizeNotifications`; docs/i18n.md "Alerts"). Not alerted, only
  logged (`files_manager/processing_alerts.go`): an image/*
  file in a format the device makes no preview of that fails to decode (a `.cur`, a `.djvu`: no
  thumbnail, no analysis; "previewed" is `previewedMimes` - the Go decoders, HEIC/HEIF, and JPEG
  2000/Photoshop through ffmpeg - by a `.HEIC` name, the row's MIME or the sniffed content, so a
  damaged JPEG or HEIC is still alerted, even one stored as application/octet-stream), and any
  processing failure (decode, HEIC conversion, video frames, thumbnail encode, a recovered panic:
  `processingAlert`, `safelyOn`) of content kept out of Images (#192's rule, `keptOutOfImages`),
  logged by hash at Info and by path at Debug only (#156); disk read/write failures are alerted
  wherever the file is. HEIC's HEVC stills and grids are decoded by `decodeHeic` (heif_decode.go),
  not goheif's own decoder, which read 10-bit samples (phones' HDR photos) as 8-bit and panicked
  placing a 10-bit grid's tiles (`TestDecodeHeicGridAtTenBits`); AV1 still goes to goheif. A camera
  raw (a DNG - Apple ProRAW, whose IFD0 x/image/tiff refused as "color model" - or a TIFF with a
  CFA/linear-raw directory) is its largest embedded 8-bit JPEG (`rawPreview`, raw_preview.go), first;
  any other TIFF Go can't read is tried for one before ffmpeg. A preview
  that failed leaves `<hash>.nothumb` holding `cDecoders`: `BackfillMissingThumbnails` skips it
  until a release adds to `cDecoders` (heic10 did), then retries it once. The clients show one line per row and the full list on hover (web)
  or tap (iOS/Android). Never push-notify these - the one exception is `raidwatch`'s disk failure below. `AddErrorNotification` is serialised in-process,
  and a group's `details` stop growing at 60000 bytes (later errors still count). The device DSN
  has `interpolateParams=true` (the provisioning DSNs in `dao/provisioning.go` don't).
  Release 143 dropped `files`' indexes on `modified` and `size` (no query filters or sorts by
  either); no `(created, path)` index can exist - `path` alone is InnoDB's 3072-byte key limit
  (ERROR 1071 on 11.8), and so the `created` index isn't extended by it either. MariaDB's buffer
  pool goes by the memory: 256 MB with 4 GB, 768 MB with 8 GB, 1 GB from 12 GB, the default under 3
  GB - `/etc/mysql/mariadb.conf.d/70-otc-memory.cnf` from install.sh and 143.sh (from MariaDB's
  next start: update.sh keeps otc and its users' instances running through the migrations, so it
  restarts nothing, and 11.8 can't grow the pool past its starting size), and
  `--innodb-buffer-pool-size` from the container's entrypoint (`buffer_pool_mb`, the memory as
  `container.MemorySizing` reads it).
- `files_manager` — file storage, hashing, dedup on disk. Content is keyed by hash, and the
  hash-first upload (`HasFile`/`HasCloudIds` then `LinkFile`) only skips the bytes when the blob is
  really on the disk (`hasBlob`: present and non-empty; each answer is recorded in
  `missingBlobs`, which is what listings use instead of a stat per file - issue #173; folder
  listings find rows with `dao.underPrefix`, an index range plus an escaped LIKE, never a
  REGEXP) - a database row alone once made a device
  claim content whose blob was gone, so no client ever re-sent it and every `GetFile` came back
  empty; `GetFile` now returns the read/decrypt error (and raises a #64 alert) instead of a File
  with no content. The sync clients verify a download's hash before writing it, for the same
  reason. Issue #141: a blob is only ever removed or relied on under its hash's
  lock (`lockBlob`, 256 stripes): `removeBlobIfUnused` checks-and-removes, `withBlob` checks-and-
  stores a row, and UploadFile's background write holds it too - never across a `DelFile`, which
  takes its own. Blobs are written to a temporary file and renamed into place (`writeBlob`):
no 0-byte blob after a crash, and a blob owned by another account (a recovered older
installation's `pi`) can still be replaced; release 22's script hands such files to `otc`. The
folder is synced after each rename too (`blobstore.SyncDir`: `Commit`, `writeBlob`, social's
`writeFileAtomic`, the posts' copies), so a power cut can't take a name back while its row stays.
**Disk space** (`freespace.go`): a write that would leave less than a reserve free - 1 GiB, or 2%
of a disk under 50 GB, at least 64 MiB: room for MariaDB on the same disk, the thumbnails and the
alert itself - is refused before it starts: `BeginUpload` (its size plus what the uploads under way
still have to write), `UploadFile`, `GetSharedLink`'s archive, `StartSharedGallery` and a post's
video copy, with `RespEnvelope.error_code` "storage_full" (also for a write that hit ENOSPC) and
`dev.files.storage_full`'s "this needs …, and only … is left". The sync clients don't yet pause on
it. What is left is statfs' f_bavail in f_frsize units (package `diskspace`: f_bfree counts ext4's
5% for root, which neither otc nor MariaDB can use - a 4 TB mirror showed ~200 GB free while
uploads failed); Status' raid/disk size is used plus available, so the apps' free (size - usage)
is what can be written, and Settings > Users' share is of that size. `storagewatch` (main instance,
`machine_alerts.go`) raises an Alert at 90% full and at 98% (`dev.alert.storage.*`), the latter
pushed too (`push.storage.full_*`, no figures), each once until the disk goes 5 points under it
(`<storage>/.storage-alerted`).
At every start `sweepOrphanedStorage` removes what interrupted work left at the top
level (`.blob-*`/`.upload-*`, `.post-*`, archives and `shared/<uuid>` galleries with no
`shared_links` row - none if that query fails): in the background once the listeners are up
(`AfterListening`, from bin/otc.go), reading the flat folder 4096 entries at a time (unsorted),
and only what was last modified a minute or more before this process started (`Manager.started`),
so nothing this run writes - a paused chunked upload, an archive being built - is ever taken.
`integrity.go` checks once a day (a minute after start, then daily) for rows whose content is
  missing - the distinct hashes a page of 5000 at a time from `files` and from `file_versions`
  (`dao.HashesPage`), a `hasBlob` stat each, and the paths of the missing ones only
  (`ContentPathsOf`); every (hash, path) pair used to be loaded first - and raises one Alerts entry
  per change (`.integrity-reported` in the storage path holds the last reported set). The missing blobs found on Cala came with its RAID recovery on
  2026-09-23: they were lost on the previous installation, most likely by the re-upload bug fixed
  on 2026-09-03 (34a8c7d), which deleted the old blob while the row kept its hash. Issue #132's upload-only folders live
  here: `upload_only_folders` (paths with their trailing slash, checked by prefix) refuse
  `DelPath` for anything under them with `ErrUploadOnly` (`RespEnvelope.error_code =
  "upload_only"`, which the sync clients take as "kept on the device": not retried, and a two-way
  folder records it so the file isn't downloaded back - see the desktop section), and a second
  `UploadFile`/`LinkFile` to an existing path there moves the old row into `file_versions`
  (`dao.ReplaceFileKeepingVersion`) instead of failing or overwriting. Blobs are shared by hash
  between `files` and `file_versions`, so `HashReferenced` is the check before one is removed;
  a path's versions go with it when it is finally deleted. Outside those folders an override
  (`registerUpload`'s forceOverride, `LinkFile`) replaces the row in place (`dao.OverrideFile`) and
  keeps any versions; only `DelFile` deletes them. `ListFiles` annotates every entry
  with `upload_only` and `versions`, `ListFileVersions` lists them, and `GetFile.hash` serves
  one. The web, iOS and Android explorers show the lock on folders (a toggle, `SetUploadOnly`)
  and the versions badge that opens the pop-up. Unlocking a folder that a flagged folder above it
  still covers is refused and changes nothing (issue #186: it used to answer ok with the lock
  still on): `LockedByParentError`, error code `locked_by_parent`, a message naming the nearest
  flagged folder above ("unlock /Photos to unlock it", or "first, then Trip" when the folder has
  its own flag too); every explorer shows an error reply's message.
  Issue #192's folders kept out of Images (`out_of_images_folders`, the same path rules;
  `files_manager/out_of_images.go`): content is kept out when a row of it (`files` or
  `file_versions`) is under a flagged folder and none is outside them all. It still gets its
  thumbnail (Files shows it; a processing failure making it is logged, not alerted - a disk
  read/write failure still is, #64) but never tags (place tags included) or faces: `guardAnalysis` takes
  the analysis out at processMedia's two entry points, before anything is read, and records the
  hash in `skipped_analysis`. Flagging (`SetOutOfImages`, synchronous) deletes the tags and faces of
  what only that folder holds (`DelFacesByHashes`: unnamed people left with no face go, named ones
  stay) and takes its rows out of every search token (`keepOut`; a page in flight is filtered when
  it stores); showing it again moves the skipped content to `pending_analysis` in one transaction
  (`TakeSkippedAnalysis`), then queues it (faces only if face recognition is on now - an explicit owner
  action, not #178's retroactive sweep), and is refused under a flagged folder
  (`OutOfImagesByParentError`, error code `out_of_images_by_parent`). `reconcileAnalysis` applies
  the rule again after anything that changes which paths hold a hash (an upload or link of known
  content, DelFile/DelPath, overrides, an analysis that ran while a flag was set);
  `ReconcileOutOfImages` repairs an interrupted one at the first sign-in; DelPath of a folder clears
  its flags (not a file's of the same name), DelFile never does (a sync app empties a folder file by file). Images - `SearchMedia`,
  the date buckets, collections' count and cover (membership is kept), a collection's gallery; a
  folder's gallery leaves out flagged folders below it, not the shared folder itself - takes the
  folders as `excluded`, from memory (`OutOfImagesFolders`: only this process writes the table), and
  fails rather than show kept-out photos when they can't be read; tags and People need no change,
  the data is gone. `ListFiles`/`SearchFiles` mark `File.out_of_images` and set
  `ListOfFiles.out_of_images_supported` (+ `folder_out_of_images` for a listing). Renaming a flagged
  folder from a computer makes a new, unflagged path (known limitation). Known gap: content whose
  only `files` row is kept out but which a kept version outside holds keeps its tags and faces
  (#132), which the tag list and People show while Images never lists it. Release 108's script
  creates both tables; a database without them reads as nothing kept out.
  **Storage format and chunked transfers** (security advisory on memory exhaustion, releases 40-42):
  every blob and thumbnail is encrypted in 1 MiB segments (`segcrypt`: header `OTS1` + a 7-byte
  nonce prefix, each segment AES-GCM with nonce prefix|index|last-flag and the header as AAD -
  no reordering, swapping or truncation), read and written through `blobstore` (`Open` gives a
  `ReaderAt` that decrypts only the segments a read covers; `Create`/`CommitAs` seal as data
  arrives). There is no other format: devices from before release 40 were reinstalled rather
  than converted (release 42 removed the whole-seal reader). Files move in
  chunks of at most 4 MiB: `ReadFile` (original bytes by range - `GetFile` remains only to show
  a photo, converting HEIC), `BeginUpload`/`UploadChunk`/`FinishUpload` (encrypted as it arrives,
  SHA-256 checked at the end, then `registerUpload` - the same bookkeeping as `UploadFile`),
  `DownloadSharedLink` with offset/length. Stored videos reach ffmpeg/ffprobe over the device's
  own loopback stream (`SetVideoSource`: a short-lived media token), so processing, reprocess
  and the info panel never load a video whole nor write it out in plaintext; share-link zips
  are streamed into a segmented file under the link's key. Every zip entry is Deflate; types that
  are already compressed (`zipLevelFor`: video, most audio, JPEG/HEIC/PNG/WebP..., archives) go in at
  `flate.NoCompression`, never `zip.Store` - Go always writes data descriptors, and streaming
  unzippers (Java's ZipInputStream, funzip) reject stored entries that have one.
  `OpenSharedLinkRange` (pre-auth) serves only `kind='archive'` rows with canonical lowercase uuids.
  **Thumbnails** (release 111, `files_manager/thumbnail_size.go`): every photo and video has two,
  sealed next to its blob and made from the picture as shown (EXIF orientation applied), never
  scaled up. `<hash>_thumbnail`, the big one: its LONGEST side at most `[otc]
  max-thumbnail-width-px` (`ThumbnailMaxSide()`, default 1000; the key keeps its old name but
  bounds the height too since 111 - a portrait was 1000x1333, a long screenshot 1000 px wide and
  as tall as it was), JPEG q80: Social, shared galleries, video posters, a viewer's thumbnail that
  stays on screen (below) and every answer that doesn't ask for small ones. `<hash>_thumbnail_small`, the grids' tile: shorter
  side 400 px, longer at most 800 (a panorama stays small), q75, the picture's own aspect (clients
  crop it to a square; the device never crops). Measured on 32 photos: big ~104 KB (was ~130 KB
  width-capped, max 415 KB), small ~38 KB (max 70 KB) - a first page of 12 is ~0.46 MB instead of
  ~1.5 MB. Processing writes both from one decode (`writeThumbnails`; the small one from the big
  one's pixels), small first and big last, both under the hash's lock: the big one is what says "processed"
  (`hasThumbnail`, ImageSearch's #147 rule), so `readGridThumbnail` shows a small one only when
  the big one is there. Both go together under the hash's lock (`removeThumbnails`, from
  `removeBlobIfUnused` and `dropIfOrphaned`); kept-out content (#192) gets both. Clients ask for
  the small ones with `small_thumbnails` on `SearchPhotos` (9), `GetThumbnails` (2),
  `ListImageGroups` (1) and `CreateImageGroup` (3); devices before 111 ignore it and send big ones,
  and clients that don't set it keep getting big ones. Every grid asks for small ones (web: Images,
  Files, Collections, the composer, the profile photo picker). A viewer shows the grid's small tile
  only until the full size arrives; where a thumbnail stays on screen (the full fetch failed, a
  photo the browser can't decode, a video it can't play) it asks `GetThumbnails` without the flag
  for the big one, retried like a grid page while it stays on the item (web `MediaViewer`, with
  `usePageRetry`; the apps' viewers follow the same rule). Content without a small one yet is answered
  with its big one AS STORED - made before 111, that one is capped by width only (1000x1333, or 1000
  px wide and any height) until the queue or the pass rescales it, so clients must assume no bound
  on it - and queued (`thumbnail_fix.go`: one worker, at most 1024 hashes, deduplicated) to get one
  from its big thumbnail - making it inline would cost ~60 ms a photo on a Pi 5 before the first
  tile (14 ms on an M-series Mac), as much as the bytes it saves on most links. The queue's worker
  gives way like the pass (below): it waits for quiet before each file, rests after each fix, and
  is emptied by a full reprocess. No size tells the two apart (an 800x600 photo's big one is
  800x600, a panorama's 1000x250), so since release 113 every entry of SearchPhotos and
  GetThumbnails answers says which came: `File.thumbnail_small` (field 13, `optional bool`: 113+
  always sets it, true or false; older devices leave it unset, and clients keep those by what they
  asked for). On an entry without content (`omit_thumbnails`) it only says a small one is stored
  (a stat, not a read): a phone that cached a big one under its small key asks `GetThumbnails` for
  it once when it turns true and keeps what that answer's entry says - the marker on an entry with
  content is the one that counts (a small one stored but unreadable comes as the big one, and is
  not asked for again). The
  one-time pass (`thumbnail_pass.go`, `FitStoredThumbnails`, after `BackfillMissingThumbnails` at
  the first sign-in) does the rest: for every media hash it reads both headers and, only where
  needed, makes the small one and scales a big one over the cap down - always from the big
  thumbnail, never the original (`fixThumbnails`, which decides and writes under the hash's lock,
  skipping content deleted or processed again meanwhile - and, before decoding, content whose blob
  is gone (#141), which would otherwise be decoded for nothing at every grid's request - and holds the content budget for what it
  allocates, `thumbFixReserve`: the decode (3 B/px), x/image/draw's kernel scaler buffer (32 B x
  dstW x srcH per scale - most of it: an old 1000x1333 portrait allocates ~51 MB) and the copies,
  checked against the real allocations by a test; a 1000x30000 thumbnail holds ~124 MB and waits
  for downloads instead of adding to them; over 500 MP, `cMaxImagePixels`, it is skipped). It waits while uploads arrive (`noteUploadActivity`, a
  minute) or the lanes are busy, rests as long as each fix took (at least 50 ms), stops for a full
  reprocess, and records `<cap> <small side> <last hash>|done` in `<storage>/.thumbnails-sized` -
  resumable, run again when the small size changes or the cap goes down. Posts' copies in
  `unenc-storage-path` and shared galleries keep what they were made with.
  **Files grid** (release 80): the Files section on the web, iOS and Android switches between the
  list and a grid (remembered per browser/app). The grid shows each photo or video by its
  thumbnail and everything else by a generic labelled document icon whose colour says the type
  (PDF red, DOC blue, XLS green, PPT orange...; the mapping is the same in `FileTypeIcon.tsx`,
  `FileTypeIcon.swift` and `FileTypeIcon.kt` - change all three together; no vendor logos).
  `GetThumbnails{paths}` (their rows in one `in` query, `dao.GetFilesByPaths`; a path at a time
  should it fail) answers up to 48 paths (about 8 MB) per request with the stored
  thumbnails, in the order asked, leaving out paths without one; since 113
  `ListOfFiles.ask_again_from` (5) is the index of the first path it didn't look at (the byte cap,
  or past 48), 0 when it looked at all - before, a cut answer looked like paths without
  thumbnails. It stays by path, for the phones' cache misses too: every grid has each photo's
  path, each entry carries its path's current hash to key a cache by, and a lookup by hash would
  still need a query to know the hash is in use.
  **Storage view** (issue #195): `GetFolderUsage{path}` (139) answers `FolderUsage` (69), asked
  only while Files' Storage view is open - ListFiles never sizes folders. Same callers as
  ListFiles (the owner, a user on their own instance, never a friend); older devices answer
  `unknown_payload`. `files_manager.FolderUsage` takes the folder as ListFiles does (the trailing
  slash added, `""` is the root) and answers every direct child holding a file or an older
  version: its path as ListFiles lists it, `is_dir` (a file and a folder of one name are two
  entries), `files_size`/`file_count` of every file under it at any depth, and `versions_size`
  (`file_versions`, not part of `files_size`); largest first (files + versions), then by path
  byte by byte, a folder before a file of the same name; plus the folder's sums. Sizes are
  logical (`size64`): content at two paths counts at both; thumbnails, encryption, posts,
  galleries, other users and the database aren't counted. `dao.FolderUsage` is one statement
  that reads each table once by `underPrefix`'s range; the database only cuts each path down to
  its child and Go sums (a GROUP BY was 4-8x slower: MariaDB copies the 768-character key into
  its temporary table for every row). `BenchmarkFolderUsageMySQL` (100k files and 10k versions,
  Lima VM): 22 ms at the root, where listing the root takes 114 ms, and 22 ms for a 45k-file
  folder. The answer has no upload-only or kept-out flags: the apps join it with their ListFiles
  listing by (path, is_dir).
  **Logs** (Settings > Logs, web/iOS/Android, main instance only): `GetLogs{source "app"|"update",
  offset, max_bytes, wait_seconds}` reads the device's log (`[logger] log_file`) or the update log
  (`/var/log/otc-update/update.log`) in whole lines; with `wait_seconds` and nothing new the device
  holds the request until the log grows (25 s at most), so the clients' loop of requests is a live
  stream that the bridge relays unchanged. `SendLogs{note}` gzips the end of both logs and sends
  them with `BridgeSendLogs` (device secret) to the bridge, which mails them to info@off-the.cloud
  with the owner's account email as Reply-To (3 an hour per device). The screens warn that logs can
  include file names, search words, Wi-Fi names and addresses.
  **Processing lanes** (release 73, `files_manager/lanes.go`): an upload is answered once its
  bytes and row are stored, then `enqueueMedia` (media only) records its hash in
  `pending_analysis` and queues it in the *fast lane* (NumCPU-1 workers, at most one per 2 GB - `processingWorkersFor`; the
  slow lane at most 3; ffmpeg decodes frames with a Pi's 4 threads: a 32-thread PC ran 31 a lane,
  more than 32 GB hold, until release 133; one job at a time across
  both lanes in low-memory mode and for an hour after a death - see Low-memory profile): EXIF, decode,
  orientation, thumbnail - one frame for a video. It then moves to the *slow lane* (tags,
  faces; four frames for a video), whose workers only take a job while the fast lane has none
  queued or running, so during a big sync every thumbnail comes first. `processMedia(...,
  stages)` is the one pipeline (`processMediaContent` = both stages from one decode, for
  backfill and Reprocess, and clears the pending row). The queue itself is memory, but
  `pending_analysis` holds only hashes, so after a restart - when nothing can be decrypted
  until the owner's key is back - `ResumePendingAnalysis` refills the lanes at the first
  sign-in (`startBackfillOnce`, before `BackfillMissingThumbnails`, which skips those hashes).
  `UploadFile`/`FinishUpload` don't queue content the device already knows and has a thumbnail for,
  and `processFaces` skips content that already has faces.
  Issue #180 (release 69): **shared galleries** - an image group (`group_id`), a folder
  (`directory`, recursive) or files (`paths`) are copied by a background job
  (`files_manager/shared_gallery.go`: `CreateSharedGallery` then `GetSharedGalleryJob` polling)
  into `<storage>/shared/<uuid>/` - `manifest`, `<i>.orig`, `<i>.thumb`, and `<i>.prev` (a JPEG
  for what browsers can't show: HEIC, RAW, TIFF) - all sealed under `getCipher(secret)`. The
  link is `https://<domain>/shared#<uuid>.<secret>`: the secret only ever lives in the link's
  fragment (never sent to a server, never stored); `shared_links` keeps the uuid, `kind`
  (archive/gallery), `description` (under the owner's key), `files`, `opens`, `last_opened`,
  `expires` (1/7/30 days, default the old TTL). The web route `/shared` renders
  `SharedGalleryView` before anything else of the app, for anyone: thumbnails, a viewer (photos
  by their preview, videos streamed through a `/media/<token>` minted for the gallery file
  under the link's key), download all or a selection. Visitors' requests
  (`OpenSharedGallery`, `GetSharedGalleryItem`, `GetSharedGalleryStream`) are public and answer
  every failure identically. Owners: share from an image group or a folder (web, iOS, Android:
  `SharedGalleryShareFlow`), and Settings > Shared Links (`SharedLinksView` /
  `SharedLinksPanel`) lists every link with its opens and size, and deletes a link with its copy.
  Decrypted manifests are cached (8, 5 min idle, valid only for the same secret and manifest
  size/mtime), but the expiry is read from the database on every request.
  An archive's link is `/#download=<uuid>_<secret>` (2026-10: the secret in the fragment, as a
  gallery's; `downloadLink.ts` still takes `?download=`).
  Issue #166 (release 63): a share link is downloaded in parts (the web page asks for 4 MiB
  ranges; a whole-archive `DownloadSharedLink` is refused above 4 MiB), and every reply carrying
  file content holds the content budget until it is on the wire (`connHandler.reserveMemory`:
  `GetFile`, `GetPublicationMedia`, share-link parts; `GetFileInfo` reserves while it reads a
  photo). One reply never holds a file too large for that (`membudget.go`; acquire lets a request
  over the whole budget run alone, and a 1.5 GB TIFF in a viewer, or a friend syncing a multi-GB
  post video, killed the device): `GetPublicationMedia` refuses a post's file over
  `MaxPublicationMedia` (256 MB, or a third of the budget: 85 MB on a 4 GB Pi) and `GetFile` one over
  `shownWholeLimit` (a third of the budget, at least 64 MB) - never a HEIC (sent as its JPEG, which the
  decode budget bounds) nor, for now, a video (the web composer's trimmer, `NewPostPicker`, fetches
  a library video whole and has no other way to read it; every viewer streams videos) - with
  `RespEnvelope.error_code` "too_large" (`websocket/content_codes.go`), reserving nothing. The
  viewers keep the thumbnail, downloads read in parts, a friend's device keeps the post's thumbnail
  (its sync logs the refusal; fetching it through GetMediaURL is still to do). `GetFileInfo` reads a
  photo's EXIF through the blob's ReaderAt (`exifFrom`, by its first bytes: a JPEG up to its EXIF
  segment, a HEIC its boxes; a TIFF, which goexif reads whole, up to `shownWholeLimit`), and
  `exifinfo.FromVideoSource`'s ffprobe has a deadline (`ProbeTimeout`, a minute). A post's video is
  never loaded: `ExportVideoForPost` re-encodes it from the loopback
  stream straight into the posts' directory (named by hash) or decrypts the original there a
  segment at a time, one transcode at a time (`transcodeSlots`, taken before the stream token).
  The re-encode gets six seconds a second of output, half an hour at least (`transcodeTimeoutFor`,
  the length from ffprobe), and once more after a timeout with `transcodePlan.fast` (ultrafast,
  `-skip_loop_filter all`, no HDR tone mapping); if that fails too, the original is published in its
  place only when no cut was asked for and it is within `MaxPublicationMedia` - otherwise the post
  fails, saying why (`dev.files.video_not_cut`, `video_not_reduced`): a cut is never undone. Both
  post copies are synced before their rename, the folder after it. A post's photo that needs no
  converting is copied a segment at a time (`CopyForPost`), a HEIC read whole within the budget.
  `social.NewPublication` holds the hashes it writes (`social/post_files.go`, `postsInFlight`) and
  takes its plaintext copies back when the post isn't made (`postFiles.discard`: not a file another
  post names or another post being made holds); once per start, before the first friend sync
  (websocket's `backgroundWorker`), `SweepUnusedPostFiles` removes `<hash>` and `<hash>_thumbnail`
  files older than a day that no `social_publications_files` row names (the only table naming them;
  the alerts' thumbnails are their posts') - nothing else in the folder. `budgetSize` uses the row's int64 size and
  looks at the blob only until the size backfill below has run. Since release 93 (`93.sh`, and
  `install.sh` `apply_schema_migrations`) the size columns of `files`, `file_versions` and
  `social_publications_files` are BIGINT; `files_manager/size_backfill.go` corrects, once and in
  the background at start until `settings.sizes_backfilled` is set, the rows stored wrapped (or,
  for friends' posts in release 92, clamped) before it: only blobs over MaxInt32, content size from
  the size on disk via `segcrypt.PlainSize` (no key needed), own and friends' post files from their
  copies in `unenc-storage-path`; it waits for the BIGINT columns, and a pass with an error is
  retried at the next start. `checkImageSize` also bounds a HEIC
  grid by its decoded first tile (`checkHeifGrid`). **Large photos** (2026-10-24, `limits.go`,
  `decode_cost.go`; until then a fixed 120 MP limit refused a phone's 200 MP photos): what an image
  may be goes by the device's memory. Every still decode (`decodeImage`, `heicToJpeg`) holds what it
  costs from its own budget, a fifth of MemTotal (1.6 GB on 8 GB, ~760 MB on 4 GB, 1 GB unknown;
  `holdDecode`, waiting for room - separate from the content budget, which downloads and gallery
  previews hold first), and an image whose decode alone is over the whole budget is refused, the
  message saying the device's limit for that kind ("over N megapixels"); 500 MP is refused
  everywhere (`cMaxImagePixels`). The cost is read from the header (`decodeBytesPerPixel`, matched
  to Go's real allocations in `TestDecodeReservationCoversWhatItAllocates`): a byte a sample for a
  JPEG (`jpegFrame`: 1.5 a pixel at 4:2:0) plus 4 a sample if progressive, a PNG's pixel size (x2
  interlaced), 8 for a HEIC. A photo over 50 MP (`cWorkPixels`) is box-shrunk by a whole factor
  as it is decoded (`shrinkForWork`, +1 byte a pixel) and the full decode freed: thumbnails, tags,
  faces and gallery previews work on at most 50 MP, so what follows costs what a 50 MP photo
  always did. A 200 MP phone JPEG: ~490 MB held, 1.9 s on an M-series Mac; limits per kind in
  `decodeBudgetBytes`' table (4 GB: 311 MP of 4:2:0 JPEG, 91 MP progressive, 86 MP HEIC). The
  backfill's `cDecoders` gained `by-memory`, so photos refused before are tried once more. Decode stills through `decodeStill`
  (upload processing and the shared-gallery preview): ffmpeg only for what Go can't read, never
  for anything `checkImageSize` refuses (issue #188) - ffmpeg would decode it anyway, unbounded -
  nor for content no ffmpeg reads (`neverPreviewedMimes`: DjVu, CAD drawings, GIMP files), which
  `isMedia` also leaves out, so it is never read, queued (`enqueueMedia`, `enqueueAnalysis` and
  `ResumePendingAnalysis` drop it; dao's `cMediaRows` doesn't exclude it, so #192 may record it as
  skipped) or given a thumbnail; other image/* types
  (`.ico`, `.svg`, AVIF...) are left to whatever the device's ffmpeg build decodes.
- `raidwatch` — a disk of the mirror (md0) that stops working: an Alert of its own
  (`dao.AddStorageNotification`, an ungrouped Error row) and a push naming the USB port it is in ("the
  disk in the top blue USB port"), once per failure (`<storage>/.raid-alerted`), and another once the
  mirror is whole again; the status line names it too (`raidwatch.Missing`). The alert and push go
  out through `Notify.AlertText` (an `alerttext.Alert`) and `Notify.PushText` (a `push.Text`);
  `Notify.Alert`/`Push` get the English when those are nil. Read from sysfs, no root:
  where each disk is plugged in is recorded once every disk is in the mirror, during a new mirror's
  first copy too (md counts both in sync then; `<storage>/.raid-members.json`), since a failed reader
  usually vanishes. With nothing recorded, the disk still working is named instead when it is in a
  blue port (`raidwatch.StillWorking`; `dev.alert.raid.stopped_detail_working_*`,
  `dev.status.raid.degraded_working_*`, `push.raid.stopped_working_*`). Main instance only.
  The storage log (release 138): `raid_watch.py` writes what it prints, with times, to
  `/var/log/otc-storage/storage.log` (root-owned, 0644, 512 KB and a `.1`) - the disks the Pi sees
  with their USB port and speed, the mirror's state in one line, why a disk wasn't added, a
  rebuild's progress every 10 minutes, and the kernel's lines about USB, the disks and md
  (`journalctl -k`, every 30 s and on any change) - because the service can't read the journal and
  root must not write in its log directory; `raidwatch.FollowLog` copies each new line into the
  device's own log as `storage: …` (Settings > Logs > Device, every app; its place kept in
  `<storage>/.storage-log-read`, by the file's first line). `python3 scripts/raid_watch.py
  --selftest` tests the script's side. The Pi 5 has one USB controller per blue
  port (`bluePort`: xhci-hcd.0/1 -> top/bottom); which is on top is still to be confirmed on hardware -
  the startup log names each disk's port. The root `raid_watch.py` still drives the LEDs and re-adds a
  replacement - since release 143 only a blank one (no partition table, no `blkid -p`/`wipefs`
  signature, first and last MiB all 0x00 or 0xFF, read with O_EXCL|O_DIRECT) or a former member of
  md0 whose superblock doesn't show it ran without the disk md0 runs on now (`judge_disk`, the same
  rule as `otc-mirror`; a verdict is kept while the disk stays plugged in, disks are looked at every
  5 s). Any other disk (a backup drive, a card fresh from the shop, another array's disk, a split
  copy) is left alone, logged once, and listed in root's `/run/otc-raid-watch/not-added.json`, from
  which `raidwatch` raises one Alert per plug-in (`dev.alert.raid.not_added_*`,
  `<storage>/.raid-not-added`); the stopped alerts' details say the replacement must be blank
  (`dev.alert.raid.replace_detail*`; the old `stopped_detail*` keys stay for old rows). First-sign-in
  storage setup (`OTC_SKIP_RAID` hand installs) refuses storage that already holds files, a disk with a
  signature, partitions or an md superblock, and storage fstab already names; it reads its request with
  O_NOFOLLOW|O_NONBLOCK, gives the new filesystem to otc (0751, `unencrypted/` 0750) and writes fstab by
  `UUID=` with nofail and the drop-ins below (MariaDB stays on the SD card there). Tested on
  `scsi_debug` disks in the Lima VM (`per_host_store=1`, or every disk shares one store). More storage:
  swap the cards for bigger ones one at a time (each is added and
  rebuilt like a replacement); once every member has room to spare, `raid_watch.py` grows md0
  (`mdadm --grow --size=max`) and the ext4 on it (`resize2fs`, online), checked every 10 minutes, never
  while degraded or rebuilding (tested on loop devices in the Lima VM). Disks go in the two blue USB ports (README, website).
- `wifiwatch` — the Wi-Fi watchdog (release 115). Pit (Pi 5, brcmfmac over SDIO) after ~27 h up
  kept receiving at 6.6 MB/s but sent at 0.15 MB/s, 7.7% of TCP segments retransmitted
  (`CMD53 sg block write failed -84`); a reboot fixed it. Main instance only, only while the IPv4
  default route is a wireless interface. Once a minute: TCP RetransSegs/OutSegs from
  `/proc/net/snmp` with loopback's tx packets taken off OutSegs, judged only with >= 1000 segments
  sent in the minute (idle never counts); bad >= 4%, healthy < 1%, in between holds a streak; quiet
  minutes are tolerated up to 10 (`MaxGap`). Restart after 5 bad minutes in a row when the radio
  confirms (>= 10 tx errors+drops on the interface - brcmfmac counts a failed SDIO write there - or
  >= 5% of frames unacknowledged in `iw dev <if> station dump`), 10 without; never while an update
  runs. All overridable in `[otc]` (`wifi-watchdog = off`, `wifi-watchdog-*`, README). Once per
  episode (the owner's rule): phases armed -> restarting -> restarted -> armed (5 healthy minutes
  after a 2-minute settle) or gave_up (bad again: no second restart until 5 healthy minutes re-arm
  it). Persisted in `<working dir>/.wifi-watchdog.json` with any Alert not yet stored
  (`pending_alert`, an `alerttext.Alert`; an older `{title, detail}` loads as English only) and
  the speed before the restart (`slow_speed` `{rate, pct}` as numbers for the Alert's keys, next to
  the English `slow`). Alerts (`AddErrorNotification`) come after the restart, once judged: "The
  Wi-Fi was restarted: sending data had slowed down: ... It is working normally again." (or, after
  an hour without enough traffic to judge, "...There hasn't been enough traffic since..."), "The
  Wi-Fi is still slow after a restart" (Restart button / Ethernet), "could not be restarted" (root
  failed or silent for 10 min) - never at the request. The root side
  (`scripts/device-runner/otc-wifi-restart-runner.sh`, `otc-wifi-restart.path` on
  `/var/lib/otc/wifi-restart.request`, answer in `wifi-restart-status.json`: running/done/failed/
  rebooted) reads nothing from the request: interface from `/proc/net/route` (refused unless
  wireless), driver module from sysfs (holders first, e.g. brcmfmac_wcc), the SDIO controller
  (`.../mmc_host/...`'s platform device) unbound and bound again only when no block device sits
  under it, the module back, power save off, NetworkManager reconnect; not connected within 150 s
  -> it reboots (`rebooted`, mentioned in the Alert). At most one restart per 30 min
  (`/run/otc-wifi-restart.last`). Tested with fake readings (`wifiwatch_test.go`) and
  `mac80211_hwsim` in the Lima VM; the SDIO reset itself only on a Pi. A Settings › Wi-Fi change
  (release 140) never builds a streak: while `network.Joining()` is true - a change in flight and
  the 10 minutes after it ended - `step` resets the streak and returns (the `Watchdog.joining`
  seam, set to `network.Joining` in `Watch`). The restart runner takes `/run/otc-wifi.lock` (the
  Wi-Fi change's lock) and answers `busy` (`dev.status.wifi.busy`) when a change holds it, which
  wifiwatch takes as "try later": re-armed, no Alert, no gave_up.
- `network` — Settings › Wi-Fi (release 140): the owner changes the network the device uses, or its
  password - typically after changing the router's - from the web app (iOS and Android with their
  next store builds). A cable is the fallback once the device has lost its Wi-Fi (the FAQ and the
  apps' "can't reach the device" screens say so: plug it into the router, open Settings › Wi-Fi,
  unplug).
  - Protocol: `GetWifiStatus` (143) and `JoinWifi` (144) both answer `WifiStatus` (71: `managed`,
    the current `ssid` and `signal`, `wired`, and `last_join` = `WifiJoin{id, ssid, state
    joining|connected|failed, code, message, restored}` while a change runs and 15 minutes after).
    `ListWifiNetworks` (53) feeds the picker: one row per name, current first, then by signal,
    `current` and `unsupported` (802.1X, WEP, OWE) set, names that aren't valid UTF-8 dropped (proto3
    would fail the whole list). `JoinWifi.password` is `bytes`, RSA-OAEP sealed under the
    connection's `GetPubKey` key like `CreateUser.password`. One rule everywhere (Go, runner, the
    three apps): SSID 1-32 bytes of UTF-8 with no rune below 0x20 or 0x7f; password empty (open
    network), 8-63 BYTES of UTF-8 without control characters, or exactly 64 hex digits - the apps
    count bytes. `SetWifi` (54, the old first-run step's plaintext password) is retired: answered
    `unknown_payload` without logging the request (the `default:` branch would log the password);
    the web's first-run Wi-Fi step is gone and `RequestJoin`'s plaintext file on the SD card with
    it (140.sh deletes `/var/lib/otc/wifi_join_request.json`).
  - Who: the owner of the primary instance. A child instance gets `managed=false` / `notAvailableHere`,
    a container `managed=false` / `notInContainer`, the demo `managed=true, wired=true` / `demo`.
    `Managed()` (cached 30 s) needs: not a container, `RUNTIME_DIRECTORY=/run/otc` writable,
    NetworkManager running and not netplan-backed (no `/etc/netplan/*.yaml` with `renderer:
    NetworkManager` - one otc can't read counts - and no `90-NM-*.yaml`: its saved profiles land in
    netplan), a wifi device not named `uap*`/`p2p*`, and `otc-wifi-join.path` active; it logs the
    reason when off. Raspberry Pi OS (Trixie) ships NetworkManager 1.52.1 built with netplan, but its
    own patch (`network-manager 1.52.1-1+rpt4`, `0012-fix-netplan.patch`) writes to netplan only
    profiles whose file name starts with `netplan-`, so the runner's profiles stay keyfiles; the
    image purges cloud-init (and `rpi-cloud-init-mods`' netplan file with it), so it has no netplan
    file and is managed - a hand-installed Pi OS set up by cloud-init isn't. The service logs
    `nmcli --version` at Info once at start.
    Devices before 140 answer `unknown_payload`: the apps hide the section until the next sign-in.
  - Go side (`Join`): refusals before anything changes, each with an `error_code` - `bad_name`,
    `bad_password` (after decrypting), `not_managed`, `update_running` (`updater.Pending`: its
    request still waiting or a run going on), `busy` (one change in flight, or a request file still
    there), `too_many` (6 accepted in 15 minutes) - the update check, the in-flight check and the
    handover all under one mutex, which `ApplyUpdate` holds too while it checks `InFlight` and writes
    its request (`network.UnlessChanging`), so neither slips past the other. The request `{"v":1,"id","ssid","psk","hidden"}` goes to a temp file in
    `/run/otc` (tmpfs; the service is mlocked; the bytes zeroed after) and is `os.Link`ed to
    `/run/otc/wifi-join.request`, so a request is never overwritten and the PSK never touches the
    SD card (the buffer is sized for every byte escaped, so it never reallocates and leaves a copy).
    Logged at Info without the SSID (Debug only). `ApplyUpdate` refuses while a change is in flight
    (`wifi_changing`, `dev.update.wifi_changing`).
  - Root side: `scripts/device-runner/otc-wifi-join-runner.py` (Python stdlib, `#!/usr/bin/python3
    -I`, installed as `/usr/local/bin/otc-wifi-join-runner`), started by `otc-wifi-join.path`
    (`PathExists=/run/otc/wifi-join.request`) as `otc-wifi-join.service` (oneshot, root,
    `TimeoutStartSec=7min` - a slow change is about 5 - and `TimeoutStopSec=3min` for
    `ExecStopPost=... --stopped`; `StartLimitIntervalSec=0`, so a burst of requests never leaves the
    path unit failed until a reboot - the runner's own limit is the bound; `ProtectSystem=strict`,
    `ReadWritePaths=/run`, `AF_UNIX AF_NETLINK AF_INET AF_INET6`). It first removes what a run killed
    outright left in its directory (`req.*`, temp files), then renames the request into its own
    root-owned `/run/otc-wifi-join/` (0755), opens it `O_NOFOLLOW`, checks it is a regular file owned
    by `otc` with one link, reads at most 1025 bytes and refuses duplicate or unknown keys
    (`bad_request`, counted toward the limit too). Before touching the network: 10 runs in 15
    minutes of uptime -> `too_many`; an update asked for (`/var/lib/otc/update.request`, lstat
    only), `otc-update.service` active, or `/run/otc-update.lock` held (a non-blocking EXCLUSIVE flock
    released at once) -> `update_running`; `/run/otc-wifi.lock` (shared with the restart runner) not
    free within 10 s -> `busy`; no NetworkManager or wifi interface -> `no_wifi`; a scan
    (`--rescan yes`, `no` when refused; matched by SSID-HEX; a network not seen is looked for up to
    3 more times, 5 s apart, within 40 s - NetworkManager answers a rescan asked too soon with its
    old list, silently). The security comes from every access point with that name that the runner
    can join (WEP, 802.1X and OWE ones don't count): any secured one makes the network secured - an
    open copy of its name (anyone can broadcast one, louder) is never joined and the password never
    dropped - `wpa-psk` when one shows WPA1/WPA2, else `sae`; no password then ->
    `password_required`; a password for a name only open access points have -> `open_network`;
    none it can join -> `unsupported_security`. The previous connection is the one `activated` on
    the interface with the device connected (one still trying - an old password - is none). The
    trial profile is a keyfile written in `/run/NetworkManager/system-connections/` (created 0755 if
    missing, only where `/run/NetworkManager` exists; tmpfs: a wrong password never reaches the card,
    and the PSK is never on a command line) with autoconnect off, the SSID as decimal bytes,
    `cloned-mac-address=permanent`, `addr-gen-mode=stable-privacy` (a keyfile without it means EUI-64
    addresses from the MAC) and, for the same network, the IP settings and addr-gen-mode of the
    previous connection, or of an inactive `OTC-WiFi` for that SSID (the router's password changed
    and the device came back over a cable); `nmcli connection load`, `connection up --wait 60` on
    the interface (longer than NetworkManager's 45 s DHCP timeout), state 100 with an address, then
    the internet through THAT interface: `curl --interface IF` to the bridge host from `[otc]
    bridge-addr` in the root-owned config (any HTTP status), or without a bridge the default
    gateway answering a ping, each tried twice 3 s apart -> else `no_internet`. Each step rewrites
    `status.json` ("joining", the uptime now). Success: `connection modify` without `--temporary`
    saves it to `/etc` (checked: its FILENAME is no longer under `/run/`), `os.sync()`, `state.json`
    rewritten as `{"committed": NEW}` (no previous or trial left in it), then the old `OTC-WiFi`
    profiles deleted, other profiles of the same SSID set to autoconnect off (not deleted),
    `os.sync()` again. Failure: stderr classified (`Secrets were required` -> `wrong_password`, as
    NetworkManager 1.52 reports a wrong WPA2 password - and, for a profile that never connected, a
    drop during the handshake too; "supplicant disconnected" / "took too long to authenticate" ->
    `auth_failed`, not seen with 1.52; "could not be found" for a network the runner's own scan just
    saw -> `auth_failed`, as 1.52 reports a wrong WPA3 password, a refusing or silent access point
    that way; `not_found`, `timed_out`, `no_address`, else `join_failed`), the trial deleted and the
    previous connection brought back up. A run that finds a `state.json` left by one cut short (its
    `--stopped` killed too, as `systemctl kill` does) puts that right first (`put_right`), under
    the Wi-Fi lock. `--stopped` takes the Wi-Fi lock (10 s at most, then goes on), turns
    a status still `joining` into `interrupted`, deletes a trial only while its FILENAME is still
    under `/run/`, brings the previous connection up only when the interface isn't connected, and
    does nothing while the system is stopping; a change committed, or whose profile is already
    saved (cut short between the two), it finishes instead: old profiles retired, flushed,
    `connected`. At start (once NetworkManager answers) it removes stray `otc-wifi-*.nmconnection`
    files from `/run/NetworkManager/system-connections`, deleting a profile only while it is still
    that unsaved trial (that file, the trial's name, not active) - a saved profile only loses the
    stray file. It logs byte counts and codes, never the SSID or the PSK, and exception types only.
    `python3 scripts/device-runner/otc-wifi-join-runner.py --selftest` runs without NetworkManager
    (on the Mac too).
  - Status and texts: `/run/otc-wifi-join/status.json` (0644, written by temp file + rename:
    `{"v":1,"id","state","code","ssid","left","restored","uptime"}`) has no English. Go reads it with
    a 4 KiB cap, checks the id, state, code and SSID, and merges it with its own pending change: no
    status for that id after 60 s -> `no_answer` (the request file removed), `joining` more than
    600 s of uptime after its last word -> `interrupted` (never while the runner or its `--stopped`
    can still be at work; uptime, not the clock: no RTC, and NTP jumps it).
    `devtext.WifiJoinText(lang, code, ssid, left, restored)` renders `dev.status.wifi_join.<code>`,
    or, for the 8 codes that can come after the device left its network (`wrong_password`,
    `auth_failed`, `not_found`, `timed_out`, `no_address`, `no_internet`, `join_failed`,
    `interrupted`), `dev.status.wifi_join_back.<code>` (`left && restored`: "...back on its previous
    network") or `wifi_join_lost.<code>` (`left`, not restored: "...connect it to your router with an
    Ethernet cable"), from the key itself in every language, English included; an unknown code is
    `join_failed`. `WifiJoin.restored` is `left && restored`; the apps never branch on it and show
    `message`. The runner's `CODES` are devtext's `wifiJoinCodes` minus `no_answer` (Go's own):
    `TestWifiJoinRunnerCodes` compares them.
  - Rate limits: Go one change in flight and 6 accepted in 15 minutes; root 10 runs in 15 minutes
    (it holds even if the otc service is compromised); `ListWifiNetworks` relies on
    NetworkManager's own rescan throttling.
  - Accepted risk: additional users' instances run as the same uid `otc`, so a compromised one can
    write `/run/otc/wifi-join.request` itself and root's owner check can't tell it from the
    primary - as with the reboot and update requests. Root's validation and rate limit are the
    bound.
  - The bridge pool follows a network change (`websocket`'s network watcher): when the default
    route's interface or the local address changes (checked every 5 s), idle pool connections are
    closed, the dial backoff is reset and waiting retries are woken (nil-safe: `wake` is nil after a
    registration), so unplugging the cable doesn't leave the device off the bridge for minutes.
  - Apps (web `components/settings/WifiPanel.tsx` + `wifiFlow.ts`, iOS `WifiSection.swift`,
    Android `WifiSection` in `ui/settings/SettingsView.kt` + `WifiViewModel` + `WifiFlow.kt`; the
    same rules, tested row for row with the same case ids - N names, P passwords, C what is sent, K
    the cable-only confirmation, L/R on load, J JoinWifi's answer, W a look while waiting, K- which
    failures keep the picker open, U which listed networks can be chosen, T timing - in
    `web/scripts/wifi-flow-test.mjs`, `WifiFlowTests.swift` and `WifiFlowTest.kt`): a section just
    before Tailscale, for the primary's owner only, shown only for `managed`. The picker shows a
    lock for secured networks, "Open" for open ones (allowed: the reachability check protects),
    "Connected" for the current one, ones the device can't take greyed out as "Not supported"
    (unsupported security, a name with control characters or over 32 bytes), and "Other Network…"
    for a hidden name (a typed name is never changed: control characters are left to the device's
    `bad_name`); the confirmation says the device leaves its network for about a minute (or, on a
    cable with no Wi-Fi, that it stays reachable through it). A failed scan says so
    (`scan_failed`). After `wrong_password`, `auth_failed`, `not_found`, `password_required` or
    `open_network` the picker stays (or opens again) on the network tried. The password is sealed and never kept (no saved state, no autofill: web
    `autocomplete="off"` and a field not named "password", iOS no password content type, Android
    `importantForAutofill=no`); the name field has no auto-capitalisation, auto-correction or smart
    quotes. After `JoinWifi` they ask `GetWifiStatus` every 3 s (10 s a look, the bridge's own
    answers, errors and a moment's `managed=false` ignored) until `last_join` with that id ends,
    show "slow" after 240 s (in place of the spinner) and keep asking every 10 s; a reload, and a
    refusal (`busy`: the same request sent again), resume a `joining` change. A failed
    `GetInstanceRole`/`GetWifiStatus` leaves the section as it was.
  - Tests: `network`, `websocket` (the gates, sealed password, SetWifi, the update lock), `wifiwatch`,
    `devtext`, the runner's `--selftest`, and NetworkManager itself only in a NEW throwaway Lima VM
    with `mac80211_hwsim` and hostapd - never the `otc` VM's networking or a real device:
    `scripts/dev/wifi-join-hwsim.sh` (its header has the VM's setup; the access point lives in its
    own network namespace, bridged with a "cable" veth for the FAQ's way back). The real join on a
    Pi happens after the release, through Update.
- `devicerestart` — Settings > Restart device (release 115): `RestartDevice` (137) ->
  `RestartingDevice{expected_seconds}` (68), owner of the main instance only (`sup == nil` refused;
  older devices answer `unknown_payload`). Writes `/var/lib/otc/reboot.request`;
  `otc-reboot.path` runs `otc-reboot-runner` (removes the request first, refuses in the first 2
  minutes after boot - the device too - waits 3 s for the answer to leave, `systemctl reboot`). In
  the Device version section of web `UpdatePanel`, iOS `UpdateSection`, Android `UpdateViewModel`:
  confirm, "Restarting…", then a `GetInstanceRole` every 5 s from 20 s on; back = the device's own
  answer (never the bridge's `device_unreachable` Ack) after a failed look or 90 s; "slow" after 5
  minutes.
- `images_tagger` — runs the RAM++ ONNX model (paths from `[tagger]` config) to auto-tag photos;
  requires CGO + libonnxruntime at runtime (see Build section). The model file is 873 MB (int8,
  `ram_plus_int8.onnx`); measured with ONNX Runtime 1.24.3 on arm64 (macOS, 2026-10-08) it is
  ~0.85-1.0 GB resident when freed memory goes back to the system (~2.3 GB with what the
  allocator keeps of the load), ~1.4 GB while loading, and a run needs ~650 MB more - which the
  default CPU arena keeps after it (three runs at once: +1.3 GB). Loading reads the IO names from a
  whole temporary session (`firstTensorIO`), cached per process (`cachedTensorIO`) for reloads.
  Outside low-memory mode the primary loads it in the background (`files_manager/tagger_load.go`);
  a load that fails no longer ends the process (`log.Fatal`, then a restart every 3 s, the device
  unreachable for the Update that would repair it): one Alert (`dev.alert.tagger.not_loaded`), photos
  and videos get their place tags only (`retryTagger` answers `modelserver.ErrNoTagger`, which
  processing skips quietly), and it is tried again 1, 2, 4... minutes later, an hour apart at most;
  Reprocess Media adds the missing tags.
- **Low-memory profile** (`files_manager/lowmem.go`): MemTotal under 6 GB (a 4 GB Pi 5 reports
  ~3.7 GB) or `[otc] low-memory = on` (`auto` default, `off`; the primary passes a configured
  choice to its supervised instances as `OTC_LOW_MEMORY`). Logged once at start ("low-memory mode:
  4 GB of memory ..."); a device with 8 GB and no setting logs and does nothing new. One media job
  at a time (`processingGuard`'s slot, taken through `beginProcessing(hash, kind)` by the lanes'
  jobs, the backfill and Reprocess - which take it themselves before `reprocessOneFile` - a
  thumbnail fix right before its decode, and - in low-memory mode only - a post's transcode), Go's soft limit 20% of MemTotal
  instead of 40% (`setMemoryLimit`, after `cfg.Init` now), content budget MemTotal/16 (the 256 MB
  floor on 4 GB), ffmpeg with `-threads 2` (`ffmpegInput` for frames and transcodes, plus the
  encoder's; a 4K HEVC frame took ~680 MB with ffmpeg's own thread count on 16 cores, ~270 MB with
  1-2), glibc's mmap threshold fixed at 1 MiB and 2 arenas (`hardening.ReturnFreedMemory`, cgo
  `mallopt`: Linux refuses MADV_DONTNEED on mlocked pages, so freed native memory stayed resident;
  Go's own scavenger falls back to mmap and is fine). RAM++ runs without ONNX Runtime's CPU arena and
  memory pattern (`imagestagger.LoadOptions{NoArena}`), loads at first use (`waitForTagger` loads it
  before a photo's 10 s deadline starts) and is released with the face models after 10 idle minutes
  (`idleModels`, which also runs one model call at a time - tags or faces, local or a supervised
  instance's; `serveModels` = `modelserver.ServeLimited(..., 1)` reads one child's pixels at a time,
  "info" never waits). Face detection is unchanged (YuNet at 1600 px keeps ~100 MB; at 1024 px, or
  cropping from a 2x-downscaled photo, a group photo lost 2 of 8 faces). After each job a new
  resident-memory high-water mark (128 MB steps) is logged ("memory high-water mark: this process
  holds ... MB"), and each model load logs what the process holds after it.
- **Run marker** (`files_manager/runstate.go`): `.otc-run` in the instance's working directory
  (the StateDirectory, or the user's home - not the RuntimeDirectory, which systemd empties at every
  stop), written at start with the boot id and the OOM kill counters (`/proc/vmstat` `oom_kill`,
  the cgroup's `memory.events`), refreshed when they move (a look every minute and at each
  in-flight write, so another program's OOM kill long ago isn't taken for this run's). bin/otc.go
  catches SIGTERM before `filesmanager.Init` writes it, and `stopServices` removes it first
  (`Manager.Stopped()`), except when the cgroup's oom_kill rose during the run: systemd's default
  `OOMPolicy=stop` then stops the whole unit after the kernel killed one of its processes (a
  supervised instance, an ffmpeg), and the marker stays, `stopped_oom`. Found at a start, the
  previous run died: same boot and a counter higher than at the last look (or `stopped_oom`) = out
  of memory - one Alert per series (`dao.UpsertErrorNotification`, kept by id in the marker: "Your
  device ran out of memory and restarted 12 times since 19:34", detail "This device has 4 GB of
  memory, ..."; never a push). Other deaths are only logged, another boot (a power cut) counts
  nothing. A series is deaths of runs younger than an hour. The run after a death goes one job at a
  time and records the hash in flight (rewritten in place, padded, never truncated - ext4 flushes
  a truncated-and-rewritten file at once) for an hour (`recoveryFor`; a low-memory device always
  does). Its analysis, backfill and Reprocess (Cancel ends that wait) - never an upload's
  thumbnail - wait 1, 2, 4, 8, 16, then 30 minutes, only when the death looked like processing's
  (out of memory, or content in flight), and a death during the dead run's own pause doesn't make
  it longer (`level`, `resume_at`). Content in flight at two deaths gets set aside, unless a model
  was loading (`loading`, set by `idleModels.load`): no strike for that. Set aside
  (`files_manager/setaside.go`): its `pending_analysis` row goes, it gets the backfill's
  `.nothumb` marker, it is listed in `<storage>/.set-aside` with when and the binary
  (`binaryID`: size and mtime) - skipped by Reprocess and the thumbnail fixes - and gets an Alert
  of its own ("<name> was set aside"). `retrySetAside` at start gives it back (pending again, the
  marker removed) under another binary or after 30 days. The memory Alert and set-aside rows are
  standalone: `actor_domain = '(standalone)'`, which `AddErrorNotification`'s grouping skips (it
  joins only rows with an empty one). The unit keeps systemd's default `OOMPolicy=stop`;
  `OOMPolicy=continue` (an ffmpeg or a supervised instance killed without restarting everything)
  would need install.sh and a release script.
- `modelserver` — issue #167: the primary instance loads RAM++ and the face models once and
  serves them on `models.sock` in its working directory (0600, Unix socket); the supervisor sets
  `OTC_MODELS_SOCKET` on each child, which then uses `modelserver.Client` for
  `files_manager`'s `Tagger`/`FaceDetector` instead of loading its own copy (~1-2.3 GB resident).
  Protocol v1
  (`cProto`, reported by "info"): a gob header, then raw RGBA pixels (`request.N` = 4*W*H, checked
  under 2 GiB before allocating); tags use `tags-resized` with only the 384x384 image the child
  scaled itself (`imagestagger.Resize`, then `TagsResized`), faces still get full resolution. A
  child speaks v1 only when "info" reports it, and the primary still accepts v0 (pixels inside the
  gob message) - keep both when changing the socket. The primary reads and answers two requests at
  a time (`cServedAtOnce`; one in low-memory mode): a face request carries the whole photo. With no
  tagging model loaded the answer is `ErrNoTagger`, which reaches a child as itself.
- `supervisor` — issue #82's users' instances, run by the primary. One that keeps stopping is started
  again after 4 s, doubling to 10 minutes (`respawnBackoff`; a run of 10 minutes or more starts a
  new series), and the 5th stop in a row is alerted, once a series (`dev.alert.user.keeps_stopping`);
  Stop ends a pause at once. Its ini, on the SD card, is rewritten only when it changes
  (`writeIfChanged`).
- `face_recognition` — (matching, issue #173: every face row is kept, but new faces are matched
  against at most 20 decrypted *reference* embeddings per person cached in memory -
  `files_manager/face_refs.go`; at 20 an outlier isn't added and the most redundant reference is
  dropped, so the set stays varied; the cover medoid is over the references;
  `InvalidateFaceRefs` after Reprocess, person delete/merge) issue #52's "People" search: detects faces (YuNet) and embeds them (SFace)
  via `gocv`, humans only. `files_manager.processFaces` (called from the slow processing lane) gates this on `settings.face_recognition_enabled` (off unless the owner turns it on - in Settings or the setup wizard; release 43 made the column default 0 again, issue #178, since faces are biometric data) checked *at upload
  time* - enabling it later never retroactively processes anything already in the library, by
  design (see the `faces` table's doc comment in `db.sql`). Requires CGO + a real OpenCV install at
  build time (see Build section); optional at runtime like APNs - a device with `[faces]`
  unconfigured just has the feature unavailable, nothing else affected. On Linux each model is
  probed first (`model_probe_linux.cpp`), so a bad or empty model leaves faces off instead of
  aborting. A hash's faces go with its last file or version (`dropFacesOfHash` ->
  `dao.DelFacesByHash`, which also deletes unnamed people left with no face); lock order is always
  the hash lock, then `outOfImagesMu` (issue #192), then `faceRefsMu`, and `processFaces` never takes a hash lock. Only the people who
  lost a reference are reloaded (`faceRefsStale`), the whole set only after a database error.
  Issue #181 (release 74): image tagging has the same kind of switch, `settings.image_tagging_enabled`
  (on by default, `SetImageTaggingEnabled`, read per file in the slow lane by
  `imageTaggingEnabled`); when off only the place tags from a file's own location data are
  written. Web, iOS and Android show it under Face Recognition.
- `bg_processor` — background job runner invoked from `files_manager`/`websocket`.
- `websocket` — the `/ws` connection handler and dispatch switch described above; also owns
  `ensureBridgePool()`/`openBridgeConn()`, which the device uses to dial *out* to the bridge relay
  (`[otc] bridge-addr`) so the bridge can reach an otherwise unreachable home device. The pool is
  self-managing (5 ready, refilling in batches of 2 once it dips to 3) rather than a fixed count
  dialed once at startup — see `ensureBridgePool`'s doc comment. Issue #170: a pooled connection stops counting as available at its first relayed message (the bridge keeps it for the client's whole session), and the pool refills right then - `serveConnection`'s `onFirst`.
  The pool's `pending` counts dials in flight *and* retries waiting out a backoff; a successful
  registration ends every backoff (`bridgePool.wake`). Replies go through `writeReply` (stall
  deadline `cReplyStall`, 1 min, renewed per 64 KiB piece) and `marshalReply` (a marshal error sends
  a clone with invalid UTF-8 replaced). `reserveMemory` reserves only for a caller allowed to make
  the request (`GetMediaRange` 2x its span for a known token, `GetStaticAsset` 2x the file, too).
  Once the peer is gone, queued `pureDownload` requests are skipped (`GetMediaRange` is one) - never
  add one with side effects (`DownloadSharedLink` counts opens). Until a session or a friend exists
  (`anonymous`), a connection has `cMaxInFlightAnon` (4) requests at once - the next isn't read
  meanwhile, nothing is refused - and its messages are read with `wsframe.ReadFree`, 64 KiB free
  (`cAnonFrameFree`), the rest in `anonFrameBudget` (64 MiB, reserved by capacity); then 32 and
  `frameBudget` past 4 MiB (`frameRules`). `Listen` refuses another site's page with a 403 before
  the upgrade (`origin.go`, `originAllowed`, also the upgrader's CheckOrigin): no Origin (apps,
  otc-sync, friends' devices), an Origin whose host is the request's Host (the web app at any of the
  device's addresses, tunnels, Tailscale Funnel, which passes the Host on), one on
  X-Forwarded-Host from loopback or a container's trusted proxies, or one in `[otc-api]
  allowed-origins` (`OTC_ALLOWED_ORIGINS`); `null` and other schemes never. Strangers' routine
  requests log at Debug; refused origins, plain requests to `/ws`, passwords tried while locked out
  and unreadable messages before sign-in are counted, one Info line a minute (`anonlog.go`). `RespFriendships` never carries
  `Friendship.secret`. Push-registration syncs to the bridge go only through `mg.requestPushSync`
  (one at a time). `mediastream.Store.IssueShared(key, res)` reuses a token per resource for 5 min:
  give it a key only when it names exactly one resource every authorised caller may share (a
  gallery item, a post's media). The device still serves `ReqUploadFile` for old clients.
- `social`, `session`, `settings`, `profile`, `status` — feature-specific logic (social feed/friend
  sync, auth sessions, device settings, owner profile, RAID/disk/CPU status) sitting between
  `websocket` and `dao`. `session` also owns issue #101's in-memory session-token store
  (`session/tokens.go`): a browser can't keep the account password around the way the native apps
  keep theirs in the Keychain, so after a password `ReqAuth` it holds a single-use, TTL'd opaque
  token (`ReqIssueSessionToken` / `ReqAuthWithToken` / `ReqRevokeSessionToken`) that redeems back
  into the same already-derived `*Session`. Process-local and deliberately not persisted — exactly
  like the `*Session` objects it points at, tokens don't survive a restart. At most 32 a sign-in and
  10,000 in all (oldest out), swept every minute. A password change (`ChangeKey`, whose old
  password goes through `session.Attempts` as a sign-in's does: address, 1 s delay, lockout) starts
  a new password generation (`session.PasswordChanged`): every token goes, none is issued or
  redeemed for a connection signed in before, every other signed-in connection is closed
  (`Manager.passwordChanged` over `mg.live`; friends' stay) and the changer carries on; a sign-in
  checked before the change and done after it is refused (`signIn`). `session/ratelimit.go` (issue #117) is the per-address
  password-attempt limit: 5 failures in a minute lock that address out for a minute, answered with
  `Ack.code = "too_many_attempts"` + `retry_after_seconds`. The bridge reports each relayed client's
  address to the device with `BridgeClientInfo`, so the limit applies through the bridge too.
  `ReqAuth` runs through `session.Attempts.Attempt(addr, try)`: the limit is re-checked and the
  outcome recorded while holding one of `cAuthSlots` (2, matching `deriveSlots`), so guesses queued
  behind a failure cost no Argon2 derivation. The vault is created by `dao.PersistSecretIfAbsent`
  (a conditional insert under a mutex), and `session.New` checks a lost race - two first sign-ins
  at once - against the stored vault; never add a key or migration to `vault` for this. A new
  password needs `session.MinPasswordLen` (8 bytes: `ChangeKey`, the first password `session.New`
  sets, `init-owner-password`, `ReqCreateUser.password`); one already set is never checked against it.
  Settings > Users asks for a new account's first password (web, iOS, Android): `createUser`
  checks it before provisioning anything and stores the vault in the account's database
  (`dao.OpenUserDatabase` + `session.New`) before its instance spawns, so nobody can claim the account
  by signing in first; the person changes it later with `ChangeKey`. `RespUsers.password_set` is set
  on every CreateUser answer of a device that knows the field - unset, an older device ignored the
  password and the clients say the first sign-in sets it. A too-short first
  password is answered `Ack{Ok:false, ErrorMsg}` with **no code** - the phone apps take any sign-in
  code for the bridge's "device unreachable" and would keep retrying - undelayed and outside the
  lockout count (`AuthLimiter.Attempt`); `ChangeKey` answers `code = "password_too_short"`.
  Friend requests (issue #25) can be removed by either side: `ReqDeleteFriendship` deletes the
  local row and, best effort, sends `FriendshipInterDelete` (authenticated by the shared
  per-friendship secret) so the other device drops its copy; a sender whose request was deleted
  while it was offline learns it from `FriendshipStatus.not_found` on its next friend sync.
  Issue #174: removing a friend can also `delete_their_data` (`social.purgeFriendData`: their
  posts with media, their comments - `social_publications_comments.author_domain`, the device
  a comment was synced from, never what the event claims - and likes on any post, alerts) and
  `ask_them_to_delete_mine`: `FriendshipInterDelete.forget_me` when their device answers,
  otherwise a `forget_event` in `events` with `target` = that friend (`GetEvents` serves a
  targeted event to its target only) and the friendship "leaving" (`forget_requested`) until
  their device purges and sends a plain `FriendshipInterDelete`. Everything a friend's sync or
  request deletes is checked against the sender: `mayDeletePublication` (its own posts only),
  `mayDeleteComment` (its comments, or comments on its posts), forget = the sender's data only.
  A friend connection is re-checked on every request (`FriendshipAccess`), and may read media
  of the owner's own posts only (`ownPublication`). Issue #140: a
  friend request is only stored after the receiver dials the sender's domain back and it confirms
  (`DidSendFriendshipReq`); if the receiver already has a friendship with that domain, the
  sender is that friend's re-created device, so the row takes the new secret and profile in place
  (`relinkDecision`: accepted stays accepted, pending becomes the new incoming request, blocked
  stays blocked and the request is refused) instead of failing on the primary key. A re-linked
  accepted friendship answers the sender with `Ack.code = "accepted"`, which the sender stores at
  once (`social.ErrFriendsAgain`); the sender reuses its own existing row with the new secret and
  restores it (or deletes a row it just created) when the request fails. `ReqFriendshipRequest`
  always answers with an Ack, so the clients show the reason.
  Issue #169: every socket this device opens to someone else (a friend's device, the bridge's
  one-off calls) is a `wsframe.Client` (`wsframe.Dial`): 15 s handshake, 30 s per write/read,
  64 MiB read limit (`ReadMedia`: a whole file, 10 min, the 1000 MiB cap); friend sync closes
  each friend's socket after its pass, and Web Push uses a 15 s HTTP client. The pooled bridge
  relay socket clears the deadlines once registered.
  **Friend post sync** (`social.updateFriendEvents`): a file whose hash fails `dao.IsContentHash`
  is dropped (`NewSocialPublication` refuses one too, storing a post and its files in one
  transaction); the size recorded is what the files take on this disk (int64; release 92 clamped it to MaxInt32); media is pulled in
  an answer of at most the declared size + 64 KiB (`friendMediaLimit`; a JPEG 6x + 4 MiB, as a
  posted HEIC is served as its JPEG under the HEIC's size; a size of 0 or less keeps the thumbnail
  only) under `ReserveFriendMedia` of that limit (3x, waiting at most 2 min) and written by
  `writeFileAtomic`. A file's type is kept only when `mediastream.SafeType` keeps it (ServedType's
  inline list), else `application/octet-stream`, and every answer carrying a post's files maps it
  again (`safePostFiles`): the web shows them from blob: URLs in the device's origin. A comment is
  shown with the friendship's name for its `author_domain` (the domain without one), the owner's
  name for theirs, `publisher_name` only for rows from before release 51 (`dao.queryComments`), and
  a friend's is stored with the name the friend is known by, never the one its event signs. A transport failure (`errFriendTransport`, including the bridge answering
  `device_unreachable`/`account_disabled` for the friend) or a failed thumbnail write stops the page
  at that post without moving `latest_sync` past it; after 3 stops in a row (`cPostTries`) the post
  is given up. `GetEvents` never serves the last second and completes a full page with the rest of
  its last second; same-second rows come in `INDEX(dt)` order (insertion order) - don't add a
  tie-break. A friend's feed page is capped at `dao.MaxFriendFeedPage` (20). **Likes**: action
  `delete` removes the sending domain's like; any other action, `""` included, is a like. Likes
  insert with NOT EXISTS and only a new row notifies; release 92 added unique keys `like_once
  (pub_uuid, friend_domain)` and `comment_like_once (comment_uuid, friend_domain)`.
- The bridge shared secret never reaches a client (release 66): `GetSettings` leaves it out and
  `SetBridgeSecret`/`RegenerateBridgeSecret` are refused - the device pairs and rotates it itself
  (`regenerateBridgeSecret`), and no Settings screen shows or edits it.
- `push` — Web Push (per-device VAPID keys) and iOS pushes. The APNs auth key is the developer
  team's private key and lives **only on the bridge**: a device never has an `[apns]` section, it
  relays title/body to the bridge (`BridgeNotify`), which sends to the tokens that device itself
  registered - so a device can only ever reach its own phones. The device runs
  `Push.StartAsync(256)` (one ordered worker, inline when the queue is full); the bridge stays
  synchronous and builds its APNs/FCM senders once, so a new .p8 or service-account JSON needs a
  restart.
- `api` — the small HTTP layer: healthcheck, the `/ws` upgrade, and static file serving (serves
  `web/dist` copied to the device's static path; appends `.html` to extensionless paths for
  client-side routing). Pages (`.html`: index.html, the SPA fallback, a route's page) are
  `Cache-Control: no-cache` with a content ETag (`pageETag`: a release unpacks its bundle with
  its build times, so Last-Modified alone could keep the old page); Vite's hashed
  `/assets/<name>-<8 chars>.<ext>` are `immutable` (`staticCacheControl`, the bridge's
  `cHashedAssetPath` rule; `TestServeStaticCacheHeaders`). The bridge's relay of these files sets
  no Cache-Control, Last-Modified or ETag on HTML, so browsers never keep a page from it.
  Listener bind errors are logged, not fatal.
- `log` — leveled logger with size-based rotation, configured once in `main()` from `[logger]`.
  A log file that can't be opened or read (a read-only card, its folder gone) no longer ends the
  process - which used to hang holding the logger's mutex, then exit into a restart loop: the lines
  go to stderr (journald) and the file is tried again every minute (`toStderr`, `retryLogFile`).
- **Plaintext never on the SD card** (issue #156, releases 62/65; devices build `go build ./bin/otc.go` - one file - so `bin/` must stay a single file and helpers live in packages like `hardening`): the service `mlockall`s its memory at
  start (`hardening/hardening_linux.go`; the unit has `LimitMEMLOCK=infinity` and `LimitCORE=0`; `[otc]
  disable-mlock=true` turns it off), swap is zram only (`/etc/rpi/swap.conf.d/90-otc-ram-only.conf`,
  no `/var/swap` writeback; dphys-swapfile removed; Makefile.pi's `swap` is no longer in bootstrap),
  and `TMPDIR` is a per-process `otc-<pid>` directory on a tmpfs (`/tmp` when it is one, else
  `/dev/shm`), made by the process and never taken as found (`ownTempDir`: `otc-<pid>-<random>` when
  the name is taken; 0700, ours), with dead processes' directories swept at start (a supervised child resolves an
  inherited `otc-<pid>` TMPDIR to its base, `tempBaseCandidate`, so it never sweeps the primary's).
  At `level=info` the log never names
  a file path, search term, share path or Wi-Fi network - those are Debug only - errors name hashes,
  and no request is dumped whole (a friendship secret once was).

### Localization (`docs/i18n.md`, `i18n/`)

Every surface ships in English plus es, fr, de, it, pt (pt-PT) and nl since 2026-10-11 (device
release 119, otc-sync 0.16.0, macOS 2.2, the bridge's site, account page and emails; the iOS and
Android builds follow once their store reviews clear). Translations were made and reviewed by
back-translation (`"reviewed": "claude-backtranslation …"`); a new or changed English text needs
all six before `I18N_RELEASE=1` passes. The design is `docs/i18n.md`; the contributor guide is
`i18n/README.md`. The user's language is
stored on the device per user (`Settings.language` / `Status.language`, `optional`: absent =
device predates it, `""` = Automatic, `SetLanguage` = 138), and every app sends its effective
language as `ReqEnvelope.lang` (= 2); a request without it gets today's English, byte for byte.
One source, `i18n/strings/<code>/<prefix>.json`, generates every platform's files: `make i18n`
(web `web/src/i18n/`, Apple `OffTheCloud/i18n/` with the hand-written `L10n.swift`, Android
`cloud/offthe/otc/i18n/` with the hand-written `UiText.kt`, Go `i18n/catalog` + `i18n/*_gen.go`
for package `i18n`'s runtime, the wizard's `# BEGIN GENERATED I18N` block); never edit those
generated files or the catalogs in Xcode. `make i18n-check` validates (placeholders, tags,
glossary in `i18n/glossary/`, staleness), fails on a stale generated file, runs the
hard-coded-text ratchet (`i18n/scan/README.md`: a file whose count of literal UI text rises
fails - lower the baseline with `go run ./i18n/cmd/i18nscan -update -path <files>` in the commit
that moves text into the catalog) and builds what embeds the catalog. It gates
`scripts/release.sh` (on a `git archive` of HEAD; needs `make pb`'s `proto/generated` and
`web/node_modules`), `scripts/desktop-release.sh` (needs `i18n/` committed), `make -C bridge
bridge`, `make android-release` and CI (`.github/workflows/i18n.yml`). Codes before prose: clients check `error_code` first and read
the English message only when the code is empty (`duplicated_file` on UploadFile/FinishUpload/
LinkFile, from `filesmanager.ErrDuplicatedFile`; `unknown_payload`) - iOS/macOS `ErrorCodes.swift`,
Android `net/ErrorCodes.kt`, otc-sync `engine/requests.go`. Machine alerts (update, RAID, disk
space, Wi-Fi wiring) live in `websocket/machine_alerts.go`; `raidwatch.MissingPorts()` keeps the failed disks'
ports as data; update alerts are one per `notifications.update_version`, with the legacy English
titles still matched (`updateTitles` keeps them even if the title is reworded).

Moving text into the catalog (phase 2; docs/i18n.md "Text the device writes" and "Each
platform"): a request without `lang`, with `en`, or in a language the build doesn't carry renders
today's English byte for byte (`devtext.English`), and tests that compare English keep their
expected strings. Device replies: packages return devtext errors (`devtext.New`/`Wrap`/
`WithEnglish`), reply sites use `devtext.Text(lang, err)` or `websocket/replies.go`'s `errText`/
`sayOr`, and a raw error the device can't translate goes after `dev.err.action_failed` in another
language. Alerts: `alerttext` (dao paragraph above). The apps show the device's and the bridge's
words as they come (already in the request's language) and keep their own text as keys or a
`UiText` until shown: web `components/uiText.ts` (`say`, `asIs`; `percentText`/`unitText`/
`plainNumber` keep hand-written numbers byte-identical), Android `UiText` plus
`ui/common/AppText.kt` (`now()`, `TextIOException`) and `.english()` in JVM tests
(`test/.../i18n/English.kt`), Swift computed accessors and `LocalizedError`s, otc-sync `Msg()`
errors. iOS model text is an enum worded when shown (`GalleryNotice`, `PeopleNote`, `FriendsToast`,
`ComposerAlert`), Settings' `SettingsText` (`say`/`asIs`/`error`) or a private `@Published` `() ->
String` behind a computed `String` (`FilesExplorerViewModel`); Android picks the device's words or
ours with `deviceOr`/`Throwable.shownText()` (`ui/settings/SettingsViewModels.kt`) and puts a key's
text inside another key as a nested `UiText.Res(id, listOf(nested))` (`PhotoMonths.kt`). Shared
patterns (docs/i18n.md "Each platform"): a date written by hand is a key with `text` pieces
("{month} {year}", "{day} {month} {year}"; month names from `Intl.DateTimeFormat(t.tag, {month})` on
the web, `app.photos.months.*` on the phones); a sentence that would need two plurals takes its
counted pieces, each its own plural key, as a `{what}` text argument; name lists are two recursive
keys ("{first} and {second}", "{first}, {rest}"), never `Intl.ListFormat`; a number written by
hand follows the language's canonical tag, never the region (web `plainNumber`/`percentText`, iOS
and Android `decimalText`, the Mac's and otc-sync's `desk.main.percent`), so English stays byte for byte; a typed confirmation
sends the code constant (the account page shows and accepts `site.account.delete.word`, and
`delete`, but always sends `{"confirm": "delete"}`; deleting a user in Settings asks for the
username). The setup wizard (`wiz.*`) takes `?lang=` from the apps (iOS `BLESetupLang`, Android
`withLang`, Mac `BLESetupTransport.withLanguage`); in a browser its page has a language list
(Automatic first, which drops `?lang=`), hidden inside the apps, which call
`window.otcSetupLang(code)`: it reloads with `?lang=` on the same step, keeping the state in
sessionStorage (never passwords or the terms checkbox); the Mac's setup window has its own
language menu (`LanguageMenu` in `LanguagePicker.swift`). It words install.sh's steps from its
`[otc-install] @step|@error <token>` marks, only when the key's English equals the line that
follows. The bridge's landing, comparison, account, not-found, disabled and unavailable pages are templates in
`bridge/site` (Frontend below); its emails follow `accounts.lang` (their links go to
`/<code>/account` for a carried non-English language; `mail.date.long`'s `{day}` is a count only so
French can write "1er janvier"), while its JSON `error` texts and websocket replies stay English (a
fallback only) and clients word them by `code`.

The stored choice (`docs/i18n.md`, "The stored choice"; every client follows the same rules):
- Device (release 118, `scripts/updates/118.sh`): `settings.language` and `last_ui_language`.
  SetLanguage is owner-only (`websocket/language.go`), compare-and-set under `settings.langMu`,
  Ack codes `changed` and `invalid_language`. `processMessage` passes `requestLang(env)` to the
  three `process*` handlers as `lang`, never as connection state (requests run concurrently).
  Owner push registrations write `last_ui_language` after they succeed. The push language is
  `push.ResolveLanguage` -> `UpdatePushRegistrations.language` and `Push.Lang()`. A binary newer
  than its schema gets `dao.ErrSchemaBehind` (MySQL 1054) and carries on with the defaults.
- Bridge: migration `010-language.sql` (`accounts.lang`, `push_registrations.language`), tolerated
  when absent (`bridge/dao/language.go`); `PUT /api/account/language {"lang"}`; sign-up takes the
  body's `lang` else `Accept-Language` (`""` when none matches); `/me` carries `lang`; the account
  page sends its own language at sign-up from a non-English page and, on a non-English page,
  PUTs it once for an account whose `lang` is `""` (never overwriting one, never sending `en`);
  the export adds `account.language` and `domains[].push_language`; the offline alert's language
  comes from `push_registrations.language` (`offlineAlertText`). Never log a language next to an
  account or domain.
- Apps: a local copy plus a pending change, sent with SetLanguage against the device value seen
  when it was made. Sign-out, Log Out, Disconnect or another device/password keeps the copy and
  drops the pending change - except one made for the next device (no device set up; on macOS,
  otc-sync and the phones also none of its values seen yet, e.g. a wrong password - the phones
  keep `seen` in memory only, forgotten when the device turns the password down and at
  `OTCConnection.invalidate()`; picked in the Mac's setup wizard; or still pending when a phone
  leaves a device from the "isn't available"/"can't connect" card, `loggedOut(carryPending:)`;
  macOS `languageForNextDevice`, otc-sync `language_next_device`, iOS/Android `forNextDevice`),
  which the next sign-in sends without `expected`. The web and the phones send nothing before a
  sign-in is accepted (phones: `Device.signedIn` / `LanguageDevice.signedIn()`) and send at every
  accepted one, also on an already-open socket, ahead of its first Status/Settings (phones:
  `LanguageSettings.connected()` from `OTCConnection`). On the phones new credentials alone only
  forget `seen` (a change made signed in keeps its `expected`; "changed" adopts the device's
  value), Settings' Log Out drops a change made signed in, and a relaunch keeps a pending one.
  The phones' `LanguageMenu` (a globe and the language's own name, Settings' list) is on Welcome,
  the Bluetooth setup and the two cards; the setup page is asked with `window.otcSetupLang`, and an
  older wizard is reloaded once installing (`ios.`/`android.setup_language_later` until then).
  The 30 s window runs from the change or its last send.
  `unknown_payload` keeps it pending until the next connection. `lang` is set only in
  iOS/Android `WSClient.exchange`, macOS `WSClient.envelope`, otc-sync `Client.Request` and web
  `ws.ts`. The Language row/panel/picker/tray submenu is hidden while
  English is the only shipping language. Parity: iOS `LanguageSettings.swift` <-> Android
  `i18n/LanguageSettings.kt` (rules in `LanguageSync`); macOS `LanguageSettings.swift` <-> otc-sync
  `engine/language.go` + `internal/oslang`; `otc-sync language [auto|<code>]` (English output).
  Android recreates the Activity on a change, except on the Bluetooth setup (the manifest's
  `locale|layoutDirection` plus `LanguageSettings.holdInPlace()`, so its link and page survive):
  state that must survive uses `rememberSaveable` or a view model (the connection forms'
  `ConnectionFormModel`; never a password in saved state), and launch-time prompts ask only on a
  fresh create. `node web/scripts/i18n-test.mjs` tests the web runtime and choice.
- New web text goes through `useT()` / `<Trans>` from `web/src/i18n` with an entry in
  `i18n/strings/en/web.<area>.json`; never keep translated text at module level or in state
  (keep a key or a `UiText`). A `UiText` is a function, so never hand one bare to React:
  `useState(() => say(…))`, `setX(() => text)`. Round a value that isn't whole with `toFixed`
  before `unitText`/`percentText` (Intl rounds ties differently), write B/KB/MB/GB sizes with
  `components/sizeText.ts`, and format dates with `fmtDate(t.lang, d, options)` (`dateTimeFields`
  for `toLocaleString()`'s fields), never `toLocaleString()`: the page's language, in the
  browser's own format when it is that language. The public `/shared` page shows the visitor's browser language (Automatic,
  or that browser's own stored choice).

### Bridge (`bridge/`)

A separate deployable with its own `dao`/`websocket`/`api`/`makefile`, sharing only `proto/generated`
and `cfg` with the device module. It maintains a pool of authenticated device WebSocket connections
keyed by domain (`bridgePool` in `bridge/websocket/websocket.go`) and proxies friend/browser traffic
to the right device — the device never accepts inbound connections directly.

Relay invariants: a `/ws` socket not yet paired has a 2 min read deadline (`cUnpairedReadTimeout`)
and is closed on every return path unless a device relay owns it (`handedOff`). Every write to a
client goes through `writeClient` (fragments above 256 KiB, a fresh 60 s deadline per piece, so only
a client that stopped reading trips it) - never on a device socket; `pingLoop` uses `WriteControl`
so pongs flow during large uploads. Device replies are read under `replyBudget` (8 GiB), and
`forward()`'s `release` is never nil. Pairing is limited per client address (IPv6 by /64) and Host
to 64 open (`[bridge] max-paired-per-addr`), 2/s with a burst of 30, and per device to 64 in use
(`[bridge] max-paired-per-device`). A relay records its owner uuid (never the secret), and a
registration evicts other owners' relays. `DropDomains` (release, account deletion,
`BridgeReleaseDomain`, inactivity) and `DropReplacedIdentity` (claim replace, `accountNewIdentity`)
publish on Redis `otc:drop` for every node, and every 20 s each node checks its domains against
MySQL (`dao.DeviceOwners`). One node sends the offline alert per outage (`otc:alert:<domain>`). The
bridge's Web Push client reaches only public addresses, without redirects.

Fleet tab (`bridge/fleet`): every host's `otc-fleet-agent` (unprivileged user `otc-fleet`, passwords
as systemd credentials) and every bridge node (`fleet.Reporter`, counts from
`Manager.FleetStats`) write a JSON snapshot to Redis every 30 s (`fleet:host:<name>`,
`fleet:bridge:<node>`, 10-minute TTL, `fleet:known-*` remember the last report);
`GET /admin/api/fleet` reads them (`fleet.Read`) and judges each host with `fleet.Evaluate` - the
one function the email alerts use too (`fleet/alerts.go`: one node at a time under
`fleet:alert-lock`, bad twice in a row or warn 5 times before mailing, reminders every 6 h, one
"resolved", grouped per minute, state in `fleet:alert-state`; `[fleet] alert-to`). Snapshots carry
counts and host figures only - never a domain, account or address; MySQL errors lose their quoted
values.

Growth tab (`GET /admin/api/growth?days=N`, `dao/growth.go`; migration 011): the site's anonymous
visit counts and the GitHub download counts, with the device figures (names; active = a client
relayed in the last 1/7/30 days, `last_client_at`). `serveSite` counts a page someone opened
(`api/visits.go`: own host, GET, 200, no prefetch, no robot by User-Agent, which is never kept)
under day, page and source - `ref:<tag>` from `?ref=`/`?utm_source=`, else the Referer's host
(never a device's address or an IP; nothing but `ref:` for `/account`) - added up in memory and
written with the traffic counts (`flushPass`), at most 1,000 named sources per day per node, the
rest under `(other)`. `bridge/downloads` polls the releases every 6 h into `download_counts`
(`src.tar.gz@v<N>`: devices that took release N). Both pruned after 25 months. The privacy page's
"Visit counts" row describes exactly this: keep the two in step, and the record in
`private/gdpr/records-of-processing.md`.

### Frontend (`web/`)

React 19 + TypeScript + Vite, routed with `react-router-dom`. The signed-in shell is Google Photos-like
(App.tsx, index.css). A fixed top bar holds the menu button, the logo and the search (`TopSearch.tsx`: chips
for the open collection, people and tags inside the field; no panel until something is typed, then the
matching things (5 tags), named people (the 5 with the most photos) and files and folders by path
(`SearchFiles`, debounced, stale answers dropped), plus "Search documents for x" - every match (`SearchFiles`
limit 50) listed in Files over the folder, which stays as it was; neither on a device answering `unknown_payload`.
A picked folder or file opens in Files through `filesNav.ts` - a file as a click there would), and at the right
the account button (`AccountMenu.tsx`: picture, name and device address, Settings, Sign out through
`net/signOut.ts`). Signed out, the bar holds a password field beside Sign In instead (`TopSignIn.tsx`, which
calls `net/signIn.ts`; on a phone Sign In opens the field across the bar); `views/SignIn.tsx` is only a new
device's setup, shown when GetPubKey says `is_new_device`. The left menu
(`Sidebar.tsx`) lists Images, People, Collections, Files; Sharing: Social, Friends; Device: Alerts, Settings;
storage at the foot. People - menu item, page, search, person filter - exists only while face recognition is
on (`faceRecognition.ts`: asked after sign-in, set by Settings' switch, the last answer kept in
`otc_face_recognition` so a reload doesn't flash it; unknown means hidden). Three widths: ≥1024px it is
whole or a rail of icons with labels (menu button, remembered in `otc_menu_open`); 600-1023px always the
rail, the button lays the whole menu over the page; below that only the overlay. The window scrolls -
nothing may give `main` (or another ancestor of the pages) an overflow, or `position: sticky` measures from
it (release 103's 64px gap) - so sticky and fixed things offset by `--topbar-h`, and a page's own fixed bars
use `left: var(--content-left)`.
Image groups are called **collections** in every UI (web, iOS, Android, site, store texts); the protocol
and code keep `ImageGroup`/`groupId`. Inside an open collection the selection bar has **Remove from
collection** (`RemoveFromImageGroup`, release 129: the membership of each selected photo's hash, never the
file). The web app always has a device that knows it (the device serves it); the phones may not, and turn
its `unknown_payload` into "needs an update" with an Update button for the primary's owner (`ReqApplyUpdate`)
or, for a user, who to ask. What Images shows is `photoFilter.ts` (tags, people, open
collection; a store the search, People and Collections pages and the grid share); the people, collection
and tag lists are `libraryStore.ts` (cover URLs made when a list arrives, never in a render). Images
(`PhotoGallery.tsx`) groups date-ordered photos into month sections, Google Photos-style selection (hover
or long-press, a drag for a run - see below - numbered in post order) with a bar over the top bar, dialogs for posting and collections,
and `DateScrubber.tsx` (a column of years with a mouse, a draggable handle on touch). `People` and
`Collections` are pages of their own (`PeopleView`, `CollectionsView`). `web/src/net/` holds the WebSocket/proto
client; `web/src/views/` are top-level routed pages (`SignIn` - a new device's setup - and `Social`); `web/src/components/` are the
feature widgets (files explorer, photo gallery, friendships, settings, status, profile, social feed —
each with a co-located `.css`). Built output (`vite build`) is copied by `make web` into the device's
own static dir and the iOS app's bundled web assets. The bridge does **not** get its own copy (issue
#95): a browser hitting `<device>.off-the.cloud` for a static asset is proxied straight through to that
device over the same bridge tunnel every other request uses (`ReqGetStaticAsset` /
`staticassets.Resolve`, shared with the device's own direct HTTP static handler) rather than served from
a separate bundle the bridge would otherwise need redeployed by hand on every web change — this is what
lets a device running an older build still work correctly through the bridge. The bridge's *own*
pages are deployed by `bridge/makefile` independently of a device's web build. The public landing
page, the account page and the pages shown for a disabled or unreachable device are html/template
files in `bridge/site/pages/*.html`, embedded in the binary (package `bridge/site`) and served by
`bridge/api/site.go` in the visitor's language: `/` and `/account` (English, or a 302 to
`/<code>/` / `/<code>/account` by `Accept-Language`, query kept), `/en/`, `/en/account`,
`/<code>/` and `/<code>/account`, `/account.html` -> 301 `/account`, `/privacy.html` and
`/terms.html` -> 301 without `.html`, a page's address with a last slash (`/account/`,
`/es/private-social-network/`, `/privacy/`) -> 301 without it, hreflang plus x-default (and `pt`
beside `pt-PT`, the same page), a footer language switcher once a second language ships, and a
CSP with a nonce per response (docs/i18n.md "bridge"). An address the host doesn't have is the
site's own 404 page (`notfound.html`, noindex, in the path's language or `Accept-Language`'s) for
a browser asking for a page (`Accept: text/html`), the bare 404 for anything else. They are never read from `/var/www`, where older deploys left copies (an
old `account.html` there is never served). `bridge/static/` holds privacy, terms, the admin panel
and the assets, copied to `/var/www` by the deploy. For search: the
three comparison pages (`social.html` at `/private-social-network`, `alternative.html` at
`/google-photos-alternative`, `drive.html` at `/google-drive-alternative`, each also under
`/<code>/...`, routed like `/account`) and the Raspberry Pi page (`pi.html` at
`/raspberry-pi-home-server`: what it needs, the setup, the mirrored drives, the lights, a new Pi;
linked from the hardware answer and the footer, its claims the landing page's FAQ's) share the
landing page's head, header and footer (`pages/common.html`, `pages/site.css`, plus
`pages/compare.css`); their head carries Open Graph tags and one JSON-LD graph (`bridge/site/head.go`:
the same `@id`s in every language, nothing the page doesn't state - no ratings, counts or
versions, and no person: the Organization has no founder; `og-1200x630.jpg` is the hero photo's
crop); the account page says noindex. Titles are at most 60 characters and descriptions 155 in
every language (the keys' `max`); the comparison pages' titles leave out the brand, which search
engines show beside them. `/robots.txt`, `/sitemap.xml` and `/llms.txt` come from
`bridge/site/crawl.go` (llms.txt rendered from the English catalog, its keys in `llmsText`, each
question linked to its own answer), and `/llms-full.txt` from `llmsfull.go`: every public page in
English, rendered and read back into Markdown (its `<main>`; no pictures, scripts or forms), made
once per origin. Every FAQ question is a `<details id="faq-<key>">` (`faqID`:
`site.landing.faq.social_how.q` is `#faq-social-how`) that common.html's `faq-script` opens when
the address names it. The header links the account page in the page's language (`.Account`, and
`localHref` for the texts' `account`/`signup` tags); below the width its links need (1006 px in
English, 1120 px otherwise) they move into a Menu, a `<details>` with no script, and on a phone
(480 px) "Create an account" too. It stays at the top of the whole page (a `body` only as tall as
the window let it go after the first screen), and `scroll-padding-top` keeps a `#section` below
it. `site.css` carries `bridge/static/fonts/fonts.css`'s `@font-face` rules word for word
(`TestFontFaces`) and the head preloads Unbounded's and Manrope's latin files, so the public
pages don't wait for a stylesheet; the account, privacy and terms pages still link `fonts.css`.
Pages and those files are gzipped, and so are the static directory's text files (`compress.go`:
`fonts.css`, privacy, terms, `sw.js`; never on a Range request); unknown addresses on the
bridge's host are 404 (never the old web app's `index.html` left in `/var/www`); `www.` is a 301
to the bare domain; every answer for a device's address says `X-Robots-Tag: noindex`
(`noindexDevices`). Static files: content-named fonts cached for good, a picture asked for with
its `?v=` 30 days, other pictures and `fonts.css` a day, HTML `no-cache`. A device's web app
files, relayed by `proxyStaticAsset`, go out with `X-Content-Type-Options: nosniff` and
`Content-Security-Policy: frame-ancestors 'none'` only (a fuller policy would have to fit every
web app build a device may still run; the device types each file by its extension), and text
gzipped for a client that takes it; a content-named one (`cHashedAssetPath`) is compressed once,
kept by the SHA-256 of the device's bytes (never by its name: another device's file under the same
name never stands in for it) in a 32 MB least-recently-used cache. The landing hero puts
`img/hero-{900,1280,1600}` (WebP and JPEG, from `docs/promo/pi-photographer-desk.jpg`) left of the
summary, stacked below 820 px; its install block (`#install`) has three tabs, the ARIA tabs
pattern (arrows, Home and End select; only the selected tab is in the tab order; chips below 600
px): "Computer app" (first,
selected, "Recommended") - the Mac App Store, Windows and Linux buttons are the Downloads
section's links (`TestInstallTabs` keeps them equal; the visitor's OS is ember and first) and its
steps say only what issue #184 below does (the Mac hands the card to Raspberry Pi Imager and can
finish over Bluetooth; otc-sync writes the card and the phone app finishes) - then "Raspberry Pi
5 image" and "Install script" (`/#script` opens it); the header's "Get started" goes to it, and the
section under the hood (`#get-started`) is the install script's. The gallery (`#screenshots`) has
the device photo, the Mac popover (`macos-sync.jpg`), five phone shots (`mobile-1..5`) and six web
shots (`web-1..6`, three to a row), each shown as a WebP thumbnail at two widths, about once and
twice its size on the page (`mobile-N-{180,360}`, `web-N-{450,900}`, `device-{460,920}`,
`macos-sync-{340,680}`; `cwebp -q 80 -m 6 -sharp_yuv -resize W 0` from the full picture), with
`srcset`/`sizes`, opening the full picture (`data-full`) in the lightbox, which makes the page
behind it inert and keeps Tab on its buttons; the JSON-LD's screenshots are full pictures
(`TestStructuredData`, `TestLandingPictures`). No picture shows a real person (the AI sample
library of `docs/promo/source/library`, animals, or nothing). A picture that replaces another
keeps its file name, because the deploy copies over `/var/www` and never deletes, so the old file
is overwritten. It, and its thumbnails made again, also get a new `?v=` in the page.

Web invariants: never call `URL.createObjectURL` during render. PhotoGallery and NewPostPicker use
`useObjectURLs(items, make)` (`make` module-level and stable); other URLs are made in a memo or
effect and revoked in its cleanup - only `blob:` URLs, never a `/media` stream URL. Social's `Post`
is a module-level memo component: keep its callbacks stable and `highlighted` a boolean, or every
post re-renders. Infinite-scroll grids use `usePageRetry` (1 s doubling to 10 s; user searches
`reset()`). Files reads non-media with chunked `ReqReadFile` (`readAll`; a file replaced mid-read is
read again) and uses `GetFile` only for what the device converts. `useWS`: `lastAuthRef`, the
password replayed on reconnect, is set on an ok Ack (or when nothing answered and nothing verified
is held) and cleared when the device refuses it; a typed password that met a lockout is not replayed,
and one typed on the page (`signInWithPassword`: `keepOnlyIfAccepted`) is never kept after no answer -
the person presses Enter again; with `refuseNewDevice`, a device whose GetPubKey answer (the same one
the password is encrypted with) says it is new gets nothing (`NewDevice`), so it never adopts a sign-in
as its owner password and its setup shows instead;
call `useWS.passwordChanged(newKey)` after a ChangeKey; the bridge's `device_unreachable` is not a
refused sign-in (`authOwed`); only a tab that has signed in (`hadSession`) redeems the stored token
on reconnect. Sign Out also clears `otc_files_path`, `otc_photo_search_tags` and a pending
language change (`otc_language_pending`; `otc_language` stays). `net/`'s errors carry translated
messages: test for a request with no answer with `err instanceof NoAnswer` (`net/ws.ts`), never
by its text, and import `t` there from `../i18n/runtime`, never `../i18n` (a cycle through the
provider and `useWS`). `web/` has no test runner
(`node web/scripts/i18n-test.mjs` tests the i18n runtime and ws.ts's `lang` line through Vite's
SSR loader); `npm run lint` already reports about 150 problems (mostly `no-explicit-any`), so
compare counts before and after a change.

"Your device was updated" (`components/DeviceUpdatedNotice.tsx`, rules in `deviceBuild.ts`,
`node web/scripts/device-build-test.mjs`): a single-page app never reloads itself, so a tab open
across a device update kept the old web app. A build is the web app's own entry script
(`entryScript`: Vite's hashed `assets/index-*.js` in the page's HTML): the page notes its own at
start, and after every accepted sign-in (`useWS.onSignedIn`: the first, and each a reconnect
makes) fetches `/` with `cache: "no-store"` and notes the one the device serves now; another one
shows a card at the foot of the page (in an always-present `role="status"` region, so it is
announced) with Reload (`location.reload()`) and an ×. Never reloads by itself; one notice per
build (dismissed, it comes back only for another build; back on the page's build it goes). Only
where the device served the page (not `localhost` dev, not `__OTC_CONFIG`); it works for every
account and needs nothing from the internet (an earlier draft compared `CheckUpdate`'s version,
which only the primary instance answers and which fetches the release list from GitHub).

The first-run tour on the web (`components/Tour.tsx`, rules in `tourSteps.ts`, `node
web/scripts/tour-test.mjs`; the phones have their own `TourView` with the same rules and their own
steps): a card in a native modal `<dialog>` (centred from 600px, a sheet at the foot below that),
one step at a time - welcome, menu, images, people (only while face recognition is on: steps are
kept by id, so People can come or go mid-tour), collections, files, social, post, friends, alerts,
done - with Skip on every step. What a step says never depends on the page, and every label it
quotes is an argument from that label's own key (`web.tour.*`; section titles reuse
`web.shell.menu.*`), so it follows their wording in every language. It opens by itself once per
browser origin, the first time the signed-in shell shows, however the sign-in came (a restored
session too, so people signed in before the tour get it once) - never over a new device's setup,
in the phone apps' web view (`__OTC_CONFIG`), on `/shared` or with `?download`: `claimAutoTour`
writes `localStorage.otc_tour_seen` = `cTourVersion` (1; raise it only to show a rewritten tour
again) before it shows, storage that throws means no tour by itself, and Sign out keeps the flag.
Show the tour (account menu, between Settings and Sign out; Settings › Getting started, after
Language) replays it from step 1 and leaves the flag alone. App keeps `tour` (`{step, auto}`), so it
comes back at its step after the unreachable screen, and drops it when the session ends;
`handleSignedIn` holds `promptForPushIfNeverAsked` until a tour that opened by itself closes (the
prompt then runs inside that click). Where a step's control is on screen (`data-tour` on the docked
menu and its items - looked up under `.sb-dock` only -, `.tb-menu`, `.tb-action` and `.am-btn`) the
dimmed layer, an SVG path with an evenodd hole, leaves a hole and an ember ring around it: a click
in the hole ends the tour and is passed to the control under it (`elementFromPoint` once the dialog
is closed); a click elsewhere on the layer, Esc or a close the browser makes itself is Skip. Its
keydown listener stops every key (arrows step, Tab stays in the card; the explicit `role="dialog"`
is for Files' capture-phase Escape), wheel and one-finger touch moves are cancelled unless the
card's text scrolls, the focus goes back to the opener if it had keyboard focus, else the account
button, and a polite live region reads each new step. It sends nothing, so the demo needs nothing.

Tooltips on the web are `data-tip` (`components/TipLayer.tsx`, mounted once in main.tsx: shown at
once on hover and on keyboard focus, in a portal), not `title`, whose tip waits about a second. An
icon-only control also gets an `aria-label` (`data-tip` isn't read out); a text button whose tip
explains more carries the same text as `aria-description`. `title` stays only for a cut-off name's
full text (Files' rows and tiles, pickers, names), a button that is `disabled` while its tip
matters (Files' Delete over an upload-only pick: some browsers give a disabled button no mouse
events), and the map iframe's name.

Drag to select on the web (issue #200): Images, People and Files (list and grid) share
`components/useDragSelect.ts` (the gesture) and `dragSelect.ts` (the pure rules, tested by
`node web/scripts/drag-select-test.mjs`, the same rules as the phones' `DragSelect`). A mouse press
on an item that moves more than 8 px, or a touch held 450 ms then moved, adds every item from the
pressed one to the one under the pointer in display order after the selection it started from
(Images numbers them in grid order); moving back restores it, a press never unpicks, Files' `..`
is never picked, Escape takes the drag back, and the click it ends with is swallowed. Only a press
on an item itself starts one (`indexAt(el, true)`): a gap, the empty end of a short row, a month's
title, Files' header or `..`, or a face's name, count or "Add a name" (People's `faceAt`: the
picture and its circle only) start nothing, and the page scrolls. Each move
hit-tests with `elementFromPoint` (clamped into the grid and below `--topbar-h` - in Files below
the selection bar and column headings stuck under it, `stuckOver`/`stuckTop`, so the pointer over
them counts as above the window), and again on every scroll: the window scrolls in bands at the top
(from there too) and bottom (56 px, or a third of what can be
seen when that is less, none when that is under 8 px; up to 1200 px/s), but only once the pointer
has moved more than 8 px since the drag began (`DragPointer.armed`: at once for a mouse drag, for a
touch measured from where the long press fired - a long press held still in a band never
scrolls), and the wheel works mid-drag. The drag's two ends are kept by key (`relocate`, re-run
whenever the items change): when the pressed item goes the drag is over with the selection as it
stands - later moves do nothing and the release's click is still swallowed; when only the item
under the pointer goes, the selection stays as it is until the pointer is over another item; a
pick that went from the items goes from the drag's base too (`keptBase`), so the next move (or
Escape) doesn't bring it back. Something coming over the items (`enabled` false: a viewer, a
dialog, the date scrubber) or a new Files listing (a refresh, an upload finishing, another folder:
Files is disabled while it loads) ends the drag the same way (`interrupt`): the selection is what
the view makes it (the listing clears it), the edges stop scrolling, the release is no click and
Shift+click doesn't go on from it. The selection is committed with `flushSync` and the container's on-screen
move measured round it: Files' in-flow actions bar appearing at the first pick (or the browser's
scroll anchoring) must not move rows under the pointer, so the page scrolls by the difference,
adding a `min-height` to the document while something is picked when it can't. Text selection
and the picture callout are off only on the container during a touch press or a drag
(`data-dragsel`); the container keeps a non-passive `touchmove` listener (Chrome and Safari only
let a touch's moves be cancelled when one was in place as it began) that cancels only a touch
drag's moves, and the element a touch began on gets the same listener while it is held (its moves
keep going to it once a new listing or a face that went has taken it off the page). With touch, once something is picked a tap in Files picks or unpicks a row instead
of opening it. Shift+click (and Shift+Space on a checkbox) adds the run from the last pick or the
drag's end. The edge scroll waiting for a move, the band depth, starting only on an item, an
item going mid-drag and a new listing ending a drag follow the phones' rules: change all three together. Tested in headless
Chrome with CDP mouse and touch input against a mock `useWS`.

Leaving the selection on the web: Images' selection bar starts with an × (`web.images.select.clear`)
and Escape clears it; Files' actions bar (`.fb-actions`) starts with one too (`.fb-sel-clear`,
`web.files.selection.clear`, a 36 px circle in a 44 px target), and Escape clears Files' selection
unless the viewer, a pop-up, the menu's drawer, a drag (its own Escape takes the drag back), the
top bar or a text field has it. `clearSelection` unpicks everything, ends a drag under way
(`useDragSelect`'s `interrupt`) and moves the focus from the bar to the folder's contents; a tap
then opens again. Over a search's results the first Escape closes them and the next clears
the selection made in the folder underneath (its bar hidden with it), as Android's Back does.

Files' sticky strip on the web (web only: the phones have no column headings): scrolled down, the
column headings of List, a search's results and Storage (`.fb-head`, `position: sticky`) stay under
the top bar - `.fb-table` clips its rounded corners with `overflow: clip`, as `hidden` would stop
them sticking - and while something is picked the actions bar (`.fb-actions`, in the page just above
the list or grid, no longer in the toolbar) sticks between the two, alone in Grid. `FilesExplorer`
measures their heights onto `.fb-wrap` (`--fb-sel-h`, `--fb-head-h`) for the headings' `top` and the
rows' `scroll-margin` (keyboard focus; the viewer's return scrolls its row fully into sight). A
heading clicked while its list is scrolled down shows the new order from its first row
(`sortFromHeading`). Scrolled down or mid-drag, the bar coming or going doesn't move the rows (the
browser's scroll anchoring; mid-drag `useDragSelect`'s own measure); at the list's top a tick still
pushes them down by the bar's height, as before the bar stuck.

Full-size photos on the web (`components/fullSizeCache.ts`, `node web/scripts/full-size-cache-test.mjs`):
`MediaViewer` keeps every `GetFile` answer it fetched whole (photos, clips the device won't stream)
for the tab's session, so stepping back to a photo or reopening it shows full size at once with no
request - every step back and forth used to show the low-res tile while it fetched again. By content
hash (the answer's `File.hash`, else the item's; no hash, not kept), least recently used out first,
100 MB (a tenth of the apps' default 1 GB thumbnail cache, the phones' `FullPhotoCache` share), one
photo at most a quarter of it. Kept even when the viewer moved on before it came. Memory only:
sign-out reloads the page. Read-ahead: once the open photo shows at full size and the viewer stays
on it `cRapidStepMs`, the next and then the previous photo are fetched into it (never videos, nor
around one; a held arrow key reads nothing ahead); `fetchWhole` shares one request per path, so a
step onto a neighbour on its way waits for it. Only after the open one, as on the phones: started
with it, they slowed down the photo being looked at. On the demo: the next or previous photo 2-26 ms with no
badge, was 100-230 ms with it.

Copies on the web (issue #201, `components/photoCopies.ts`, `node web/scripts/photo-copies-test.mjs`,
the phones' `DeleteCopies` rules): Images' Delete (selection bar and viewer) asks about copies when
a photo's `File.copies` is above 1 - one photo: `web.images.copies.single_text`, "Delete only this
copy" / "Delete all {count} copies"; several: `several_text`, "only these" / "every copy"
(`ConfirmDialog`'s outlined `other` button) - else, and always with `copies` unset, as before.
Every `DelFile` sends the tile's (or a Files row's) `hash`, and `every_copy` only for a photo with
copies (`delFileFields`). A single refusal (`copy_upload_only`/`changed`, whatever the others did)
shows the device's words in `web.images.delete.failed_detail` ("Delete failed: {detail}", the
phones' `delete_failed`; `refusedText`, also in the viewer and Files' alerts); two or more for one
reason are counted (`web.images.delete.*`), and different reasons follow the phones' `keptText`: a
counted line for `changed`, one for `copy_upload_only`, then any other refusal in its words in the
wrapper, once each (`deleteNotice` lines, one toast line each). Refused photos stay picked;
`changed` reloads the grid from where its search began (`reloadGrid`: a jump at its month, both
cursors reset) and a page refreshes a picked photo's hash (`refreshPicked`). The viewer closes on
`changed` (toast, reload, as the phones); so does Files' viewer (an alert with the words, as
Files' selection says them, and the folder listed again).

Newer photos above a jump on the web (issue #194, `components/newerPhotos.ts`,
`node web/scripts/newer-photos-test.mjs`): after a scrubber jump (`cutoff`), `PhotoGallery` keeps an
upward cursor - `upRef` (token, end), `upMapRef` (its `have`; `mapRef` is the jump's search alone,
`dropDeleted` lowers whichever brought a photo), `upLoading`, its own `usePageRetry`. Nothing is
asked at the jump: the first scroll up near the top arms `.pg-up-mark`, however made (the phones'
rule): any scroll event that lowers `scrollY` by more than a pixel with the page's height unchanged
(the jump, photos put above and a new width change it) while the mark is within `cUpReach` (600px)
- the scrollbar dragged too (`scrollArmsUp`) - or, as the page can't scroll above its top, a wheel
up, Arrow Up/Page Up/Home/Shift+Space or a finger drawn down while it is (`nearTop`); a new search
(`searchGenRef`) drops what the old one saw. The mark then asks `upwardRequest` (`newer_than` = the
cutoff, never `before`) once within 600px of the screen.
`oldest_first` false drops the page and stops it while the page is open. A page goes above newest
first, without hashes shown (`notShown`), inside `keepTopTile`: the first tile below the bar
(`data-key`) is measured around a `flushSync` and the window scrolled by what it moved (Chrome's
anchoring leaves 0; Safari has none), and `openIdx`/`anchorRef`/`topTileRef` shift. The top month
(not the jump's) keeps its bucket's room, grey before its photos (`topReserve`, whole rows past 4
one `.pg-skel-rows` block); spinner and error float in `.pg-up`, a sticky 0-height pill rendered
only while the mark is within reach (`upNear`, its own observer), never pinned over the whole grid.

Files' Storage view on the web (issue #195, `components/folderUsage.ts`,
`node web/scripts/folder-usage-test.mjs`): the third `fb-viewmode` button, remembered in
`otc.files.viewMode`. Only there `loadUsage` asks `GetFolderUsage` - on switching to it and with
every listing while it shows (`loadList` checks `viewModeRef`) - dropping answers for another folder
(`usageAnswerFits`: the latest request's, for the folder shown). `unknown_payload`: `web.files.usage.needs_update`, not asked again
while the page is open; other errors the device's words; no answer `usage.failed` + Try again. Rows
are the answer's order (no Sort by), joined with the listing by (path, is_dir) for List's lock and
Images switch; a versions-only child is a plain folder row. Bar and share of files + versions
(`shareOf`: "under 1%", no bar at a 0 total; `row_label` on the bar), size, `..` on top; the total
and versions lines above, the note below; nothing is picked. Below 560px the bar has its own line.

Issue #182 (and #175/#176): accounts can be deleted - `DELETE /api/account/me` (`{"confirm":
"delete", "password"}`, or a sign-in within 15 minutes for Google/Apple-only accounts;
`dao.DeleteAccount` removes the account, its logins, tokens and app codes, releases its domains
with the usual 30-day hold and forgets their push rows and metrics) - and exported (`GET
/api/account/export`, JSON). A device gives its own name back with `BridgeReleaseDomain`
(authenticated by its secret, like `RotateBridgeSecret`). An unknown name is answered with
`error_code = "domain_not_registered"`; a primary device that keeps getting it for 15 minutes
(`websocket/bridge_leave.go`) leaves the bridge by itself, with an Alert. Leaving (that, or the
owner's "Leave the bridge" - `ReqDisableBridge` from the web BridgePanel and the apps' Settings >
Bridge and Account) sets the domain back to `otc` and writes `off <left|released>` to
`bridge.request`: the root runner empties `[otc] bridge-addr`, records `{"state":"off"}` (read
back as `RespBridgeAccess.left_reason`) and restarts the device local-only at
its home-network address; `RespBridgeAccess.local_address` (`localAddress()`: the source address of
the route out, with `[otc-api] port`) is what the apps switch their endpoint to (`ws://<it>/ws`),
since `.local` names don't resolve everywhere (Android) - `otc.local:8080` only when it is empty. The privacy
notice is `bridge/static/privacy.html` (`/privacy`, English in every language: legal text),
linked from the landing page's footer (`bridge/site/pages/landing.html`), the account page,
BridgePanel and the apps' sign-in and Settings; contact messages are pruned after a year.

Terms of use (issue #175): `bridge/static/terms.html` at `/terms` (operator "Off The Cloud",
info@off-the.cloud; free for two years then 9.99 € + VAT a year; the device, its data and its handling are
the owner's responsibility and Off The Cloud is not liable for data loss; Dutch law; English in
every language, like the privacy notice). It is linked from the landing page's footer, the
privacy notice, the account page and the setup wizard. Accounts
record `terms_version` + `terms_accepted_at` (migration 008; `accounts.TermsVersion`, bump it when
the text changes in substance). Email sign-ups accept with the checkbox. Google/Apple accounts
accept on the "complete your profile" page, where the OIDC callback sends them until they have
(`TermsAccepted`). Older accounts accept with `POST /api/account/accept-terms` from a prompt on
the account page. A setup code (`IssueSetupToken`: `ErrTermsNotAccepted`) and a hand-registered
name both need the terms in force.

Retention (issue #176):
- **Accounts unused for 6 months are removed.** `accounts/inactivity.go` runs daily on both nodes,
  and only when email is configured. "Unused" means no account-page sign-in (`last_seen`) and no
  client reaching any of the account's devices (`devices.last_client_at`). The job:
  - warns by email a month before (`inactivity_warned_at`, migration 009, claimed with a
    conditional update so only one node sends it);
  - unmarks a warning whose email failed, to retry it the next day - unless RCPT TO was refused
    for good (5.1.1/5.1.2/5.1.3/5.1.6/5.1.10, `mailer.AddressRefused`), which counts as warned;
  - forgets the warning once the account is used again;
  - removes accounts like "Delete my account" would.

  `TestInactivityPassMySQL` runs against the Lima VM's MySQL (`OTC_TEST_MYSQL_DSN`).
- **Server logs** are kept 30 days: journald `MaxRetentionSec` on every node,
  `bridge/cluster/journald-otc.conf`.
- **Contact messages** are deleted after a year, or from the admin panel's Messages tab.

GDPR paperwork (issue #175) lives in `private/gdpr/` (the maintainer's copy only, never
committed): `records-of-processing.md` (Art. 30, every table, purpose, legal basis, retention,
processor) and `breach-procedure.md` (72 hours to the Autoriteit Persoonsgegevens, with a
register). The App Store privacy answers are in `private/stores/app-store-privacy.md`, matching
the iOS and macOS `PrivacyInfo.xcprivacy` (name, email, APNs token, sent logs; photos and files
aren't collected because the relay keeps none of them). Play's Data safety answers are in
`private/stores/play-store.md`. Change these with the privacy notice whenever the bridge stores
something new.

Email (`bridge/mailer`, `[smtp]` in the bridge's ini: host `smtp.protonmail.ch`, port 587,
username/from `info@off-the.cloud`, `password-file=/etc/otc/smtp-token` - a Proton SMTP token,
0600 for the service's user, pushed from the Mac's Keychain item `otc-bridge-smtp`, never in the
ini or the repo): verification is mandatory - an email sign-up gets a link (`#verify=` in the URL
fragment, 48 h, only its SHA-256 in `account_email_tokens`) and `IssueSetupToken`,
`AccountForSetupToken`, manual name registration and `/api/claim` with the account page's session
cookie (403 `cConfirmEmailFirst`, or `cAcceptTermsFirst` without the current terms) refuse an
unverified account (a database error loading the account there is a 500, not "confirm your email";
`LookupSetupToken` is the error-aware form of `AccountForSetupToken`, and `IssueSetupToken` returns
database errors as they are, so `ErrEmailNotVerified` means just that); Google/Apple
accounts are verified by the provider, and linking one verifies an email account; accounts from
before migration 007 were kept verified. `?for=setup` sign-in/sign-up of an unverified account
answers `verify_email: true` (and resends the link on a sign-in); the setup wizard shows "Confirm
your email" and signs in again. Password reset: `/api/account/forgot` (same answer for any
email) mails a one-hour `#reset=` link; `/api/account/reset` sets the password, verifies the
email and ends every other session. A new link is sent before older ones are dropped
(`KeepNewestEmailToken`), and a QUIT error after an accepted DATA counts as sent.

Issue #163 (request limits, `bridge/limits`): every JSON body goes through `limits.DecodeJSON`,
and the sign-in callback's form post (Apple's) through `limits.ParseForm`
(64 KB, 15 s to arrive - the servers bound only headers, since a whole-request `ReadTimeout`
would also cut the websockets the cluster router proxies); one-off device GETs (static assets,
`/media`) are limited per address (`oneOffPerAddr`, 10/s, burst 60 - the forwarded address for a
cluster hop) and per device to `cOneOffConcurrent` (3) in flight, so they can't drain a device's
pool; `auth_events` takes one row per address and reason a minute; relays read only an
envelope's id (`envelopeID`, protowire) instead of unmarshalling every frame; store errors reach
clients as `cInternalErrorMsg`; sign-ups are limited to 5 an hour per address, sign-in answers
the same for unknown, wrong and Google/Apple-only accounts, and new bridge passwords use bcrypt
cost 12 (`limits.BcryptCost`; account hashes are upgraded at the next sign-in).
`limits.Rate` deliberately has **no hard cap** on its map (a0ce30c removed one): refusing unknown
keys would let a flood of distinct addresses (one IPv6 /48 is enough) lock out everyone new, for an
hour on the sign-up and email limiters. Its size stays bounded by request rate x refill window, and
besides the time-based sweep it sweeps once the map has doubled since the last sweep, with a 100k
floor (`cSweepKeys`). The account sign-in limiter's failures map is the same: no cap, stale
addresses swept as it doubles (10k floor), never a live lockout dropped - each failure costs a
bcrypt; and a sign-in that succeeds clears nothing (failures age out of the window), or a sign-in
to one's own account between guesses at another's would reset the count (the admin panel's still
clears: nobody can make an admin). Every per-address throttle (both sign-in limiters, sign-ups,
emails, sign-in starts, the
contact and claim cooldowns, one-off GETs, setup beacons, pairing) counts `clientaddr.Bucket`: an
IPv4 address, or an IPv6 /64, which one host can rotate through; logs, `BridgeClientInfo` and stored
rows keep the address itself. The account sign-in's, sign-up's and email 429s carry Retry-After,
as the admin login's does. Proxied device responses go out through `limits.WriteAll`
(deadline renewed
per 32 KiB: 30 s for static assets, `cMediaWriteStall` 5 min for media, since paused players stop
reading) and the cluster router's `deadlineWriter`; no `http.Server` `WriteTimeout`, because of the
websockets. Only hashed Vite assets (`/assets/<name>-<8 chars>.<ext>`) are cached `immutable`. Only
`dao.IsDuplicateKey` (1062) means 409 "taken"; other insert errors are a 500, which releases the
contact/claim cooldown (`releaseCooldown`).

### Bridge accounts (`bridge/accounts`, issue #124)

A database error in a sign-in or setup check is "try again" (500), never a refusal (issue #189):
`session` / `AccountFromRequest` return it (`RequireAuth` answers 500, a claim with a session
too; `Me` answers 500 keeping the cookie, which it clears only for an account that is gone; the
admin panel's `RequireAuth` 401s only an admin that is gone), `LookupSetupToken` tells "no such
code" from an error (claims with a setup code, and
name-available's `yours`/`online`), and `accountAddDomain` reads the account once. The setup
wizard passes the bridge's own error answers through (`bridge_answer`; `bridge_error` keeps their
`error`, `code` and `args`, and the page words known codes as `wiz.bridge.<code>`), and a typed
code is only
"not valid or has expired" when the bridge said so (a 5xx is a 502 "try again").

Every domain registered on the bridge belongs to an account (`devices.account_id`). The package
owns identity and sessions: email+password sign-up/sign-in (bcrypt, per-address throttling),
Google and Apple as plain OpenID Connect authorization-code flows in `oidc.go` (no SDK: the
provider's JWKS is fetched and cached - one fetch per provider at a time, outside the lock, and
while refetches fail the last good keys verify for up to 24 h - the id_token verified with
`golang-jwt/jwt/v5`; Apple's
client secret is an ES256 JWT signed with the team's .p8), HMAC-signed session cookies like the
admin panel's (`otc_account`, path `/`, same signing secret), and **setup tokens** - 8-character
codes from an unambiguous alphabet, 15 minutes, in `account_tokens` - which are the only link
between a wizard's claim and an account. The domains themselves are handled in `bridge/api`
next to the claim (`accountDomains`, `accountAddDomain`, `accountNewIdentity`,
`accountReleaseDomain`), since they share the name rules: `claimName` takes the token from the
body or `Authorization: Bearer`, refuses with `401 login_required` when there is none (unless
`[accounts] open-registration=true`), hands a name the same account already owns to the new
identity (`dao.ReplaceDeviceIdentity` - how a lost device is replaced), and caps an account at
`accounts.MaxDomains` (403 `domain_limit`). `websocket.go`'s `ReqBridgeRegister` no longer
registers an unknown domain on dial-in unless open registration is on. A device's additional
users with bridge access (issue #103, `pepe.off-the.cloud`) get theirs from the primary:
`ReqRegisterUserDomain` (141, `bridge/websocket/user_domains.go`), authenticated as the primary
and carrying the user's own identity, registers the address under the primary's account with
claimName's rules (the account's own name goes to the new identity, another account's or a held
name is refused, and it counts toward `accounts.MaxDomains` - the owner's choice, 2026-10-23);
a primary whose domain predates accounts registers it without one. The device asks in
`createUser` before provisioning (`websocket/user_domains.go`; a later failure gives it back),
gives it back in `deleteUser` (`BridgeReleaseDomain` with the user's identity), and at every
start of the primary asks again for every active user with bridge access
(`registerUserDomainsAtStart`, retried at 1, 5, 15 and 60 minutes): users created between #124
and release 127 were turned away until then. The account page is
`bridge/site/pages/account.html`: embedded in the binary, served by `bridge/api/site.go` at
`/account` and `/<code>/account` under the nonce CSP, plain JS over `/api/account/*` whose texts
come as JSON blocks (`site.account.*`, and `api.error.*` for its error table by code) built by the
`jstexts`/`jsform` template functions (`bridge/site/site.go`), filled in as text nodes only
(docs/i18n.md "bridge"). Its links (the emails' `#verify=`/`#reset=`, the sign-ins' `?error=`
and `?complete=1`, BridgePanel's `?delete=1`) all land on `/account`, except that the emails
(verification, reset and the inactivity warning) link `/<code>/account` when the account's
`accounts.lang` is a carried language other than English (`accountPage`/`pageLink` in
`bridge/accounts/emails.go`; English, `""` and anything else keep `/account`, byte for byte). The wizard's account step
goes through the device (`/api/account` in `setup_wizard.py` proxies sign-in/sign-up with
`?for=setup`, which returns a setup token; a typed code is checked with
`/api/account/setup-token-info`) because the hotspot's captive DNS only lets the device reach
the bridge - so Google/Apple users get a setup code from the account page on another device.
Inside the apps' Bluetooth setup the phone has its own internet, so the
account step also offers "Continue with Apple/Google" (the wizard's `/api/providers`): the app
runs `/account/auth/<p>/start?return=otcsetup://done&challenge=…` in the system sign-in sheet
(`ASWebAuthenticationSession` / a Custom Tab + `SetupSignInCallbackActivity`) and, PKCE-style,
the redirect carries only a one-time code that `POST /api/account/app-exchange` trades with the
app's verifier for a setup token (`bridge/accounts/appsignin.go`) - kept in the database (`app_signin_codes`, by SHA-256, single use), never one node's memory: the callback and the exchange can reach different bridge nodes (#144) - a custom scheme can be
claimed by any Android app. "Continue without an account" sets `skip_bridge`, and `install.sh` gets `OTC_BRIDGE_ADDR=""`
(local-only, name `otc`). Schema: `bridge/db/db.sql` + `bridge/db/migrations/001-accounts.sql`
for an existing bridge (no updater on the bridge: run it by hand, it is idempotent). Issue #139: the admin
panel's Devices tab shows each device's owner, whether it is dialled in (`Admin.IsOnline`, set
from the websocket manager in main), when a client last reached it (`devices.last_client_at`,
written from Go in UTC at most once a minute per domain by `RecordDeviceActivity`; migration
`002-last-client.sql`) and relayed traffic over 1 h / 24 h / 30 days from `device_metrics`; an
Accounts tab lists accounts and opens one with its devices (`/admin/api/accounts[/{id}]`). Test bed: the
Lima VM `otc` has MariaDB with `bridge/db/db.sql` and the migrations loaded, and a test bridge
in `~/btest` (not `/tmp`, which the VM loses on every restart): `etc/otc_test.ini` with
`tld=bridge.test:8081` and `static=$HOME/btest/static/` (the trailing slash matters), a
self-signed cert, and `run.sh` to (re)start it; copy a linux/arm64 build of `otc_bridge` and
`bridge/static` in, the admin login is `admin` / `test-admin-pw`. curl with `-H "Host:
bridge.test:8081"` (Lima forwards the port); Chrome needs `--host-resolver-rules="MAP
bridge.test 127.0.0.1"`. Issue #143: every admin list (`/admin/api/devices`, `accounts`,
`auth-events`, `contact-requests`) takes `q`, `page`, `size` (25 by default, 200 at most) and
answers `{Items, Total, Page, Size}` (+ `Unread` for messages); searches escape LIKE's
wildcards (`likeArg`), and `/admin/api/domains` feeds the device pickers.

Sessions and sign-in: only a real sign-in (sign-up, login, provider callback, reset, password
change) issues the session cookie, whose issue time the 15-minute `cFreshSignIn` checks trust. A
password change or reset is one UPDATE that also bumps `session_epoch`
(`dao.SetAccountPasswordEndingSessions`, `LAST_INSERT_ID(session_epoch+1)`). A Google/Apple
sign-in's state is tied to the browser by `__Host-otc_oauth_<state[:16]>` (SameSite=None) plus a
`_l` twin without SameSite (Safari 12); a 48-hex state from an older build (`isPreCookieState`)
passes without. `GET /account/auth/{provider}/start` answers 400 unless it is a top-level
navigation: open it as a page, never fetch or frame it. `GET /api/account/continue` needs
`Sec-Fetch-Site: same-origin` (or an https Referer on the bridge's host). The bridge DSN has
`timeout=5s` and read/write timeouts of 60 s (above InnoDB's 50 s lock wait); relay-path queries run under a 5 s
context (`cRelayDBTimeout`) - follow that for anything new there. A `device_metrics` write that
timed out is dropped, not retried.

### Desktop sync client (`app/desktop`, issues #119 and #120)

`otc-sync` is the Windows and Linux counterpart of the macOS menu bar app, written in Go inside
this module (it shares `proto/generated`), pure Go so `make desktop` cross-compiles all four
binaries into `dist/` from any machine (version `dev`: development builds never update
themselves). Publishing is `scripts/desktop-release.sh [--patch] "notes"` (or `NOTES=… make
desktop-publish`): it bumps `app/desktop/VERSION`, builds the four binaries with it, writes
`desktop.json` (version, notes, per `GOOS-GOARCH` the download URL and SHA-256), signs it with the
release key (`scripts/sign-file.sh`, the same key and Keychain passphrase as `release.sh`) into
`desktop.json.sig`, uploads everything to the rolling GitHub release `desktop` (binaries first,
manifest last), checks what was published and commits the version. The website's Downloads section
(`/#download`, in `bridge/site/pages/landing.html`; it highlights the visitor's OS) links to
those stable URLs.
Self-update (`internal/selfupdate`): the tray checks 20 s after start and every 6 h, verifies the
manifest with the embedded release key, and shows "⬆ Update otc-sync to X"; a click downloads the
platform's file next to the executable, checks its SHA-256 and swaps it in (Windows renames the
running exe to `.old`, removed at the next start), restarts the systemd user service if it runs,
and relaunches. `otc-sync update` does the same from the command line. Linux installs go to
`~/.local/bin` so the user can replace the binary; a root-owned one asks for `sudo otc-sync update`.
The Mac app is distributed only through the Mac App Store (which updates it); its card on the
Downloads section links to the listing (`apps.apple.com/app/id6814658734`, live since 2026-10-06). The Android card links to Google Play (`play.google.com/store/apps/details?id=cloud.offthe.otc`, live since 2026-10-10); the iPhone card links to the same App Store listing as the Mac (one app record for both, live on iPhone since 2026-10-21).
**Connect / Disconnect.** Both desktop apps show the device and password fields only while
nothing is configured. That form is open from the start: the Mac's inline Settings, and the tray's
dialog at launch. Once connected they show the device and a Disconnect button: Mac Settings >
"Disconnect…" (plus Retry while not connected), the tray's "Disconnect from <device>…", and
`otc-sync disconnect [--yes]`. Disconnecting asks first, then removes every folder from the app and
clears the address and password (`SyncModel.disconnect` / config with no folders and no domain).
The files stay where they are. This is because folders kept across a change of device would sync
with, or delete on, the other device. The Mac wizard's "Sync this Mac with my device" asks the same
when another device is set (`SetupWizardView.switchDevice`), and so do the tray's Connect dialog
and the Mac's connect form when another device is entered while folders are synced - also with the
old password gone - then disconnect first; `otc-sync settings` refuses it and points to `otc-sync
disconnect`. A respelling of the same address is no change (`engine.SameDevice` /
`SyncPaths.sameDevice`). "Change Password…" (the tray, Mac Settings' ⋯ menu, `otc-sync settings
--password-prompt`) keeps every folder; "Wrong password" in the tray's status line and the popover
leads to it. The tray's Connect and Change Password wait up to 30 s and say Connected, turned down
(Try Again) or unreachable (`waitForConnection`). A ws:// address for a device not on the home
network (`config.Unencrypted` / `SyncPaths.isUnencryptedRemote`: not loopback, private,
link-local, `.local`/`.lan`/`.home.arpa`/`.internal` or single-label) asks before it is used; the
CLI wants `--allow-unencrypted`. Removing a folder asks first ("its files stay"). A folder is
synced once: one equal to, inside or holding a synced folder, here or on the device (ignoring case
on macOS and Windows), is refused with why (`config.FolderOverlap` / `SyncModel.admit`); on the
Mac, a folder waiting for its disk picked again is linked to it again, not added twice. "Set Up a New Device…" is a small
button (Disconnect's size) at the right of "Start at login" in the Mac's Settings (the Language
picker is the row below, last, at the left), and its own section above Quit in the tray.
**Web app (issue #193).** Once a device is configured both offer "Open Web App": the Mac in the
popover's header beside the gear (where the header doesn't fit, its heading drops "— Sync" before
the button keeps only its globe), the tray as the last line of its top group, and `otc-sync open`
(prints the address, and opens it where there is a display). It is the configured address's host
and port at `/` - https for a bridge name or a wss address, http for ws (`SettingsStore.webAppURL`
/ `config.WebURL`) - opened in the default browser (`NSWorkspace.open`; otc-sync's
`internal/browser`, http/https only: ShellExecute on Windows, xdg-open on Linux). Never the
password or a token (the web app asks for the password itself), and never the home-network route,
whose certificate a browser can't pin.
**Start at login, only with consent.** The Mac App Store rejected macOS 2.0 (17) under guideline
2.4.5(iii): 1.0 registered itself as a login item the first time it ran and re-registered at every
launch. Now neither client registers anything until the user says yes. The Mac's
`LoginItemSettings` (`LoginItem.swift`, `SMAppService.mainApp` behind `LoginItemService`, faked in
`LoginItemTests`) records the answer as `startAtLoginChoice` (absent = never asked). Only a true
lets `launch()` re-register an item the system dropped, and never one the user switched off in
System Settings (`requiresApproval`: left alone, the checkbox shows it off, Settings says so with
"Open Login Items…", ticking the box opens Login Items instead of registering). 1.0's
`startAtLogin` key was written by the app itself, so it isn't consent: true means unregister
whatever is there (the key stays until that works) and ask; false was the user unticking it, kept
as a no. The question is asked once, inline (never an `.alert`, which closes the popover):
`LoginItemOffer`, "Start Off The Cloud when you log in?" / "Start at Login" / "Not Now", shown
below the folder list and above Add Folder/Quit once a device has connected this launch
(`SyncModel`'s onConnect calls `deviceConnected()`), hidden while Settings is open, and also on
the setup wizard's "Your device is ready" page after "Sync this Mac with my device". Either answer,
or Settings' "Start at login" checkbox (`StartAtLoginToggle`, the real `SMAppService` state,
re-read when the popover opens), records the choice. otc-sync does the same with config.json's
`autostart` (nil = never asked; `autostart.AtLaunch` restores a consented entry and registers
nothing else; `autostart.Apply`; `autostart.Backend` is faked in the tests): the tray asks once
after the first successful connection (`tray/autostart.go`, a zenity question with the same
words; closing it counts as Not Now; with no dialog program nothing is recorded and it asks at the
next start), and not in a viewer tray next to the service (`OfferAutostart`); the menu's
"Start at login" checkbox and `otc-sync autostart on|off|status` record an answer too. Unlike the
Mac, an entry an older otc-sync made unasked is kept until the user answers (there is no store
rule on Windows/Linux, and silently dropping it would stop the sync after the next reboot for
someone relying on it); "Not Now" removes it. The systemd user service (`otc-sync service
install`) is its own explicit choice and isn't affected. Never run the Debug Mac app to test this:
it shares the bundle id, settings and login item with the owner's installed app.
**Adding a folder (two steps) and a folder's requests (issues #192 and #132).** Both apps add a
folder in two steps: first the kind (backup, two-way from this computer, two-way from the device,
each with its (i)), then that kind's options, and only then the folder picker. A backup offers
"Keep out of Images" plus a fixed, already-on "Upload only" line (a backup always is,
`markUploadOnly`); a two-way folder from this computer offers "Upload only" and "Keep out of
Images"; a folder from the device offers nothing (it keeps what is set for it there) and goes
straight to the device's folders. The Mac's `AddFolderChooser` is one inline view in the popover
(never alerts, which close it) with Back and Cancel and a "Choose Folder…" button that opens the
`NSOpenPanel`; options start unticked for each kind, and one the device can't do is disabled with
its "needs an update" caption. The tray has one "Add Folder…" item (`tray/addfolder.go`): a zenity
list of the kinds, then the options - one checklist on Linux (zenity `--checklist`, with Back),
one question per option on Windows, whose stock list dialog has no checkboxes (only Ctrl+click
multi-select): Yes / No / Cancel (MB_YESNOCANCEL; No is zenity's extra button, so Cancel, Esc and
closing the dialog stop the whole flow instead of meaning No; there is no Back) - skipped when the
kind has none or the device can't do them, then the folder chooser. "What Do These Do?" explains the kinds and the options. The CLI takes
`--keep-out-of-images` on `backup` and `add`, `--upload-only` on `add` (`backup --upload-only`
just says a backup always is); `add-remote` refuses both with an error naming where to change
them. The words are the same in both apps ("this Mac" / "this computer"): `UploadOnlyText` /
`engine.UploadOnly*`, `OutOfImagesText` / `engine.OutOfImages*`, the kinds' titles and (i)s.
Upload only's (i) says what it means for a two-way folder in one sentence: "Files you delete on
this computer stay on the device and aren't downloaded again; files added or changed on the
device still come down.", then that turning it off later brings back the files deleted here
while it was on. Each option only records a request with the folder (`config.Folder` /
`RemoteFolder` `.OutOfImages` and `.UploadOnly`, `StoredFolder.outOfImages`,
`StoredRemoteFolder.outOfImages` / `.uploadOnly`; nil = nothing to send, absent from older data;
a request an older version stored for a folder from the device is still sent once). Both kinds
share one machinery - otc-sync's `engine/requests.go` (`requestKind`, `requestState`,
`applyRequest`, `config.Request`/`SetRequest`/`ClearRequest`), the Mac's `RequestKind` (key paths
to each kind's state) and `sendRequest`. A request is sent for the folder's device path - at the
start of its passes (upload only first, before the listing; never holding the pass up), at
connect, and at once when config.json brings one or the row makes one - until acknowledged, then
cleared compare-and-clear (otc-sync in config.json first, retried like its reads, then in memory)
and never sent again: unlike `markUploadOnly`, re-sending at every start would undo a change made
since from the web or a phone. If config.json can't be written, the engine keeps the value in
memory marked answered (`requestState.acked`): no pass or reload sends it, the clear is retried at
the next pass and config.json reload, and only a different value (or a later request once the
clear is written) goes. One request per folder and kind at a time; a pass waits for one under way,
and a change made while a request is under way goes right after it, even when that request failed
(otc-sync releases the folder in the same lock hold as its last look at the request).
`unknown_payload` keeps it pending and marks the device unable for that kind until the next
connect (the option is disabled with "Your device needs an update to …", the tray doesn't offer
it, and a pending folder says so). The kind's by-parent code (`out_of_images_by_parent`,
`locked_by_parent` - only lifting upload only can get it, which no app asks) drops it and shows
the device's message on the folder; any other failure leaves it for the next pass. A pending
upload-only request shows as a lock on the Mac's row (`UploadOnlyBadge`, tooltip "Making it upload
only…", orange for a device that can't or a refusal) and " · Making it upload only…" in the tray's
folder title (the refusal on a line of its submenu); nothing once acknowledged. state.json has
`FolderStatus.UploadOnly` (`engine.UploadOnlyState`: making, lifting, unsupported) and
`UploadOnlyNote`, `State.UploadOnlyUnsupported`; `otc-sync folders` prints it.
**Keep out of Images (issue #192)** in particular: the add flow's caption says that tags and faces
already found are deleted (`engine.OutOfImagesAddCaption` / `OutOfImagesText.addCaption`): the
folder may already be on the device. Later, per folder - every folder, from the device too: the
eye button on the Mac's row (an inline confirmation, not an alert; the row has no text line about
Images - the owner found it crowded - so the eye's tooltip carries the state, a request on its way
and the device's refusal), the "Kept Out of Images" checkbox in the tray's folder submenu, and
`otc-sync images <id|path> keep-out|show`. `unknown_payload` is a device before release 108.
`out_of_images_by_parent` shows the device's message while a folder above still keeps it out (the
next list that says otherwise drops the message). `otc-sync images <id> keep-out` on a folder kept
out by a folder above says it already is (exit 0); `show` there is refused. What the device keeps
out comes from `ListOutOfImages` at connect, after each ack and with the one-minute poll,
forgotten when the device or password changes; a folder's state (`SyncPaths.outOfImagesState` /
`engine.OutOfImagesState`: shown, kept out, kept out by a folder above - the control disabled -,
keeping/showing while pending, unsupported, unknown = nothing shown) compares paths byte for byte.
`engine.migrateFolders` carries both requests. otc-sync's state.json has it per folder
(`FolderStatus.OutOfImages`, `OutOfImagesBy`, `OutOfImagesNote`) and `otc-sync folders` prints it.
**Upload only in a two-way folder (issue #132).** The device refuses a delete under an
upload-only folder (`upload_only`). Until 2026-10 the clients logged it and dropped the path from
the sync record, so the next pass took the file for new on the device and downloaded it back. Now
the refusal is recorded: the record entry becomes `kept-on-device:<device hash>`
(`keptOnDevice` / `SyncPaths.keptOnDevice`), which stands for two baselines -
absent here, that hash on the device (`recordBaselines`). While neither side changes the file
nothing happens (not downloaded again, the delete not sent again, restarts included); a new
version on the device comes down; a file put back here goes up (while the folder is upload only
the device keeps the old one as a version); both at once are a conflict like any other (a copy is
kept); gone from the device too, the entry goes. Upload only lifted later (from the web or a
phone): the listing's per-file `upload_only` (File field 9) is false for a kept entry, which then
has no baseline at all (`baselinesFor` / `SyncPaths.baselines`), so the file is downloaded back
here and the record is a plain entry again - the old delete is never sent, so nothing the lock
protected is deleted (owner's decision; the add flow's (i) says files deleted while it was on come
back once it is turned off). The delete is still sent once per file (the device decides, not the
listing's flag). While a folder's own Upload only request is pending and not acknowledged (a
failure, or a device that answers `unknown_payload`), its passes plan no device deletes at all
(`holdDeletes`, from `uploadOnlyAwaited` / `uploadOnlyPending[id] == true`): a file deleted here
keeps its baseline, nothing is downloaded back, and the delete goes once the flag is set - where
the device refuses it and it becomes kept-on-device. otc-sync logs the held count when it changes
(`noteHeldDeletes`); the Mac counts it in each pass's summary line. The mass-deletion guard is
unchanged (kept entries never become local deletes). An older client reading such a record sees a
hash that matches neither side and downloads the file, as before.

**Setting up a new device from a computer (issue #184).** Both desktop apps have "Set Up a New
Device…". They download `off-the-cloud-rpi-lite-arm64.img.xz` from the `image` release and trust it
only through the release-key signature over its SHA-256 (`.sha256.sig`, signed by `make
image-publish` via `scripts/sign-file.sh`). otc-sync (`internal/flasher`, tray `setup.go`, CLI
`otc-sync flash [disk]`) writes the card itself. It lists removable disks only:
- on Linux, sysfs `removable`, USB or `mmcblk`, minus any disk holding `/`, `/boot`, swap and so
  on, followed through device-mapper slaves;
- on Windows, `Get-Disk` with bus type USB, SD or MMC, not boot or system.

The tray's list of cards shows Linux's full labels; Windows' list is 241 px wide and cuts what
doesn't fit, so there a row is the size, the disk's number and the name, at most 34 characters
(`windowsCardRow`; the device dialog's prompt there is the short `desk.main.settings_hint_windows`).
It needs 8 GB or more. Writing runs elevated as the hidden `otc-sync flash-device` (pkexec, or UAC
through `ShellExecuteExW runas`), which reports progress as JSON lines in a status file the
wizard polls. The elevated process re-checks that the disk is removable, fetches the signed hash
itself and hashes the image before and while writing. It prepares the card: Linux unmounts it
and opens it `O_EXCL`; Windows runs diskpart `clean` and opens the drive `NO_BUFFERING` with
page-aligned buffers. It blanks the first MiB, streams the xz (ulikunitz/xz), writes the first
chunk last so nothing mounts a half-written card, then syncs. On Linux it drops the cache
(`BLKFLSBUF`) and reads everything back against the decompressed hash before ejecting
(udisksctl power-off). The download is deleted after a successful write.

The Mac app (`SetupWizardView.swift`) is sandboxed and can't write raw disks. It downloads into
`~/Downloads` (`ENABLE_FILE_ACCESS_DOWNLOADS_FOLDER`) with the same checks, opens Raspberry Pi
Imager with the image (bundle `com.raspberrypi.rpi-imager`; it also selects the file in Finder for
"Use custom"), deletes the image after "I've Written the Card", and can continue over Bluetooth:
`BluetoothSetupView.swift` is the iOS file with an AppKit view layer, and finishing there points
the Mac's sync at the new device. otc-sync continues on the phone or through the hotspot
(`http://10.42.0.1/`), the parity exception the owner chose. Tested by flashing a loop device in
the Lima VM (`OTC_FLASH_TEST_DISK`, honoured only for root) and a virtual USB disk in the Windows
VM (`run.sh -drive file=card.raw,if=none,id=card,format=raw -device
usb-storage,drive=card,removable=on`). Its packages are one-to-one with the Swift files: `wsclient` =
WSClient.swift + PwCrypto.swift, `engine` = SyncModel.swift (upload folders with a watcher and a
10-minute reconcile, two-way remote folders with the three-way merge and a 1-minute poll,
hash-first uploads, RAID polling), `engine/watcher.go` = FolderWatcher.swift (fsnotify, one watch
per directory, added as directories appear; 4 KiB buffers on Windows, an overflow checks the
whole folder; a watch limit reached leaves the rest watched), `tray` = PopoverView.swift (fyne.io/systray menu,
the OS's own dialogs through ncruces/zenity - no GUI toolkit, no CGO), `config` = SettingsStore
+ the bookmarks (config.json, keyring or a 0600 `secret` file, state.json), `autostart` = the
registration half of LoginItem.swift (XDG autostart file / HKCU Run key, only with consent - see
"Start at login" above), `service` (systemd user unit + linger). One process runs the
engine (a flock in the config dir); a tray started next to the service is a viewer, and every
edit goes through config.json, which the engine watches - so the CLI, the tray and the service
never disagree. Every edit takes `config.lock` (`config.Edit`; flock, LockFileEx on Windows) and is
written through a temp file of its own, fsynced and renamed (`WriteFileAtomic`, also state.json,
records, caches); the file replaced is kept as `config.json.bak`, which `Load` reads when
config.json isn't valid. One that can't be read starts the tray with "The settings can't be read"
and "Reset Settings…" (`config.Reset` renames it aside). The tray and the service log to `<config
dir>/otc-sync.log` (5 MiB, one `.1` kept; the tray's "Show Log" - the Mac logs to the unified
log instead, `log show --predicate 'subsystem == "cloud.off-the.OffTheCloud"'`); on Windows the CLI subcommands
attach to the terminal's console (`attachConsole`). A keyring still locked at boot shows "Waiting
for the password" and is asked again every 30 s; `otc-sync service install [--password-file]`
offers to keep the password in the 0600 `secret` file instead (`Config.PasswordInFile`). Remote paths are `/linux/<host>/…` and `/windows/<host>/C/…`, like `/mac/<host>`.
Any behaviour change in the macOS app must be mirrored here (and vice versa), the same rule as
iOS/Android. There are three kinds of folder, each explained in the app (the Mac's
`AddFolderChooser` with an (i) per kind and option; the tray's "What Do These Do?"):
**backup** (one way and upload only, `TrackedFolder` / `config.Folder{OneWay: true}`, `otc-sync
backup`: new and changed files go up, nothing is ever deleted on the device, and nothing on the
device ever changes the folder. Its device folder is made upload only at every start,
`markUploadOnly`, so the device keeps older versions and refuses deletes), and two
**two-way** kinds - from the computer or from the device - which only differ in their first pass
(what is only on one side is copied to the other; with no sync record yet nothing is ever
deleted). Both directions have a mass-deletion guard (more than 20 files and a quarter of the
folder in one pass): a two-way folder restores what the device lost instead of deleting it
locally, a backup keeps on the device what vanished locally; the Mac moves what it deletes to
the Trash. Upload-only folders from before backups were a choice were migrated to two-way once
(`SyncModel.migrateLocalFolders`, gated by `sync.folders.migratedToTwoWay`; `engine.migrateFolders`
skips `OneWay`), and the sync record (relative path -> hash after the last pass) is saved per folder
(`synced/<id>.json`), so a delete made while the app was closed still propagates. Issue #134: both send a file's own creation and modification times with
`UploadFile`/`LinkFile` (`created`, `modified`; the device keeps them as the row's dates instead
of the upload time) and set them back on a downloaded file (`SyncModel.download` /
`engine.download` via `times_*.go` - creation time only where the platform can set one:
macOS and Windows; Linux has no birth time to read or set, so there `created` is the mtime).
The two-way conflict rule compares local mtime with the device's `modified`, which is why a
downloaded file must carry the device's time and not "now". Both keep their local hash cache on
disk too (`Application Support/OffTheCloud/hashes/<folder id>.json` on macOS, `<config
dir>/hashes/<folder id>.json` for otc-sync; path -> size, mtime, hash), so a relaunch doesn't
re-read every file - the reference two-way folder is 66 GB - just to confirm nothing changed;
the checking pass shows "Checking i/N · name" while it runs (#138). Next to the storage health both show how full the device is and its load: on the Mac "Storage healthy · 15% used" (`StorageStatusView`: one text that wraps after the dot rather than cut, for a longer health or language) with storage, CPU and memory in a pop-up on hover (`DeviceLoadView`); in otc-sync a text bar in the storage line (`storageTitle`) whose submenu, opened on hover, holds the same three, also its tooltip (`config.State.StorageUsed`/`CPUPercent`/`MemUsed`, from the same status poll). Storage comes first, used of total ("39.6 of 474 GB", `storageUseTitle`), and only once the device reports a size; on the Mac its bar turns orange past 75% and red past 90%, like the line (`fullnessColor`). Storage and memory share one number format (`sizeText`/`usedOfTotal` in both apps): one decimal under 100 GB, whole GB from 100 GB, TB with one decimal from 1000 GB ("1.2 of 3.6 TB"; "39.6 GB of 3.6 TB" when the units differ). Conflicts in a two-way folder (both sides changed a file since the last sync) still go to the newer
modification, but the losing version is kept next to it as `name (conflict from <computer>
<yyyy-mm-dd hh.mm>).ext` (this computer's version) or `name (conflict <date>).ext` (the device's),
and then syncs like any new file. The steps are ordered so a failure loses nothing: rename, then
download (otc-sync `actDownloadKeepLocal`); or download the other version to the copy, then
upload (`actUploadKeepRemote`). On the Mac these are `.downloadKeepLocal` / `.uploadKeepRemote`
and `SyncModel.conflictURL`. Before this, the older edit was silently lost. In a two-way pass an entry the device lists without a hash (content lost, #141) is uploaded from the local copy and never downloaded - taken for a device-side change, it failed on every pass and flooded Alerts. A folder at rest that starts a pass keeps showing it is synced for 1.5 s (`setState` in both, `quietPassDelay`), so a pass that sends one changed file doesn't flash "99%". The status reports storage for the storage path's disk (the OS disk when there is none) in units of 1.024 MB. Test on Linux with the Lima VM `otc` (`limactl shell otc`, `limactl copy
dist/otc-sync-linux-arm64 otc:/tmp/`); the tray needs a real desktop - the Lima VM is headless, so the
Linux tray is tested on a real machine. Windows is tested in the QEMU VM under `~/VMs/win11`
(`./run.sh` starts it: Windows 11 ARM64, user `otc`, SSH on `127.0.0.1:2222` with the Mac's key,
VNC on `127.0.0.1:5905`; `./qmp.py screenshot|type|combo|click` drives the screen through QMP,
`scp -P 2222` copies builds in; the tray app has to be launched on the desktop, e.g. through
`qmp.py combo meta_l+r` + `qmp.py type`, since an SSH session has no desktop). It was installed
unattended from Microsoft's Enterprise Evaluation ISO (`unattend/autounattend.xml`), whose
licence has expired - Windows shows a watermark and may shut the VM down periodically; if that
gets in the way, rebuild it from a retail ISO (CrystalFetch) with the same answer file.

**Sync invariants** (otc-sync `engine`, the Mac's `SyncModel` + `SyncPaths.swift`). A two-way
device entry is used only under the folder's prefix and through `safeRelative` (no empty, `.` or
`..` component, no NUL; the Mac checks by UTF-8 bytes, since a combining mark after `/` hides it from
a Character split). What is never synced, either way, is one list (`engine/skip.go` /
`SyncPaths.isExcludedFromSync`): hidden paths (a `.` component; also Finder-hidden on the Mac, the
Hidden and System attributes on Windows), `.otc-part` files, and Office's and Explorer's scratch
files (`~$*`, desktop.ini, Thumbs.db, ehthumbs.db, ehthumbs_vista.db, any case). Paths are keyed
in NFC (`pathKey` / `SyncPaths.key`, records too): what the device has is addressed by its own
bytes, a new file goes up in NFC, so an NFD path the Mac sent before is never renamed or deleted
for it, and of two device entries differing only in normalization the NFC one is synced, the
other left alone. Two-way actions run local deletes first and re-check the local file's size and
mtime just before acting (`stillAsScanned` / `localMatches`), and the device's file right before
an upload replaces it (before LinkFile and FinishUpload) or a delete removes it, by listing that
one path (`deviceStillAsListed`): changed since the listing, it is left for the next pass, which
keeps both versions. A sync record (`synced/<id>.json`: `{"v": 2, "device": {"domain", "pin"},
"files"}`) names the device it was made with - its home-network certificate pin when known, else
the address (`recordDevice`); one of another device counts as none (that pass deletes nothing), an
older bare map as the configured device's. The mass-deletion guard doesn't count deletes whose
content the device lists at a new path (a move there). A folder whose own folder is missing waits
("Folder not found - reconnect the drive"), untouched on both sides, and resumes once it is back
(the Mac resolves its bookmark again and follows a move; one that didn't resolve at launch stays
listed); it is never removed. Downloads go to a temp name of their own (`name.<random>.otc-part`),
synced before the rename. Two folders' passes run at a time (`passSlot`; the loops start at once),
the others say "Waiting for <folder>". A file that changes while it is sent is a busy file, left
for 2 minutes, doubling up to an hour. The hash cache also checks the change time (not on
Windows, which has none without a handle per file) and is saved every 5 minutes during a pass and
at Stop / quit. Both WSClients ping every 20 s and drop a connection silent for 45 s (a send under
way has its own deadline); small requests wait 2 minutes, listings and transfers 10; otc-sync
takes a clock jump between its 5 s network checks for a wake. The device has no empty folders, so the folders a pass's local deletions emptied
are removed after it (`removeEmptiedDirs` / `SyncPaths.removeEmptiedFolders`): up to, never including,
the synced folder, only while they hold nothing but a `.DS_Store`, with rmdir semantics. An unreadable directory is never a deleted one: paths under it keep their baseline
and get no action (otc-sync reports "N file(s) could not be read"). Backups run one pass per
folder, watched changes one upload at a time per folder (`drainChanges`), and a pass stops when its
folder is removed or the device changes. A backup's `SetUploadOnly` is re-sent until acknowledged,
and that is forgotten only when the device or password changes. A two-way record entry
`kept-on-device:<hash>` (a delete the device refused) is never a plain hash: read it only through
`baselinesFor` / `SyncPaths.baselines` (which also drop it once the folder is no longer upload
only). A two-way folder whose Upload only request isn't acknowledged sends no device deletes. A sync record counts as saved only after a
successful write. In both WSClients a superseded connection attempt's outcome is dropped, and a
rejected password is reported before the socket closes. `otc-sync flash-device` refuses a disk
whose `--size`/`--name` changed. The Mac's unit tests are hosted by the real app (`TEST_HOST`), so
`xcodebuild test` launches it with the saved settings and may reach the live device;
`SyncPathsTests` can run in a scratch SwiftPM package holding only `SyncPaths.swift` (module
`OffTheCloud`), and `LoginItemTests` likewise with only `LoginItem.swift` (a fake login item and a
throwaway defaults suite).

### Native apps (`app/ios`, `app/macos`, `app/android`)

Swift/Xcode projects (`OffTheCloud.xcodeproj` in each) that consume the same generated Swift protobuf
messages (`make pb` copies `messages.pb.swift` into both) to talk to a device or the bridge over the
same `/ws` protocol.

`app/android` (issue #88) is the Android app: Kotlin + Jetpack Compose, a 1:1 port of the iOS app
kept in its own directory so the two are worked on independently - port screen by screen, keeping
the iOS file/type names (SwiftUI view ↔ Composable, ObservableObject ↔ ViewModel, Keychain ↔
EncryptedSharedPreferences, PhotoKit ↔ MediaStore, BGTaskScheduler ↔ WorkManager, APNs ↔ FCM).
Android's background sync (`sync/SyncScheduler.kt`) has more than iOS can: besides the 15-minute
periodic run, a MediaStore content trigger (images and videos, 5 s update delay, 1 min max) fires
`MediaWorker`, which arms the next trigger (APPEND_OR_REPLACE behind itself, never REPLACE: that
cancels the running worker) and queues a one-time `SoonWorker` sync (network required; it retries
while another sync runs in the process). Armed by `scheduleNext()` on every backgrounding,
re-armed by WorkManager after a reboot, cancelled at Log Out.
`make pb` writes its protobuf bindings (`--java_out=lite` + `--kotlin_out=lite`, package
`cloud.offthe.otc.proto`, one file per type) to `app/android/app/src/main/proto-gen/`, which is
gitignored like `proto/generated`. The root `.gitignore` ignores the device binary as `/otc` - anchored, because the
unanchored `otc` it used to be also swallowed the `cloud/offthe/otc/` package directory and the
first Android commit went out with no Kotlin at all; check `git ls-files app/android | grep .kt`
after adding sources. Because those generators emit one file per type, no two proto
type names may differ only by case (the message `FriendshipStatusReply` was renamed for exactly
that: it collided with the enum `FriendShipStatus` on macOS's case-insensitive filesystem). The
protobuf runtime version in `app/build.gradle.kts` must match the installed `protoc` (4.<protoc
minor>, e.g. protoc 36.2 ↔ `protobuf-kotlin-lite:4.36.2`). Build with `cd app/android && ./gradlew
assembleDebug` (the wrapper pins Gradle 9.1; the Homebrew `gradle` is too new for AGP 8.13 and is
only used to generate the wrapper). Toolchain on the Mac: `openjdk@21`, `android-commandlinetools`
+ `sdkmanager` packages (platform 36, build-tools 36, emulator, `system-images;android-36;
google_apis;arm64-v8a`), Android Studio; `JAVA_HOME`/`ANDROID_HOME` are set in `~/.zprofile` and
`app/android/local.properties` points at the SDK. An emulator named `otc_pixel` (Pixel 8, API 36)
exists: `emulator -avd otc_pixel`, then `adb install -r app/build/outputs/apk/debug/app-debug.apk`
(adb on macOS tends to flip to "offline" right after a large install - retry in a loop rather than
assuming the install failed). For a real phone over USB, the SDK's adb 37.0.1 crashes on macOS 27
(`libunwind: stepWithCompactEncoding` in `usb_osx.cpp`) the moment a phone is plugged in; the
35.0.2 platform-tools from Google's archive (`platform-tools_r35.0.2-darwin.zip`) work - keep them
outside the SDK dir so `sdkmanager` doesn't overwrite them, and kill the 37 server first. The test
phone is a Motorola moto g06 (Android 15, serial ZY32MBZVJ4). Every iOS screen has an Android counterpart under
`app/src/main/java/cloud/offthe/otc/` (`ui/` mirrors the SwiftUI views, `net/` the connection,
`data/` the stores, `sync/` PhotoSync/SyncScheduler/AssetSyncCache); still missing versus iOS:
push notifications (FCM). Leaving a device from the "isn't available" card (its ✕) or "can't connect" card ("Set up or
connect a different device") wipes the phone's state the same way Log Out does. It keeps the
device's address and password as the *last device* (Keychain / EncryptedSharedPreferences
`last_endpoint`/`last_password`; `AppLogOut.run(keepDevice:)` / `logOut(keepDevice =)`), so
Onboarding fills them in: a device that is down is usually the one the phone comes back to.
Settings' Log Out forgets them too. iOS Keychain items are `AfterFirstUnlockThisDeviceOnly`:
items readable only while unlocked used to make a background launch on a locked phone (the photo
sync) read an empty address and show the connection screen. Older items are rewritten once
(`keychainAfterFirstUnlock`), and the device id is never replaced because it couldn't be read.
Release builds (issue #129, `private/stores/play-store.md`): R8 minify and resource shrinking on,
signed with the upload key `~/.otc/otc-upload.jks` (password in the Keychain, service
`otc-android-upload`, passed as `OTC_UPLOAD_PASSWORD`; never in the repo), `versionCode` = the
commit count; `-Potc.signWithDebug` signs a release build with the debug key to try it over an
installed debug build. The launcher icon is adaptive (`mipmap-anydpi-v26`). One deliberate difference: the photo sync's cloud-id shortcut (release
7, `files.cloud_id`, `HasCloudIds`) is iOS-only - `PHCloudIdentifier` is the same for an asset on
every device on the owner's iCloud account, so a phone asks the device "which of these do you
already have?" and links the hash instead of downloading the asset from iCloud to hash it. Android
has no equivalent (MediaStore ids are one phone's row numbers, and the originals are local files
anyway), so it leaves `cloud_id` empty; the device attaches an id to every row of a hash whenever
any client names it (`HasFile.cloud_id`), so content uploaded from Android still gets one the
first time an iPhone sees the same asset. The EXIF panel's map is osmdroid (OpenStreetMap, no API key), and it
only ever has something to show because `PhotoSync.readData` asks the MediaStore for the
original bytes under `ACCESS_MEDIA_LOCATION` - since Android 10 a photo read through a plain
content URI comes back with its GPS tags blanked to 0/0, which the device used to report as
NaN. Two Compose pitfalls hit here: a `PlayerView` in a `LazyColumn` must be inflated from
`res/layout/player_texture.xml` (`surface_type="texture_view"` + `clipToBounds()`), or its
SurfaceView paints at a stale position over whatever scrolls above it, and a full-screen `Dialog`
with `decorFitsSystemWindows = false` needs `systemBarsPadding()` or its top buttons sit under
the status bar and never get the tap. A third: a `Dialog` shown outside MainView (from RootView, as
SyncChoiceView) must draw a `Surface`, not a `background()`, or its text takes the default black
content colour and vanishes in dark mode.

**What to sync, asked at every sign-in** (iOS `SyncChoice.swift` <-> Android `ui/SyncChoice.kt`):
Onboarding's Save & Continue and the Bluetooth setup's "Use this device" call `SyncChoice.ask()`
before saving the credentials, and a screen that can't be dismissed asks for all photos and videos
(= Sync All: watermark cleared) or only new ones (= Sync From Now), with Wi-Fi only, Include videos
and Sync from iCloud / Sync from cloud and a warning that a whole library takes long and uses data.
Until it is answered (`syncChoicePending`, UserDefaults / the `otc_sync` prefs) `PhotoSync.runForeground`
returns at once - background runs included - and Android asks for media permission only after it.
**Folders and albums to back up** (Android `sync/PhotoFolders.kt` + `ui/settings/FoldersView.kt` <->
iOS `PhotoSources.swift` + `AlbumsView.swift`; owner's request 2026-10-24, after a user's folders seemed
missing): Settings > Sync Options > Folders (Android: MediaStore buckets by `BUCKET_ID`, with path and
volume) / Albums (iOS: the owner's albums plus Screenshots, Selfies and Screen Recordings), each with a
count and a switch. All on by default and stored as what to SKIP (`skippedFolders`), so a folder a phone
starts using later is backed up; nothing stored and no extra clause while none is off. Turning one back on
queues a one-off pass over it with no date limit that never moves the watermark; turning one off deletes
nothing, and Free Up Space leaves skipped folders/albums out. Limited photo access (Android 14+ selected
photos, iOS `.limited`) shows a warning with Select More Photos / Allow Access to All Photos; picks made
later and what was hidden before full access are backed up from the owner's chosen starting point. iOS
also reads the library's persistent change history (`PhotoHistory.swift`) so a photo added late with an
older creation date (AirDrop, Files) isn't behind the creationDate watermark; a missing token keeps the
old behaviour. Android's DATE_ADDED filter never had that gap.
**Free up space** (iOS `FreeUpSpace.swift` <-> Android `sync/FreeUpSpace.kt`; owner's request
2026-10-21): off unless turned on (Settings > Free Up Space, and the same section on the sign-in
sheet), it deletes from the phone the photos and videos taken more than N days ago (1-365, 7 by
default) that the device holds an exact copy of. It runs at the end of a sync that ran
(`runForeground` -> `afterSync`, after the sync's own flag is down) while the app is in front, at
most once a day (`freeUpLastRun`; a run that found nothing or whose system question was answered
counts, one cut short doesn't; turning it on or changing the days makes it due). An asset goes by
the SHA-256 of what the sync sends, and only once the device confirms that hash on its disk right
then: `HasHashes` (140, answers `HashesFound` 70, 500 a request, up to 1000 looked at): a `files`
row with the hash and its blob at exactly `segcrypt.EncryptedSize(row size)` - a stat, never a
decrypt; older devices answer `unknown_payload` and the phones fall back to `HasFile` per hash. iOS
reads each original right before deleting it, only when it is on the phone (never from iCloud:
those stay), as a recorded hash may come from the device's cloud-id map rather than the bytes -
AssetSyncCache's hash only spares reading what the device doesn't hold - and never deletes edited
assets (the sync sends the original),
bursts, Live Photos unless `freeUpLivePhotos` (the sync sends only the still), or videos while
Include videos is off; `PHAssetChangeRequest.deleteAssets` asks every time and keeps them 30 days
in Recently Deleted (with iCloud Photos they leave iCloud too). Android re-reads a file whose
`GENERATION_MODIFIED` or `SIZE` changed since it was hashed (its own `free_up_hashes.json`;
AssetSyncCache's hashes don't say which version), needs Android 11 (`createTrashRequest`, launched
by RootView: it needs an Activity), and its trash keeps them 30 days. Both fetch the assets again
right before the deletion and drop any that changed, and drop the run after a Log Out or a change
of device. Settings live in UserDefaults (iOS, through SecretsStore) and the `otc_sync` prefs
(Android: not backed up), wiped at Log Out.
**Settings › Wi-Fi** (iOS `WifiSection.swift`: `WifiSection`, `WifiViewModel`, `enum WifiFlow` <->
Android `WifiSection()` in `ui/settings/SettingsView.kt`, `WifiViewModel` in `SettingsViewModels.kt`,
`object WifiFlow` in `ui/settings/WifiFlow.kt`; the rules and the device side are under `network`
above): `WifiFlow` holds the pure rules, tested with the web's case ids (`WifiFlowTests.swift` <->
`WifiFlowTest.kt`). "unknown_payload until the next sign-in" is `DeviceSupport.wifi` (reset by
`signedIn()`) on iOS and the sign-in count (`OTCConnection.signIns`) on Android. The password lives
in the view model only, is sealed with `GetPubKey` + `PwCrypto` and cleared once sealed; no
password AutoFill (iOS a plain `SecureField` with no content type; Android a platform `EditText`
with `importantForAutofill = no`, as every Compose field takes part in autofill) and Return/Done is
Connect; the network-name field has no capitalisation, correction or smart punctuation (iOS a
`UITextField` wrapper: SwiftUI's `TextField` can't turn smart quotes off). iOS keeps its wait in the
view model while the Settings tab lives; Android stops looking in the background or when Settings
is left and picks the wait up again on return, reopening the picker on the network tried after a
wrong password. `DeviceUnreachableView` shows the cable hint (`app.shell.unreachable_wifi_hint`)
under the device's words and before "still trying", except for a disabled account.
**Phone app invariants** (both apps unless named). Photo sync's watermark is written per chunk,
never set to "now" at the end and never past the run's start (iOS `lastSyncDate`, Android
`DATE_ADDED`); one ahead of the clock is pulled back, and a chunk boundary inside one date is backed
off so that date is fetched again. Android holds it before the first failed upload; iOS keeps failed
assets in a retry list (`asset_sync_pending.log`) fetched first next run. A running sync's writes
are gated by generations bumped by `cancel()` (Log Out, which cancels again right before the wipe)
and by Sync From Now. A photo whose name the device holds with other content goes to
`<stem>_<first 8 hex of SHA-256(localIdentifier | MediaStore id)>.<ext>`. After the device rejects
Auth (an empty `Ack.code`, or `too_many_attempts`) `OTCConnection` fails the same credentials
without dialling for 5 s doubling to 300 s; success, `invalidate()` or new credentials lift it.
Files open, versions, trimming and the gallery's video fallback read 4 MiB `ReadFile` pieces
(`FileDownload.swift` / `ChunkedDownload.kt`); `GetFile` stays for HEIC and as the
`unknown_payload` fallback. Android keeps at most 3 `UploadChunk`s in flight (OkHttp closes a
socket past 16 MiB queued). Android only: backup and device transfer include just
`otc_settings.xml` (`data_extraction_rules.xml`, `backup_rules.xml`) - new prefs and files stay out
unless added there on purpose; `SecretsStore` recovers an `otc_secrets` restored without its
Keystore key by resetting to a fresh install; connection forms write `SecretsStore` only on Save,
since every reconnect dials the stored values; `PhotoSync.hasPermission()` must match
`mediaPermissions()`. iOS unit tests: `OffTheCloudTests` (Swift Testing), on a simulator with
`-only-testing:OffTheCloudTests`.

**Drag to select** (issue #200, iOS `DragSelect.swift` <-> Android `ui/common/DragSelect.kt`):
Images, People and Files (list and grid) select by long press, and a drag on without lifting
adds every item from the pressed one to the one under the finger, in display order (shrinking
back restores what the range passed over; a press never deselects); while anything is selected
a tap toggles. On iOS it is a UIKit `UILongPressGestureRecognizer` through
`UIGestureRecognizerRepresentable` on the lazy stack/grid/List (SwiftUI's own long press inside
a ScrollView or List holds the scroll up), refusing to begin unless the press is inside an
item's frame (`gestureRecognizerShouldBegin` and `began` use `DragSelectFrames.item(containing:)`:
a gap, the empty slots after a short row, padding, a title, Files' frameless `..`, or a face's
name or count or its "more" button start nothing - People's `faceToStart` refuses a point inside
a second `DragSelectFrames` of the buttons, recorded inside their offset; moves take the nearest
item in the row, `item(at:)`); items record
their frames with `onGeometryChange` plus
on-screen counts (`DragSelectFrames`), in a name on the lazy stack - but a List's rows don't see
a name given to the List, so Files' list uses `.global`. The edges scroll the found
`UIScrollView`'s `contentOffset` from a `CADisplayLink` (bands of min(56 pt, a third of the
visible height), none under 8 pt, up to 1200 pt/s, only once the finger has moved more than
10 pt from where the press fired, `DragSelect.armed`). A pressed item gone mid-drag (a refresh)
ends the drag (`DragSelection.goesOn`), and so does a new Files listing (`load()` drops the drag
where it sets the selection, none): the selection stays as it stands, `moved` answers false and
the gesture stops following and scrolling, and the lift is still no tap; the item under the
finger gone keeps the selection until the finger is over another (`range(in:)` is nil). A pick
made before the press that goes mid-drag (People's listing `onChange`, Images' deletes through
`items`) leaves the drag's base too (`DragSelection.forget`), so the next move doesn't bring it
back. Files' long-press menu is gone: Share as gallery and Keep out of / Show
in Images are in the bar when one folder is selected (as Android's), upload only stays on the
list's lock, and a grid tile's lock asks to allow deletions. Tests: `DragSelectTests.swift`; on
a simulator, idb can hold then drag through its Python client's HID events (touch down, delay,
moves, up) - `idb ui swipe` can't. On Android it is `Modifier.dragSelect` on the LazyColumn/grid
(a pointer input on the Initial pass, so after the long press the moves and the lift are the
drag's), hit-testing the lazy layout (`keyAt`; Images' `GalleryRow.itemAt`); a drag starts only
where `rememberDragSelect`'s `start` finds an item (People: `onFace`, the face but not its name,
count or "more" button), and the edges scroll (bands of min(56 dp, a third of the list), none
under 8 dp, `dragScrollSpeed`) only once the finger has moved more than the touch slop from where
the press fired (`DragSelectState.armed`). The view models' `dragGoesOn` is asked at every move
and before every scroll step (`DragSelectState.goingOn`), not only when another item comes under
the finger: once the pressed item is gone (`DragSelection.goesOn`, for good even if it comes
back) or a new Files listing has landed (`load()` drops the drag before it sets the selection,
none), the drag is over with the selection as it stands - moves change nothing, the edges stop,
and the lift is still no tap; the item under the finger gone keeps the selection until the
finger is over another (`range` is null). A pick that goes mid-drag (People's `keepOnly`,
Images' deletes) leaves the drag's base too (`DragSelection.forget`), so the range doesn't
bring it back. Tests: `DragSelectTest.kt`, `PeopleViewTest.kt`, `PhotoGalleryRetryTest.kt`.

**Leaving selection mode on iOS** (`SelectionActionBar.swift`, Images and Files): the count pill
reads [× N selected]; the × (`onClear`, named `app.shell.clear_selection`) runs the view model's
`clearSelection()` - everything unpicked, a drag under way ended (its next moves pick nothing),
Images' `pickedAway` dropped, a tap opens again; Files keeps the bar for Upload. The × takes only
its glyph's room but lays out and answers touches over a 44 pt square (`TouchTarget`); it is
drawn over the pill (`overlay`, a hidden glyph holding its place), not inside it, because the
iOS 26 glass takes no touch past its own edge for what it holds, and the pill sits 10 pt above
the bar so the bar's items don't take the square's bottom. `.keyboardShortcut(.cancelAction)`
gives an iPad keyboard's Escape the same (a dialog or sheet over the screen takes Escape first).
People keeps its own toolbar ×. Tests: `DragSelectTests.swift` (`SelectionBarClearTests`).

**Leaving selection mode on Android** (`ui/common/SelectionActionBar.kt`): the "N selected" pill
above the bar starts with an × (`onClear`, named `app.shell.clear_selection`) that runs the
screen's `clearSelection` - everything unpicked, a drag to select ended, a tap opens again; Files
keeps the bar for Upload. The pill stays compact (`SelectedPill` draws its Surface under the row,
so nothing clips the ×) while the × lays out at 20 dp but is measured and touched as a 48 dp
square (`touchTarget`), the pill above the bar (`zIndex`) so those touches reach into the bar's
top padding. Back clears too: Images' `BackHandler`, Files' `filesBack` - the search's results
first (they hide the folder and its bar), never while a search is open; the viewer, dialogs and
menus take Back first in their own windows. People keeps its own ×.

**Copies on Android** (issue #201, `PhotoGalleryViewModel.kt`): `Item.copies` is `File.copies`
(null when unset); `copiesAsk` picks the dialog (`CopiesDialog`: one photo with copies above 1,
`app.photos.copies.single_text`, "Delete only this copy" / "Delete all N copies"; a selection with
any, `several_text`, "only these" / "every copy"; else the old confirmation). `delFileFor` sends
the tile's `hash` on every delete and `every_copy` only where copies is above 1; Files' rows send
their hash (`filesDelete`, files only). A refusal keeps the tile and shows the device's words in
`app.photos.notice.delete_failed`; `changed` closes the viewer and starts the search again (the
Files viewer lists its folder again; Files' own delete toasts the words). Picks a jump or a
reload takes off the grid keep the `Item` last shown (`pickedAway`: `keptPicks` before
`performJump` / `resetAndLoadFirstPage` empty the grid; `pickedByPath`, the grid's own first):
`deleteSelected` sends their hash and every_copy and the copies question counts them
(`pickedItems()`), as iOS's `pickedAway`; unpicking, clearing or deleting forgets them. `removeDeleted` lowers
the `have` of the search that brought the photo. The viewer's Save downloads the original with
`ChunkedDownload` and inserts the bytes as they are through MediaStore (`IS_PENDING`, `DATE_TAKEN`
= the photo's date, its name and MIME, `saveFolder`: `Pictures/Off The Cloud`, a video
`Movies/Off The Cloud`), in the view model's scope; Save spins meanwhile (`State.saving`); only a
type the library refuses is saved as the shown picture in JPEG, as before.

**Newer photos above a jump on Android** (issue #194): a jump sets `State.upOn` and a second
cursor (`upToken`, `upHave`, `upEnd`; `askUp`/`fetchUpPage`, `upFailed`'s backoff, `retryUp`, a
Wake); the downward `have` is `downHave`, never `items.size`. The room above (`roomAbove`,
`layoutGallery(above =)`): a first month from the upward search keeps its bucket's count with
grey slots first (`lead` on `TileRow`/`Segment`), a complete one gets the next newer bucket's
month in grey above it. Rows are keyed by month and place (`GalleryLayout.keyOf`, "2024-03#0"), so
photos land in the grey tiles with no key changing and the LazyColumn keeps what is on screen;
`keepRowsInPlace` (the frame before, `DrawnRows`) holds the first row still there when one went
(a miscounted bucket, the room dropped). Nothing is asked at the jump - its landing puts its
month's title at the top in the SideEffect (`requestScrollToItem`), so the room is never drawn -
only once a grey tile is (`rowShown(key, start, end, lead)`), and after that for the first
`UP_NEAR` (12) photos too; each page redraws the rows' effects (keyed on `upLanded`,
`composedRows` cleared). `oldest_first` false drops the page and the room, not asked again until
the next sign-in (`OTCConnection.signIns`). A failed page is the foot's words and Try again in a
pill at the top while near it. The viewer's pager is keyed by item id.

**Storage view on Android** (issue #195, `ui/files/FilesUsage.kt`): `FilesView` (List, Grid,
Storage) in View options, kept in `files_view_mode` ("storage"; older builds read it as the
list). `askUsage` runs on `setView` and every `load()` while Storage shows, never from List or
Grid; answers for another folder are dropped (`usageFits`, a sequence); `unknown_payload` shows
`app.files.usage_needs_update` until the next sign-in. Rows keep the device's order, joined with
the listing by (path, is_dir) for the icon, lock and eye; size is files + versions, the share a
whole percentage, rounded (`usageShareText`: "under 1%", no bar at a 0 total); the total
and versions lines above, `..`, the note below; no selection, no Sort by; a folder opens in
Storage, a file as in the list. Tests: `PhotoGalleryUpTest.kt`, `PhotoMonthsTest.kt`,
`FilesUsageTest.kt`.

**Copies on iOS** (issue #201, `DeleteCopies.swift`, `DeleteCopiesTests.swift`): `Item.copies`
is `File.copies` (nil when unset); `CopiesAsk.of` picks the question - one photo above 1 copy
(the viewer's Delete, or one picked): `app.photos.copies.single_text`, "Delete only this copy" /
"Delete all N copies"; several with any: `several_text`, "only these" / "every copy"; else the
old one - taken when Delete is tapped (`confirmCopies`, `deleteAsk`). `DeleteCopies.request`
sends the tile's `hash` on every delete (Files' rows too, files only) and `every_copy` only
where copies is above 1. A refusal keeps the tile and shows the device's words in
`app.photos.notice.delete_failed`; `changed` closes the viewer and runs the search again from
where it began (`reloadSearch`: a jump at its month), the Files viewer lists its folder again
(`onDeleted`), Files' own delete toasts the words; Files now shows its viewer's alerts too
(`ViewerNotice`). Save downloads the original (`FileDownload.download(original:)`, ReadFile
even for HEIC) and adds it as it is (`PHAssetCreationRequest` `.photo`/`.video` resource,
`creationDate` = the item's date), add-only access as before; Save spins (`savingPath`,
`app.photos.viewer.saving`); only a photo Photos refuses is saved as the picture shown.

**Newer photos above a jump on iOS** (issue #194, `NewerPhotos.swift`, `NewerPhotosTests.swift`):
after a jump `PhotoGalleryVM` keeps a second cursor (`upToken`, `upEnd`, `upLoading`,
`upProblem`, `upRetry`, a wake); each search's `have` counts its own items (`Item.up`), so a
delete lowers the right one. Nothing is asked at the jump: the view arms it when the grid is
scrolled up (`onScrollGeometryChange`, `armNewer` - a pull at the top counts), then rows near
the top on screen (`newerIfNearTop`: first photo under 12, or the top month's grey tiles) ask.
`oldest_first` false drops the page; `DeviceSupport.newerThan` stays off until the next sign-in
(`OTCConnection.connected`). A page goes above newest first, less what is shown by id or hash
(`PhotoPaging.newer`); `openIndex`/`morePendingAt` shift. Rows are named by month and place
(`PhotoGridLayout`, "t2024-03@6"), tiles by photo (`.id`), so the rows on screen keep their
names; `GridAnchor` notes the top row's frame (`.scrollView` space) right before
(`willChangeAbove`) and `proxy.scrollTo(row, anchor:)` puts it back right after
(`aboveChanges`; scrollTo lines anchors up in the whole container height, the tab bar's inset
included), with 0.3 s in which the rows drawn for that moment ask nothing. The top month from
the upward search keeps its bucket's room with grey tiles before its photos (`firstRoom`,
negative indices from `firstSlot`), and a page's months never join the lines under them
(`upBreaks`). Spinner and failure float over the first row (`newerStatus`).

**Storage view on iOS** (issue #195, `FolderUsage.swift`, `FolderUsageTests.swift`): the third
choice of Files' view menu, `files.viewMode` "storage" (older builds read it as the list), no
Sort by there. `FilesExplorerViewModel.load()` asks `loadUsage()` alongside the listing only
while `storageShown` (on opening it, each folder, a refresh, after a delete); answers for
another folder are dropped (`FolderUsageModel.answers`), errors by a sequence;
`unknown_payload` shows `app.files.usage_needs_update` without asking until the next sign-in
(`DeviceSupport.folderUsage`); no answer `usage_failed` + Try again; other errors the device's
words. Rows keep the device's order, joined with the listing by (path, is_dir) for the icon and
the lock / eye marks (by the name; read as the row's value); size is files + versions, the
share a whole percentage in the app's locale or "under 1%", no bar at a 0 total; the total and
versions lines above, `..`, the note below; no selection; a folder opens in Storage, a file as
in the list. No per-folder file count (no catalog key; the web and Android show none).

**Known gaps left by the 2026-10 hardening pass.** iOS `FileDownload` falls back to `GetFile` only on `error_code =
"unknown_payload"`, sent since v9 (Android also takes the older bare message) - no device that old
exists. Optional items skipped: device - no `IsContentHash` guard in `mediastream/reader.go`, no
write deadlines in its own HTTP server (`api/`), `cloud_id` kept by
`ReplaceFileKeepingVersion`, no rejection of `.`/`..`/empty path components; bridge - no `interpolateParams`,
`admin/ratelimit.go` `purgeLocked` scans on every attempt, `Admin.Logout` answers ok
when ending the other sessions failed and `SetAdminPassword` doesn't bump the admin epoch;
clients - no final-path check on otc-sync's flash status file, no "could not be read" status on the
Mac, no `usePageRetry` in the web's Social feed or `DevicePhotoPicker`.

## Cutting a release (issue #94)

Devices update themselves in place: an owner presses **Update** in Settings and the
device applies every release it is missing, in order, then rebuilds its own binary and
restarts. Nothing is cross-compiled and no binaries are distributed — the device builds
from source, which is what keeps any architecture supported. The web app is the
exception: devices have no Node, so the bundle is built once, here, and attached to the
release.

**Cala is only ever updated this way.** Since 2026-09-23 Cala (installed from the image, SSH off)
gets every change through a release and the Update button in its Settings - never `make pi`/`make
web` against it - so the release history can't fall behind what devices actually run. Pit stays
the `make` development target. Cutting a release is the last step of any device or web change.

**How the button works** (release 5): otc.service runs unprivileged with `NoNewPrivileges=true`, so
the device cannot sudo. `updater.Apply` writes `/var/lib/otc/update.request`; systemd's
`otc-update.path` starts `otc-update.service`, which runs `/usr/local/bin/otc-update-runner` as
root: it reads `[otc] update-repo`/`update-releases` from the root-owned config, downloads
`scripts/update.sh` from there into `/run/otc-update/` and runs it. Those three files live in
`scripts/update-runner/` and are installed by install.sh and by release 5. A device without the
runner falls back to the old `sudo -n` path, which only works on a hand-set-up box like Pit. To
exercise the whole flow on Pit without the app: `ssh otc@pit.otc touch /var/lib/otc/update.request`
and watch `/var/lib/otc/update-status.json`.
otc-update.service has `TimeoutStartSec=4h` (a migration must finish well inside it) and
`ExecStopPost` `otc-update-stopped`, which turns a status still at "running" into failed;
`updater.CurrentStatus` does the same, read-only, when the unit has stopped and nothing holds
`/run/otc-update.lock` (update.sh's shared flock) - power cuts included. Root status files are a
`dd conv=excl` temp file under umask 022, then `mv -T`: never `> file` or chmod as root in
`/var/lib/otc`, which `otc` owns. Downloads in the runner, update.sh, verified-install.sh and
`fetch_pinned` carry `curl --connect-timeout 30 --speed-limit 1 --speed-time 120`, cutting only a
stalled transfer (no `--max-time`).
More root runners follow the same trigger-file shape, and update.sh reinstalls all of them on
every update (one that reads its request's words does so with `dd iflag=nofollow,nonblock`; each
oneshot has a `TimeoutStartSec`, 2min for the bridge and Tailscale ones): `scripts/bridge-runner/`
(switching the bridge on later, #145),
`scripts/tailscale-runner/` (release 50) and `scripts/device-runner/` (release 115: `otc-reboot` for
Settings > Restart device and `otc-wifi-restart` for `wifiwatch`; release 115's script installs them
on older devices from `$OTC_VERIFIED_SRC`; release 140: `otc-wifi-join` for Settings › Wi-Fi, a
Python runner installed as `/usr/local/bin/otc-wifi-join-runner` with its own install lines rather
than the `reboot wifi-restart` loops, triggered by `/run/otc/wifi-join.request` - on the tmpfs, not
in `/var/lib/otc` - and answering in root's `/run/otc-wifi-join/status.json`; 140.sh installs it
with the restart runner, removes a stale request before `enable --now otc-wifi-join.path`, and
deletes the plaintext `/var/lib/otc/wifi_join_request.json` the retired SetWifi left).
tailscaled runs only while Tailscale Funnel is on:
install.sh and release 50 stop and disable it unless `tailscale funnel status` serves `:8080`;
`tailscalefunnel.Enable` writes `on` to `/var/lib/otc/tailscale.request` when the daemon isn't
answering, and the runner starts it and sets `--operator=otc` (which needs the daemon up);
`Disable` resets Funnel and writes `off`. The answer comes back in
`/var/lib/otc/tailscale-status.json`.

**Follow this whenever a change is worth shipping.** A change that is only on `main` has
not reached anybody: the version number in the manifest is the only signal a device has
that anything is new.

Issue #160: releases are **signed**. Cut one with `scripts/release.sh "Summary shown in
Settings"` from a clean, pushed `main` - nothing else. If the release needs a schema or
config change, first commit its script as `scripts/updates/<N>.sh` (N = the manifest's
last version + 1; it runs as root on the primary and on every per-user database and MUST
be idempotent; update.sh runs it before building the new binary, so it only sees rows the old code
wrote). A script that loops over users must let only a `db_name` matching `^otc_[0-9a-f]{32}$` reach
root's mysql command line (the otc service can write `users`, and a name starting with `-` would be
an option), as `92.sh` (one like per domain) does. The tool builds the web bundle, tags `vN`, makes
the release's own source archive (`src.tar.gz`, `git archive` + `gzip -n`; the manifests and their
signatures are `export-ignore`d in `.gitattributes`, since they carry its hash, and so are
`docs/app-store`, `docs/play-store`, `docs/screenshots` and `bridge/bin/otc_bridge` - about 11 MB
instead of 90; over 30 MB is refused), appends the manifest line (version, script sha, web sha,
summary, source sha) and signs the whole manifest with the release key into
`scripts/updates/VERSIONS.sig`. Then it pushes the tag, publishes the GitHub release, checks the
published archives, and only then commits and pushes the signed manifest to main - archives first,
manifest last. A failure before that commit deletes the release and the tag; a failed final push of
main must be finished by hand (`pull --rebase`, push). The key is Ed25519 at
`~/.otc/otc-release-signing.pem` on the Mac mini (passphrase in the Keychain item
`otc-release-signing`; never on GitHub - back it up offline); its public half is
`scripts/release-signing.pub`, which devices pin in `/etc/otc/release-signing.pub`.
Then run `scripts/docker-release.sh` (the `container` paragraph above): the Docker image for that
release is built, tested and published from the Mac (about an hour cold, less once its cache is
warm); a failure there never touches the Pi release.

Issue #183: a release is **minor** (default), **major** (`--major`: security, stability or
durability) or **critical** (`--critical`: breaks compatibility with the bridge or the apps if not
installed, or a serious security fix), and has a `major.minor` version: a minor adds to the minor
number, a major or critical starts the next major. Release 84 is 1.0; earlier ones have no version
and show as builds. Kinds and versions are in `scripts/updates/RELEASES` (`<release> <kind>
<version>`), signed into `RELEASES.sig` by `release.sh` and checked by the device with Go's Ed25519
(`updater/kinds.go`) - a separate file because the runners read `VERSIONS` by position. The main
instance checks for updates by itself every hour (`updater.Watch`): a pending major or critical
update adds one `Update` notification per version (a critical one also pushes, once, with it), and `Status.update_alert` carries it to every
app; a critical one shows a banner in the web app, iOS and Android (and a line in the Mac app and
otc-sync) until it is installed. Settings shows "1.1 (build 85)" and badges pending releases.
`updater.Check` trusts `VERSIONS` only with its signature (a fork must sign its VERSIONS too) and
shows the last verified list on a mismatch (CDN skew); `Watch` keeps the known alert through a
failed check and retries from 5 min, doubling up to 6 h.

On a device, `otc-update-runner` downloads the manifest and its signature, verifies it with
the pinned key, downloads the target release's `src.tar.gz`, checks it against the signed
hash, and runs the `update.sh` inside it (`OTC_VERIFIED_MANIFEST`/`OTC_VERIFIED_SRC`): release
scripts come from that verified source and are checked against the signed manifest, the web
bundle too, and nothing is ever run from `main`. A device whose runner predates this ran
`update.sh` from `main` once: that copy does the same checks with the key embedded in it,
then hands over to the verified release (which installs the key and the new runner).
Forks publishing their own releases replace `scripts/release-signing.pub` with their key. Fresh
installs are verified the same way: `scripts/verified-install.sh` (the image ships it as
`/usr/local/bin/otc-verified-install` with the key in `/etc/otc/release-signing.pub`; by hand,
`curl …/verified-install.sh | sudo bash -s -- <name>`) checks the newest release's signature,
source archive and web bundle, and runs the `install.sh` inside that verified source
(`OTC_VERIFIED_SRC`, `OTC_VERIFIED_WEB`, `OTC_RELEASE_VERSION`); `install.sh` run any other way hands
over to it. The install copies that source instead of cloning, installs the release's web bundle
(no Node or npm on devices), pins Go, ONNX Runtime, protoc and the models by SHA-256
(`fetch_pinned`: into a mktemp directory beside the destination, then renamed; `/usr/local/models`
is root's, 0755, read by the service), and installs Tailscale from its apt repository with the key
pinned by hash. A re-run keeps the config's `[otc]` lines it doesn't write itself (a fork's
`update-repo`/`update-releases`, `low-memory`, a watchdog switched off) and, without
`OTC_BRIDGE_ADDR`, the `bridge-addr`/`bridge-default` it finds (empty after a switch-off, #182); the
config is written temp (0640 root:otc), sync, rename. The device's MariaDB account holds ALL on `otc`
and, with GRANT OPTION, on `otc_%` (in a GRANT the `_` of `otc_<uuid>` is itself a wildcard: an
escaped `otc\_%` can't hand ALL on `otc_<uuid>` to that user's account - error 1044), plus CREATE
USER and RELOAD - nothing on `*.*` (install.sh, the container's entrypoint, release 143's REVOKE;
tested in the Lima VM by provisioning, measuring and dropping a user through `dao`).

**Checking it worked**: a device shows its version under Settings → Device version. The full
log of any run is at `/var/log/otc-update/update.log` (Settings › Logs › Updates), and the current
state is in `/var/lib/otc/update-status.json`. `/etc/otc/version` is written only once the whole
update has worked: migrations, build and web app. Release 88 on Pit showed why. GitHub answered 500
for its web bundle, the script carried on with the old one, and the per-release version write left
the device "up to date" for good. Each release's migration is recorded in
`/etc/otc/version.migrated`, so a failed run retries everything else without re-running scripts. A
web bundle that can't be downloaded fails the update, and a failed build leaves the running binary
untouched: update.sh runs migrations, build, then the web bundle's download and check, and only
then installs anything (`install_atomic`: temp, sync, rename) and writes the version file. The
staged release is removed on exit.
Since release 143 the update also checks the new version starts. The binary it replaces stays as
`/usr/bin/otc.prev` (a hard link), and the web app is unpacked into a directory of its own under
root's `/var/otc-web/` and switched in by renaming the `/var/www` symlink (the first time, the old
`/var/www` directory moves there): never written file by file into what is served, the one before
kept. After `systemctl restart otc` update.sh writes `verifying` and waits up to 3 minutes for the
service to answer `http://127.0.0.1:<[otc-api] port>/check_healty` and then keep answering for 20 s
as the same MainPID with no restart (Restart=on-failure alone never gives up on a binary that fails
at start). Otherwise it puts back the binary, the web app and `/etc/otc/version` (migrations and
version.migrated stay: they only add), restarts, and fails with `rolled_back`, or `rollback_failed`
if something couldn't be put back; on success the older web directories go. The legacy sudo path
runs inside otc.service, so it still reports done and restarts last, unchecked. Tested in the Lima VM
with a release whose binary exits at start. `/etc/otc/version` and version.migrated are written
temp, sync, rename (install.sh too, which writes both); a version file that holds no number (a
write cut short) is recovered from version.migrated by the runner and update.sh (and written back),
or the update fails with `version_unreadable`; `updater` reads it the same way and reports
`Info.VersionUnknown` (nothing pending, no alert) - read as 0, every release was announced. Before
downloading, the runner checks free space (150 MB on `/run/otc-update`, 1 GB on `/`) and update.sh
again (1 GB on `/`, 300 MB in its temporary directory) - `no_space`. A manifest whose signature
doesn't match is fetched again every 10 s for a minute (CDN lag right after a release) before
`manifest_not_signed`, worded "could not verify this release yet - … try again in a few minutes";
verified-install.sh retries the same way.

**Forks** set `[otc] update-repo` and `[otc] update-releases` so a device updates from its
own repository rather than silently taking code from upstream.

## Config

Runtime config is INI, loaded via `cfg.Init(appName, env)`: it reads `etc/otc_<env>.ini` relative to
the working directory, or `/etc/otc_<env>.ini` if that's missing. Dev config lives at
`cfg/etc/config_dev.ini`; see `README.md` for the full annotated `[otc]`, `[otc-api]`, `[mysql]`,
`[logger]`, `[tagger]`, `[faces]` sections used in production on the Pi.

## VERY IMPORTANT NOTES
- **The repository is public: commit only what outside readers are meant to read** (users,
  contributors, forks, the press), written for them. Plans, to-do lists, drafts, store-submission
  notes, GDPR records and notes about the maintainer's decisions go in `private/` (ignored by git:
  `private/stores/`, `private/gdpr/`, `private/press/`), never in `docs/` or anywhere else tracked.
- **The iOS and Android apps must stay in sync - this is critical.** `app/android` is a 1:1 port of
  `app/ios`: any change to a screen, flow, request, setting or behaviour in one app must be made in
  the other in the same piece of work, with the matching file/type name (SwiftUI view <->
  Composable, ViewModel <-> ViewModel). Never ship a feature or fix to only one of them; if the
  other side genuinely can't be done yet, open a GitHub issue for it before finishing.
- **The macOS, Windows and Linux sync clients must stay in sync too - equally critical.** The
  macOS app (`app/macos`, Swift) and `otc-sync` (`app/desktop`, Go, the Windows and Linux client)
  are the same product: any change to what one of them does - a menu item, a sync rule, a
  setting, a status, how it starts at login - is made to the other in the same piece of work
  (Swift file <-> Go package, see "Desktop sync client"). Never ship to only one of the three
  platforms; if a platform genuinely can't be done yet, open a GitHub issue for it before
  finishing.
- All the sensible content like photographies or files of any kind uploaded by the user should be encrypted at rest
- No sensible communications should be shared over unsecure channels
- Security is our main prioirty then reliability, durability and performance
- Every time that a make is done, update the documentation, installation scripts, images and any other necessary parts
- Test as much as possible in the browser or the iOS and Android simulators
- I'm working on other machine, so before working on something, do a `git pull` to update from what we have in the repo
