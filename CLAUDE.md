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
make web      # npm run build (web/) then copy dist/ into ios app, bridge static, and scp to TARGET
make pb       # regenerate proto/generated/*.go, web/src/proto/*.ts, and app/*/OffTheCloud/*.pb.swift from proto/messages.proto
make sync     # rsync the whole repo to the device (excludes handled by rsync flags)
make clean    # remove generated protobuf, the otc binary, and ios web-dist
```

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
- `make pb` needs `npx protoc` with the Go, Go-gRPC, ts-proto, and Swift protoc plugins available.

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
  `shared_links`, `vault`, `events`). Business logic in other packages should go through `dao`, not
  raw SQL.
- `files_manager` — file storage, hashing, dedup on disk.
- `images_tagger` — runs the RAM++ ONNX model (paths from `[tagger]` config) to auto-tag photos;
  requires CGO + libonnxruntime at runtime (see Build section).
- `bg_processor` — background job runner invoked from `files_manager`/`websocket`.
- `websocket` — the `/ws` connection handler and dispatch switch described above; also owns
  `OpenBridge()`, which the device uses to dial *out* to the bridge relay (`[otc] bridge-addr`,
  `bridge-connections`) so the bridge can reach an otherwise unreachable home device.
- `social`, `session`, `settings`, `profile`, `status` — feature-specific logic (social feed/friend
  sync, auth sessions, device settings, owner profile, RAID/disk/CPU status) sitting between
  `websocket` and `dao`.
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
static dir, the bridge's static landing dir, and the iOS app's bundled web assets — the same web build
is reused across device, bridge, and iOS embedded web view.

### Native apps (`app/ios`, `app/macos`)

Swift/Xcode projects (`OffTheCloud.xcodeproj` in each) that consume the same generated Swift protobuf
messages (`make pb` copies `messages.pb.swift` into both) to talk to a device or the bridge over the
same `/ws` protocol.

## Config

Runtime config is INI, loaded via `cfg.Init(appName, env)`: it reads `etc/otc_<env>.ini` relative to
the working directory, or `/etc/otc_<env>.ini` if that's missing. Dev config lives at
`cfg/etc/config_dev.ini`; see `README.md` for the full annotated `[otc]`, `[otc-api]`, `[mysql]`,
`[logger]`, `[tagger]` sections used in production on the Pi.
