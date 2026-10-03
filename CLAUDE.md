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
`limactl copy` the `.img.xz` and its `.sha256` into `dist/` for `make image-publish`.

The image (issue #38) is stock Raspberry Pi OS Lite plus `scripts/setup_wizard.py` (a root,
stdlib-only web wizard on port 80: WiFi, device name reserved on the bridge via its public
`/api/name-available` + `/api/claim`, disks, then runs `install.sh` and shows its `[n/10]` steps as
a progress bar) and `scripts/network_setup.py` (the "Off The Cloud" hotspot on a virtual `uap0`
interface, kept up while `wlan0` joins the owner's WiFi - verified on a Pi 5; one radio means one
channel, so the join is pinned to 2.4 GHz during setup and the hotspot follows it, and the band
limit is lifted once setup is done). Over Bluetooth (#137) the hotspot isn't needed: the wizard
tells the two apart by client address (setup_ble.py forwards from loopback), lists both bands,
lets a Bluetooth join use any band (`any_band` in the join request), and network_setup.py
leaves the hotspot down when the joined channel doesn't allow one (`ap_allowed_on`: the world
regulatory domain marks all of 5 GHz "no IR"). A 5 GHz-only network is refused over the hotspot
with a pointer to the app. Networks are scanned once before the hotspot starts (scanning
takes the radio away and drops the phone's captive sheet), and the captive DNS stays on for the
whole setup so the sheet stays open (the bridge's own domain is exempted so the final link works).
Fallback if the phone still loses the page: the device reports its LAN address to the bridge under
a one-time token (`POST /api/setup-beacon`, bridge DB `setup_beacons`, 10-minute expiry) and the
page polls `GET /api/setup-lookup`. Recovery safety: an array assembled without an mdadm.conf (the wizard's own check, a fresh
image) comes up as `/dev/md127`, not `md0`; `install.sh` adopts any active array on its disks and
reassembles it as `md0`, never wipes a disk that still has a RAID superblock, and with
`OTC_RECOVERY=1` (the wizard's Recover) refuses to build a fresh array at all - the wizard only
sets `OTC_RAID_CONFIRM_WIPE` on a fresh install. A fourth: the Pi has no battery-backed clock, so an install that starts
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
in for a device - it needs Bluetooth permission for the terminal, which macOS prompts for. Like
the hotspot, the Bluetooth setup is open by design and only exists before the install completes.
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
picture field; WKWebView handles file inputs itself. Several devices can be set up at once from several phones: each advertises
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
`bridge/cluster/README.md`. The old server no longer runs the bridge.

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
installation's `pi`) can still be replaced; release 22's script hands such files to `otc`.
`integrity.go` checks once a day (10 min after start) for rows whose content is
  missing and raises one Alerts entry per change (`.integrity-reported` in the storage path holds
  the last reported set). The missing blobs found on Cala came with its RAID recovery on
  2026-09-23: they were lost on the previous installation, most likely by the re-upload bug fixed
  on 2026-09-03 (34a8c7d), which deleted the old blob while the row kept its hash. Issue #132's upload-only folders live
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
  are streamed into a segmented file under the link's key.
  **Files grid** (release 80): the Files section on the web, iOS and Android switches between the
  list and a grid (remembered per browser/app). The grid shows each photo or video by its
  thumbnail - `GetThumbnails{paths}` answers up to 48 paths (about 8 MB) per request with the
  stored thumbnails, leaving out paths without one - and everything else by a generic labelled
  document icon whose colour says the type (PDF red, DOC blue, XLS green, PPT orange...; the
  mapping is the same in `FileTypeIcon.tsx`, `FileTypeIcon.swift` and `FileTypeIcon.kt` - change
  all three together; no vendor logos).
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
  `pending_analysis` and queues it in the *fast lane* (NumCPU-1 workers): EXIF, decode,
  orientation, thumbnail - one frame for a video. It then moves to the *slow lane* (tags,
  faces; four frames for a video), whose workers only take a job while the fast lane has none
  queued or running, so during a big sync every thumbnail comes first. `processMedia(...,
  stages)` is the one pipeline (`processMediaContent` = both stages from one decode, for
  backfill and Reprocess, and clears the pending row). The queue itself is memory, but
  `pending_analysis` holds only hashes, so after a restart - when nothing can be decrypted
  until the owner's key is back - `ResumePendingAnalysis` refills the lanes at the first
  sign-in (`startBackfillOnce`, before `BackfillMissingThumbnails`, which skips those hashes).
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
  Issue #166 (release 63): a share link is downloaded in parts (the web page asks for 4 MiB
  ranges; a whole-archive `DownloadSharedLink` is refused above 4 MiB), and every reply carrying
  file content holds the content budget until it is on the wire (`connHandler.reserveMemory`:
  `GetFile`, `GetPublicationMedia`, share-link parts; `GetFileInfo` reserves while it reads a
  photo). A post's video is never loaded: `ExportVideoForPost` re-encodes it from the loopback
  stream straight into the posts' directory (named by hash) or decrypts the original there a
  segment at a time, one transcode at a time (`transcodeSlots`, taken before the stream token);
  a post's photos are read one at a time, within the budget.
- `images_tagger` — runs the RAM++ ONNX model (paths from `[tagger]` config) to auto-tag photos;
  requires CGO + libonnxruntime at runtime (see Build section).
- `modelserver` — issue #167: the primary instance loads RAM++ and the face models once and
  serves them on `models.sock` in its working directory (0600, gob over a Unix socket, the
  full-resolution image as RGBA so results match local inference); the supervisor sets
  `OTC_MODELS_SOCKET` on each child, which then uses `modelserver.Client` for
  `files_manager`'s `Tagger`/`FaceDetector` instead of loading its own ~870 MB copy.
- `face_recognition` — (matching, issue #173: every face row is kept, but new faces are matched
  against at most 20 decrypted *reference* embeddings per person cached in memory -
  `files_manager/face_refs.go`; at 20 an outlier isn't added and the most redundant reference is
  dropped, so the set stays varied; the cover medoid is over the references;
  `InvalidateFaceRefs` after Reprocess, person delete/merge) issue #52's "People" search: detects faces (YuNet) and embeds them (SFace)
  via `gocv`, humans only. `files_manager.processFaces` (called from the slow processing lane) gates this on `settings.face_recognition_enabled` (off unless the owner turns it on - in Settings or the setup wizard; release 43 made the column default 0 again, issue #178, since faces are biometric data) checked *at upload
  time* - enabling it later never retroactively processes anything already in the library, by
  design (see the `faces` table's doc comment in `db.sql`). Requires CGO + a real OpenCV install at
  build time (see Build section); optional at runtime like APNs - a device with `[faces]`
  unconfigured just has the feature unavailable, nothing else affected.
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
- The bridge shared secret never reaches a client (release 66): `GetSettings` leaves it out and
  `SetBridgeSecret`/`RegenerateBridgeSecret` are refused - the device pairs and rotates it itself
  (`regenerateBridgeSecret`), and no Settings screen shows or edits it.
- `push` — Web Push (per-device VAPID keys) and iOS pushes. The APNs auth key is the developer
  team's private key and lives **only on the bridge**: a device never has an `[apns]` section, it
  relays title/body to the bridge (`BridgeNotify`), which sends to the tokens that device itself
  registered - so a device can only ever reach its own phones.
- `api` — the small HTTP layer: healthcheck, the `/ws` upgrade, and static file serving (serves
  `web/dist` copied to the device's static path; appends `.html` to extensionless paths for
  client-side routing).
- `log` — leveled logger with size-based rotation, configured once in `main()` from `[logger]`.
- **Plaintext never on the SD card** (issue #156, releases 62/65; devices build `go build ./bin/otc.go` - one file - so `bin/` must stay a single file and helpers live in packages like `hardening`): the service `mlockall`s its memory at
  start (`hardening/hardening_linux.go`; the unit has `LimitMEMLOCK=infinity` and `LimitCORE=0`; `[otc]
  disable-mlock=true` turns it off), swap is zram only (`/etc/rpi/swap.conf.d/90-otc-ram-only.conf`,
  no `/var/swap` writeback; dphys-swapfile removed; Makefile.pi's `swap` is no longer in bootstrap),
  and `TMPDIR` is a per-process `otc-<pid>` directory on a tmpfs (`/tmp` when it is one, else
  `/dev/shm`), with dead processes' directories swept at start. At `level=info` the log never names
  a file path, search term, share path or Wi-Fi network - those are Debug only - errors name hashes,
  and no request is dumped whole (a friendship secret once was).

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
notice is `bridge/static/privacy.html` (`/privacy`), linked from the landing footer, the account
page, BridgePanel and the apps' sign-in and Settings; contact messages are pruned after a year.

Email (`bridge/mailer`, `[smtp]` in the bridge's ini: host `smtp.protonmail.ch`, port 587,
username/from `info@off-the.cloud`, `password-file=/etc/otc/smtp-token` - a Proton SMTP token,
0600 for the service's user, pushed from the Mac's Keychain item `otc-bridge-smtp`, never in the
ini or the repo): verification is mandatory - an email sign-up gets a link (`#verify=` in the URL
fragment, 48 h, only its SHA-256 in `account_email_tokens`) and `IssueSetupToken`,
`AccountForSetupToken` and manual name registration refuse an unverified account; Google/Apple
accounts are verified by the provider, and linking one verifies an email account; accounts from
before migration 007 were kept verified. `?for=setup` sign-in/sign-up of an unverified account
answers `verify_email: true` (and resends the link on a sign-in); the setup wizard shows "Confirm
your email" and signs in again. Password reset: `/api/account/forgot` (same answer for any
email) mails a one-hour `#reset=` link; `/api/account/reset` sets the password, verifies the
email and ends every other session.

Issue #163 (request limits, `bridge/limits`): every JSON body goes through `limits.DecodeJSON`
(64 KB, 15 s to arrive - the servers bound only headers, since a whole-request `ReadTimeout`
would also cut the websockets the cluster router proxies); one-off device GETs (static assets,
`/media`) are limited per address (`oneOffPerAddr`, 10/s, burst 60 - the forwarded address for a
cluster hop) and per device to `cOneOffConcurrent` (3) in flight, so they can't drain a device's
pool; `auth_events` takes one row per address and reason a minute; relays read only an
envelope's id (`envelopeID`, protowire) instead of unmarshalling every frame; store errors reach
clients as `cInternalErrorMsg`; sign-ups are limited to 5 an hour per address, sign-in answers
the same for unknown, wrong and Google/Apple-only accounts, and new bridge passwords use bcrypt
cost 12 (`limits.BcryptCost`; account hashes are upgraded at the next sign-in).

### Bridge accounts (`bridge/accounts`, issue #124)

Every domain registered on the bridge belongs to an account (`devices.account_id`). The package
owns identity and sessions: email+password sign-up/sign-in (bcrypt, per-address throttling),
Google and Apple as plain OpenID Connect authorization-code flows in `oidc.go` (no SDK: the
provider's JWKS is fetched and cached, the id_token verified with `golang-jwt/jwt/v5`; Apple's
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
registers an unknown domain on dial-in unless open registration is on. The account page is
`bridge/static/account.html` (plain JS over `/api/account/*`). The wizard's account step goes
through the device (`/api/account` in `setup_wizard.py` proxies sign-in/sign-up with
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
iOS/Android. There are three kinds of folder, each explained in the app (the Mac's
`AddFolderChooser` with an (i) per option; the tray's tooltips and "What Do These Do?"):
**backup** (one way, `TrackedFolder` / `config.Folder{OneWay: true}`, `otc-sync backup`: new,
changed and deleted files go up; nothing on the device ever changes the folder), and two
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
the checking pass shows "Checking i/N · name" while it runs (#138). Next to the storage health both show how full the device is and its load: on the Mac "Storage healthy · 15% used" on one line (`StorageStatusView`) with CPU and memory in a pop-up on hover (`DeviceLoadView`); in otc-sync a text bar in the storage line (`storageTitle`) whose submenu, opened on hover, holds CPU and memory (`config.State.StorageUsed`/`CPUPercent`/`MemUsed`, from the same status poll). In a two-way pass an entry the device lists without a hash (content lost, #141) is uploaded from the local copy and never downloaded - taken for a device-side change, it failed on every pass and flooded Alerts. A folder at rest that starts a pass keeps showing it is synced for 1.5 s (`setState` in both, `quietPassDelay`), so a pass that sends one changed file doesn't flash "99%". The status reports storage for the storage path's disk (the OS disk when there is none) in units of 1.024 MB. Test on Linux with the Lima VM `otc` (`limactl shell otc`, `limactl copy
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
push notifications (FCM). Release builds (issue #129, `docs/play-store/README.md`): R8 minify and resource shrinking on,
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
Two more root runners follow the same trigger-file shape, and update.sh reinstalls all of them on
every update: `scripts/bridge-runner/` (switching the bridge on later, #145) and
`scripts/tailscale-runner/` (release 50). tailscaled runs only while Tailscale Funnel is on:
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
be idempotent). The tool builds the web bundle, tags `vN`, makes the release's own source
archive (`src.tar.gz`, `git archive` + `gzip -n`; the manifest and its signature are
`export-ignore`d in `.gitattributes`, since they carry its hash), appends the manifest line
(version, script sha, web sha, summary, source sha), signs the whole manifest with the
release key into `scripts/updates/VERSIONS.sig`, pushes, publishes the GitHub release with
both archives and checks what was published. The key is Ed25519 at
`~/.otc/otc-release-signing.pem` on the Mac mini (passphrase in the Keychain item
`otc-release-signing`; never on GitHub - back it up offline); its public half is
`scripts/release-signing.pub`, which devices pin in `/etc/otc/release-signing.pub`.

Issue #183: a release is **minor** (default), **major** (`--major`: security, stability or
durability) or **critical** (`--critical`: breaks compatibility with the bridge or the apps if not
installed, or a serious security fix), and has a `major.minor` version: a minor adds to the minor
number, a major or critical starts the next major. Release 84 is 1.0; earlier ones have no version
and show as builds. Kinds and versions are in `scripts/updates/RELEASES` (`<release> <kind>
<version>`), signed into `RELEASES.sig` by `release.sh` and checked by the device with Go's Ed25519
(`updater/kinds.go`) - a separate file because the runners read `VERSIONS` by position. The main
instance checks for updates by itself every 6 hours (`updater.Watch`): a pending major or critical
update adds one `Update` notification per version, and `Status.update_alert` carries it to every
app; a critical one shows a banner in the web app, iOS and Android (and a line in the Mac app and
otc-sync) until it is installed. Settings shows "1.1 (build 85)" and badges pending releases.

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
(`fetch_pinned`), and installs Tailscale from its apt repository with the key pinned by hash.

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
