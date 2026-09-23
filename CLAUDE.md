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
page polls `GET /api/setup-lookup`. Two Pi 5 pitfalls learnt the hard way: NetworkManager's WiFi
switch ships off on stock Raspberry Pi OS (`nmcli radio wifi on`), and an idle `wlan0` reports
channel 34 (5170 MHz) - never copy a channel from an interface that isn't connected. The wizard also handles a re-imaged
device: it assembles any existing RAID1 array (`mdadm` is the one package baked into the image)
and, if it holds an OTC database, offers recovery - `install.sh` reassembles instead of wiping and
the identity in that database wins, so no name is asked; after installing it polls the bridge's
`/api/device-online` and only asks for a name if the recovered device never shows up. It contains
no otc code, so it only needs rebuilding when those two scripts change.

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

**Web app** (`web/`, Vite + React 19 + TypeScript + react-router):
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
  `shared_links`, `vault`, `events`, `people`, `faces`, `image_groups` + `image_group_files`). Business logic in other packages should go
  through `dao`, not raw SQL.
- `files_manager` — file storage, hashing, dedup on disk.
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
gitignored like `proto/generated`. Because those generators emit one file per type, no two proto
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
push notifications (FCM), the logo asset in the feed header, and a map in the EXIF panel.

## Cutting a release (issue #94)

Devices update themselves in place: an owner presses **Update** in Settings and the
device applies every release it is missing, in order, then rebuilds its own binary and
restarts. Nothing is cross-compiled and no binaries are distributed — the device builds
from source, which is what keeps any architecture supported. The web app is the
exception: devices have no Node, so the bundle is built once, here, and attached to the
release.

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
- All the sensible content like photographies or files of any kind uploaded by the user should be encrypted at rest
- No sensible communications should be shared over unsecure channels
- Security is our main prioirty then reliability, durability and performance
- Every time that a make is done, update the documentation, installation scripts, images and any other necessary parts
- Test as much as possible in the browser or the iOS and Android simulators
- I'm working on other machine, so before working on something, do a `git pull` to update from what we have in the repo
