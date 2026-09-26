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

The image (issue #38) is stock Raspberry Pi OS Lite plus `scripts/setup_wizard.py` (a root,
stdlib-only web wizard on port 80: WiFi, device name reserved on the bridge via its public
`/api/name-available` + `/api/claim`, disks, then runs `install.sh` and shows its `[n/10]` steps as
a progress bar) and `scripts/network_setup.py` (the "Off The Cloud" hotspot on a virtual `uap0`
interface, kept up while `wlan0` joins the owner's WiFi - verified on a Pi 5; one radio means one
channel, so the join is pinned to 2.4 GHz during setup and the hotspot follows it, and the band
limit is lifted once setup is done). Networks are scanned once before the hotspot starts (scanning
takes the radio away and drops the phone's captive sheet), and the captive DNS stays on for the
whole setup so the sheet stays open (the bridge's own domain is exempted so the final link works).
Fallback if the phone still loses the page: the device reports its LAN address to the bridge under
a one-time token (`POST /api/setup-beacon`, bridge DB `setup_beacons`, 10-minute expiry) and the
page polls `GET /api/setup-lookup`. Three Pi 5 pitfalls learnt the hard way: the image strips SSH host keys, so enabling ssh on an older card needs `ssh-keygen -A` first (newer images regenerate them from a ssh.service drop-in); NetworkManager's WiFi
switch ships off on stock Raspberry Pi OS (`nmcli radio wifi on`), and an idle `wlan0` reports
channel 34 (5170 MHz) - never copy a channel from an interface that isn't connected. The wizard also handles a re-imaged
device: it assembles any existing RAID1 array (`mdadm` is the one package baked into the image)
and, if it holds an OTC database, offers recovery - `install.sh` reassembles instead of wiping and
the identity in that database wins, so no name is asked; after installing it polls the bridge's
`/api/device-online` and only asks for a name if the recovered device never shows up. It contains
no otc code, so it only needs rebuilding when those two scripts change. The image has no SSH; its
console login is `otc-debug` / `off-the-cloud` (with sudo), set in `build_image.sh` and documented
in README.md under "Console access" - the only way into a device like Cala short of enabling SSH
from that console.

The bridge has its own `bridge/makefile` (`make -C bridge bridge`) which builds
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0` and deploys to `off-the.cloud` over SSH as `ubuntu`.

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
  pkg-config` on Debian/Raspberry Pi OS; on macOS, `brew install opencv@4` (plain `brew install
  opencv` currently installs OpenCV 5, which gocv v0.40.0 doesn't build against) and add
  `$(brew --prefix opencv@4)/lib/pkgconfig` to `PKG_CONFIG_PATH` if `pkg-config --exists opencv4`
  doesn't already find it.
- `make pb` needs `npx protoc` with the Go, Go-gRPC, ts-proto, and Swift protoc plugins available
  (`go install google.golang.org/protobuf/cmd/protoc-gen-go@<go.mod version>`, `go install
  google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`, `brew install swift-protobuf`; ts-proto comes
  from `web/node_modules` after `npm ci --prefix web` - the pb target puts both on its PATH).

**Tests**: only `cfg/` and `log/` currently have `_test.go` files. Run with
`go test ./cfg/... ./log/...` (or `go test ./...` once the Go toolchain matches `go.mod`).

**Web app** (`web/`, Vite + React 19 + TypeScript + react-router). To *see* it on a device from a
terminal session, `scripts/dev/webshot.mjs` drives a headless Chrome over the DevTools protocol
(sign in, open a tab, hover/click one element, PNG per step) - through an SSH tunnel to loopback,
because macOS's local-network permission blocks a terminal-launched Chrome from LAN addresses and
Chrome doesn't resolve `.otc` names; see the header comment.
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
  call in `web/src/net/ws.ts`.
- The frontend's `web/src/net/ws.ts` / `useWS.ts` wrap the same protobuf envelope pattern for the
  browser client.

### Device-side package layout (root Go module)

Flat, one-package-per-concern, wired together in `bin/otc.go`:

- `cfg` — INI config loader (`etc/otc_<env>.ini`, falling back to `/etc/otc_<env>.ini`); `env` is
  `os.Args[1]` (defaults to `"dev"`). All other packages pull settings via `cfg.GetStr/GetInt/...`.
- `dao` — the only package that talks to MySQL/MariaDB directly (schema in `db/db.sql`: `files`,
  `file_tags`, `social_publications` + likes/comments, `social_friendship`, `settings`, `profile`,
  `shared_links`, `vault`, `events`, `people`, `faces`, `image_groups` + `image_group_files`,
  `upload_only_folders` + `file_versions`, `notifications`). Business logic in other packages should go
  through `dao`, not raw SQL. Issue #64: a device-side error the owner should know about (a
  photo that could not be processed, an upload that never reached the disk) goes into
  `notifications` as type `Error` through `dao.AddErrorNotification` - grouped, so an error
  within five minutes of an open Error row joins it (`details` gains a line, `occurrences`
  goes up, the row is unread again) rather than adding a row; `files_manager.alert` is the
  one call site helper. The clients show one line per row and the full list on hover (web)
  or tap (iOS/Android). Never push-notify these.
- `files_manager` — file storage, hashing, dedup on disk. Issue #132's upload-only folders live
  here: `upload_only_folders` (paths with their trailing slash, checked by prefix) refuse
  `DelPath` for anything under them with `ErrUploadOnly` (`RespEnvelope.error_code =
  "upload_only"`, which the sync clients treat as done rather than retry), and a second
  `UploadFile`/`LinkFile` to an existing path there moves the old row into `file_versions`
  (`dao.ReplaceFileKeepingVersion`) instead of failing or overwriting. Blobs are shared by hash
  between `files` and `file_versions`, so `HashReferenced` is the check before one is removed;
  a path's versions go with it when it is finally deleted. `ListFiles` annotates every entry
  with `upload_only` and `versions`, `ListFileVersions` lists them, and `GetFile.hash` serves
  one. The web, iOS and Android explorers show the lock on folders (a toggle, `SetUploadOnly`)
  and the versions badge that opens the pop-up.
- `images_tagger` — runs the RAM++ ONNX model (paths from `[tagger]` config) to auto-tag photos;
  requires CGO + libonnxruntime at runtime (see Build section).
- `face_recognition` — issue #52's "People" search: detects faces (YuNet) and embeds them (SFace)
  via `gocv`, humans only. `files_manager.processFaces` (called from `UploadFile`'s background
  goroutine) gates this on `settings.face_recognition_enabled` (off by default) checked *at upload
  time* - enabling it later never retroactively processes anything already in the library, by
  design (see the `faces` table's doc comment in `db.sql`). Requires CGO + a real OpenCV install at
  build time (see Build section); optional at runtime like APNs - a device with `[faces]`
  unconfigured just has the feature unavailable, nothing else affected.
- `bg_processor` — background job runner invoked from `files_manager`/`websocket`.
- `websocket` — the `/ws` connection handler and dispatch switch described above; also owns
  `ensureBridgePool()`/`openBridgeConn()`, which the device uses to dial *out* to the bridge relay
  (`[otc] bridge-addr`) so the bridge can reach an otherwise unreachable home device. The pool is
  self-managing (5 ready, refilling in batches of 2 once it dips to 3) rather than a fixed count
  dialed once at startup — see `ensureBridgePool`'s doc comment.
- `social`, `session`, `settings`, `profile`, `status` — feature-specific logic (social feed/friend
  sync, auth sessions, device settings, owner profile, RAID/disk/CPU status) sitting between
  `websocket` and `dao`. `session` also owns issue #101's in-memory session-token store
  (`session/tokens.go`): a browser can't keep the account password around the way the native apps
  keep theirs in the Keychain, so after a password `ReqAuth` it holds a single-use, TTL'd opaque
  token (`ReqIssueSessionToken` / `ReqAuthWithToken` / `ReqRevokeSessionToken`) that redeems back
  into the same already-derived `*Session`. Process-local and deliberately not persisted — exactly
  like the `*Session` objects it points at, tokens don't survive a restart. `session/ratelimit.go` (issue #117) is the per-address
  password-attempt limit: 5 failures in a minute lock that address out for a minute, answered with
  `Ack.code = "too_many_attempts"` + `retry_after_seconds`. The bridge reports each relayed client's
  address to the device with `BridgeClientInfo`, so the limit applies through the bridge too.
  Friend requests (issue #25) can be removed by either side: `ReqDeleteFriendship` deletes the
  local row and, best effort, sends `FriendshipInterDelete` (authenticated by the shared
  per-friendship secret) so the other device drops its copy; a sender whose request was deleted
  while it was offline learns it from `FriendshipStatus.not_found` on its next friend sync.
- `push` — Web Push (per-device VAPID keys) and iOS pushes. The APNs auth key is the developer
  team's private key and lives **only on the bridge**: a device never has an `[apns]` section, it
  relays title/body to the bridge (`BridgeNotify`), which sends to the tokens that device itself
  registered - so a device can only ever reach its own phones.
- `api` — the small HTTP layer: healthcheck, the `/ws` upgrade, and static file serving (serves
  `web/dist` copied to the device's static path; appends `.html` to extensionless paths for
  client-side routing).
- `log` — leveled logger with size-based rotation, configured once in `main()` from `[logger]`.

### Bridge (`bridge/`)

A separate deployable with its own `dao`/`websocket`/`api`/`makefile`, sharing only `proto/generated`
and `cfg` with the device module. It maintains a pool of authenticated device WebSocket connections
keyed by domain (`bridgePool` in `bridge/websocket/websocket.go`) and proxies friend/browser traffic
to the right device — the device never accepts inbound connections directly.

### Frontend (`web/`)

React 19 + TypeScript + Vite, routed with `react-router-dom`. `web/src/net/` holds the WebSocket/proto
client; `web/src/views/` are top-level routed pages (`SignIn`, `Social`); `web/src/components/` are the
feature widgets (files explorer, photo gallery, friendships, settings, status, profile, social feed —
each with a co-located `.css`). Built output (`vite build`) is copied by `make web` into the device's
own static dir and the iOS app's bundled web assets. The bridge does **not** get its own copy (issue
#95): a browser hitting `<device>.off-the.cloud` for a static asset is proxied straight through to that
device over the same bridge tunnel every other request uses (`ReqGetStaticAsset` /
`staticassets.Resolve`, shared with the device's own direct HTTP static handler) rather than served from
a separate bundle the bridge would otherwise need redeployed by hand on every web change — this is what
lets a device running an older build still work correctly through the bridge. `bridge/static/` still
holds the bridge's *own* pages (the public landing page, the admin panel), deployed by `bridge/makefile`
independently of a device's web build.

### Desktop sync client (`app/desktop`, issues #119 and #120)

`otc-sync` is the Windows and Linux counterpart of the macOS menu bar app, written in Go inside
this module (it shares `proto/generated`), pure Go so `make desktop` cross-compiles all four
binaries into `dist/` from any machine and `make desktop-publish` uploads them to the rolling
GitHub release `desktop`. Its packages are one-to-one with the Swift files: `wsclient` =
WSClient.swift + PwCrypto.swift, `engine` = SyncModel.swift (upload folders with a watcher and a
10-minute reconcile, two-way remote folders with the three-way merge and a 1-minute poll,
hash-first uploads, RAID polling), `engine/watcher.go` = FolderWatcher.swift (fsnotify, one watch
per directory, added as directories appear), `tray` = PopoverView.swift (fyne.io/systray menu,
the OS's own dialogs through ncruces/zenity - no GUI toolkit, no CGO), `config` = SettingsStore
+ the bookmarks (config.json, keyring or a 0600 `secret` file, state.json), `autostart` (XDG
autostart file / HKCU Run key), `service` (systemd user unit + linger). One process runs the
engine (a flock in the config dir); a tray started next to the service is a viewer, and every
edit goes through config.json, which the engine watches - so the CLI, the tray and the service
never disagree. Remote paths are `/linux/<host>/…` and `/windows/<host>/C/…`, like `/mac/<host>`.
Any behaviour change in the macOS app must be mirrored here (and vice versa), the same rule as
iOS/Android. Test on Linux with the Lima VM `otc` (`limactl shell otc`, `limactl copy
dist/otc-sync-linux-arm64 otc:/tmp/`); the tray needs a real desktop - the Lima VM is headless, so the
Linux tray is tested on a real machine. Windows is tested in the QEMU VM under `~/VMs/win11`
(`./run.sh` starts it: Windows 11 ARM64, user `otc`, SSH on `127.0.0.1:2222` with the Mac's key,
VNC on `127.0.0.1:5905`; `./qmp.py screenshot|type|combo|click` drives the screen through QMP,
`scp -P 2222` copies builds in; the tray app has to be launched on the desktop, e.g. through
`qmp.py combo meta_l+r` + `qmp.py type`, since an SSH session has no desktop). It was installed
unattended from Microsoft's Enterprise Evaluation ISO (`unattend/autounattend.xml`), whose
licence has expired - Windows shows a watermark and may shut the VM down periodically; if that
gets in the way, rebuild it from a retail ISO (CrystalFetch) with the same answer file.

### Native apps (`app/ios`, `app/macos`, `app/android`)

Swift/Xcode projects (`OffTheCloud.xcodeproj` in each) that consume the same generated Swift protobuf
messages (`make pb` copies `messages.pb.swift` into both) to talk to a device or the bridge over the
same `/ws` protocol.

`app/android` (issue #88) is the Android app: Kotlin + Jetpack Compose, a 1:1 port of the iOS app
kept in its own directory so the two are worked on independently - port screen by screen, keeping
the iOS file/type names (SwiftUI view ↔ Composable, ObservableObject ↔ ViewModel, Keychain ↔
EncryptedSharedPreferences, PhotoKit ↔ MediaStore, BGTaskScheduler ↔ WorkManager, APNs ↔ FCM).
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
push notifications (FCM). One deliberate difference: the photo sync's cloud-id shortcut (release
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
the status bar and never get the tap.

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

**Follow this whenever a change is worth shipping.** A change that is only on `main` has
not reached anybody: the version number in the manifest is the only signal a device has
that anything is new.

```bash
# 1. Pick the next version number (the manifest's last line + 1).
N=2

# 2. Only if this release needs a schema or config change, write its script.
#    Most releases don't. It runs as root, on the primary and on every
#    per-user database, and MUST be idempotent - a device with no
#    /etc/otc/version runs the whole history from 1.
vim scripts/updates/$N.sh
SCRIPT_SHA=$(shasum -a 256 scripts/updates/$N.sh | awk '{print $1}')   # or "-" if there is no script

# 3. Build the web bundle and package it. Every release should ship this,
#    even a backend-only one: the device installs the assets belonging to
#    the version it is moving to, so skipping it leaves a device running
#    new server code behind an older UI.
npm run build --prefix web
tar -czf web-dist.tar.gz -C web/dist .
ASSETS_SHA=$(shasum -a 256 web-dist.tar.gz | awk '{print $1}')

# 4. Add the manifest line: version, script sha, assets sha, summary.
#    The summary is shown in Settings - write it for whoever is deciding
#    whether to press Update.
printf '%s\t%s\t%s\t%s\n' "$N" "$SCRIPT_SHA" "$ASSETS_SHA" "What changed" \
    >> scripts/updates/VERSIONS

# 5. Commit and push to main. The manifest is read from main, so this is
#    what makes the release visible to devices.
git add -A && git commit -m "Release $N: what changed" && git push

# 6. Tag that commit and publish the release with the assets attached.
#    The device downloads the source by tag, so the tag must exist and must
#    include the manifest line above.
git tag "v$N" && git push origin "v$N"
gh release create "v$N" web-dist.tar.gz --title "v$N" --notes "What changed"
```

Order matters: the manifest must be on `main` before the tag, the tag must exist before a
device tries to update, and the checksums must be of the exact files published — the
device verifies both before running a script as root or replacing the web app.

**Checking it worked**: a device shows its version under Settings → Device version, and
the full log of any run is at `/var/log/otc/update.log`, with the current state in
`/var/lib/otc/update-status.json`. A failed run leaves `/etc/otc/version` on the last
release that fully applied, so re-running resumes from there; a failed build leaves the
running binary untouched.

**Forks** set `[otc] update-repo` and `[otc] update-releases` so a device updates from its
own repository rather than silently taking code from upstream.

## Config

Runtime config is INI, loaded via `cfg.Init(appName, env)`: it reads `etc/otc_<env>.ini` relative to
the working directory, or `/etc/otc_<env>.ini` if that's missing. Dev config lives at
`cfg/etc/config_dev.ini`; see `README.md` for the full annotated `[otc]`, `[otc-api]`, `[mysql]`,
`[logger]`, `[tagger]`, `[faces]` sections used in production on the Pi.

## VERY IMPORTANT NOTES
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
