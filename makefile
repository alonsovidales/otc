NO_COLOR=\033[0m

OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

PROTO_SRC := proto
PROTO_OUT := $(PROTO_SRC)/generated
PROTO_TS_OUT := web/src/proto
PROTOS    := $(notdir $(wildcard $(PROTO_SRC)/*.proto))
TARGET    := pit.otc
# The SSH login on TARGET. A device provisioned by hand (Makefile.pi /
# install.sh run by you) has the `otc` account with your key; a device set
# up from the flashable image (issue #38) only has `otc-debug` (the `otc`
# service account there has no shell), and SSH must first be enabled on
# its console: sudo systemctl enable --now ssh, plus your key in
# ~otc-debug/.ssh/authorized_keys.
PI_USER   ?= otc
#TARGET    := otc

sync:
	#ssh $(PI_USER)@$(TARGET) wget https://github.com/microsoft/onnxruntime/releases/download/v1.22.0/onnxruntime-linux-aarch64-1.22.0.tgz
	#ssh $(PI_USER)@$(TARGET) sudo mkdir -p /opt/onnxruntime/lib
	#ssh $(PI_USER)@$(TARGET) sudo cp onnxruntime-linux-aarch64-1.22.0/lib/*.so* /opt/onnxruntime/lib/
	rsync -avz --delete \
	  --exclude='.git' --exclude='node_modules' --exclude='web/dist' --exclude='dist' --exclude='.env.pi' \
	  ./ $(PI_USER)@$(TARGET):/home/otc/otc/

.PHONY: sync

pi:
	@echo "$(OK_COLOR)==> Building for pi...$(NO_COLOR)"
	ssh $(PI_USER)@$(TARGET) sudo systemctl stop otc
	# PATH gets /usr/local/go/bin appended (not prepended) just for the
	# build command itself, on top of whatever this login shell's own
	# PATH already resolves - a device where `go` is already reachable is
	# unaffected either way. Needed on Cala specifically: `go` is
	# installed there but its login shell's own PATH never picks it up,
	# which used to fail this whole target outright with a plain
	# "go: command not found".
	ssh -tt $(PI_USER)@$(TARGET) 'bash -lc "cd otc && PATH=$$PATH:/usr/local/go/bin CGO_ENABLED=1 go build -o otc ./bin/otc.go && sudo mv otc /usr/bin/"'
	ssh $(PI_USER)@$(TARGET) sudo systemctl start otc

.PHONY: pi

pb:
	@echo "$(OK_COLOR)==> Generating Go files...$(NO_COLOR)"
	mkdir -p $(PROTO_OUT) ./app/android/app/src/main/proto-gen
	# protoc-gen-ts_proto is a web/ dev dependency, protoc-gen-go(-grpc)
	# come from `go install` into $$HOME/go/bin - neither is on a fresh
	# machine's PATH by default.
	PATH="$(CURDIR)/web/node_modules/.bin:$(HOME)/go/bin:$$PATH" npx protoc -I=$(PROTO_SRC) \
	  --go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
	  --go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
	  --ts_proto_out=$(PROTO_TS_OUT) \
	  --ts_proto_opt=enumsAsLiterals=true,oneof=unions,useEnumNamesAsValues=true,esModuleInterop=true,useOptionals=messages,outputServices=none,forceLong=bigint \
  	  --swift_out=./app/ios/OffTheCloud/OffTheCloud/ \
	  --swift_opt=Visibility=Public \
	  --java_out=lite:./app/android/app/src/main/proto-gen/ \
	  --kotlin_out=lite:./app/android/app/src/main/proto-gen/ \
	  $(PROTOS)
	cp ./app/ios/OffTheCloud/OffTheCloud/messages.pb.swift app/macos/OffTheCloud/OffTheCloud/
	@echo "$(OK_COLOR)==> Generated$(NO_COLOR)"

.PHONY: pb

# Translations (docs/i18n.md, i18n/README.md). `make i18n` rewrites the
# catalog's sources in their canonical form and writes every generated
# platform file (committed, like the protobuf bindings). DRAFT=1 also writes
# the draft languages (LANGS=es,fr: only those) and the pseudo-locale, for
# local testing only - that output is marked and `make i18n-check` refuses
# it, so run `make i18n` again before committing. `make i18n-check`
# validates the catalog, fails when a committed generated file is out of
# date and vets the i18n packages; I18N_FLAGS adds -release, -strict,
# -lang or -prefix.
I18N_FLAGS ?=
i18n:
	go run ./i18n/cmd/i18ngen $(if $(DRAFT),-draft) $(if $(LANGS),-lang $(LANGS)) $(I18N_FLAGS)

i18n-check:
	go run ./i18n/cmd/i18ngen -check $(I18N_FLAGS)
	go vet ./i18n/...

.PHONY: i18n i18n-check

# Issue #38: the flashable Raspberry Pi image - stock Raspberry Pi OS Lite
# plus the setup wizard and hotspot scripts (scripts/build_image.sh), so
# it is independent of the software release: the wizard installs the
# current code with scripts/install.sh at setup time. Built on TARGET
# (needs losetup + an arm64 chroot) and copied back here into dist/.
IMAGE_NAME  := off-the-cloud-rpi-lite-arm64
IMAGE_WORK  := /home/otc/image-build
# One rolling GitHub release, tag "image", holds the current image at a
# stable URL: https://github.com/alonsovidales/otc/releases/download/image/$(IMAGE_NAME).img.xz
IMAGE_RELEASE := image

image: sync
	@echo "$(OK_COLOR)==> Building $(IMAGE_NAME) on $(TARGET) (base download + xz: a few minutes)...$(NO_COLOR)"
	ssh $(PI_USER)@$(TARGET) 'sudo -n bash /home/otc/otc/scripts/build_image.sh'
	mkdir -p dist
	scp $(PI_USER)@$(TARGET):$(IMAGE_WORK)/$(IMAGE_NAME).img.xz $(PI_USER)@$(TARGET):$(IMAGE_WORK)/$(IMAGE_NAME).img.xz.sha256 dist/
	@echo "$(OK_COLOR)==> dist/$(IMAGE_NAME).img.xz ready - publish with: make image-publish$(NO_COLOR)"

.PHONY: image

image-publish:
	@test -f dist/$(IMAGE_NAME).img.xz || { echo "dist/$(IMAGE_NAME).img.xz not found - run 'make image' first"; exit 1; }
	@gh release view $(IMAGE_RELEASE) >/dev/null 2>&1 || gh release create $(IMAGE_RELEASE) --title "Raspberry Pi image" \
		--notes "Flash with Raspberry Pi Imager (Use custom, no customisation), boot, open the Off The Cloud app and tap Set up a new device (Bluetooth). Rebuilt whenever the setup scripts change; the software itself is installed at setup time."
	@# Issue #184: the desktop apps' setup wizard trusts the image only through
	@# this signature (release key, like device releases) over its SHA-256.
	scripts/sign-file.sh dist/$(IMAGE_NAME).img.xz.sha256 dist/$(IMAGE_NAME).img.xz.sha256.sig
	gh release upload $(IMAGE_RELEASE) dist/$(IMAGE_NAME).img.xz dist/$(IMAGE_NAME).img.xz.sha256 dist/$(IMAGE_NAME).img.xz.sha256.sig --clobber
	@echo "$(OK_COLOR)==> https://github.com/alonsovidales/otc/releases/download/$(IMAGE_RELEASE)/$(IMAGE_NAME).img.xz$(NO_COLOR)"

.PHONY: image-publish

otc:
	@echo "$(OK_COLOR)==> Compiling...$(NO_COLOR)"
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
	CC=aarch64-unknown-linux-gnu-gcc \
	CGO_CFLAGS="-I$(HOME)/ort-aarch64/onnxruntime-linux-aarch64-1.20.1/include" \
	CGO_LDFLAGS="-L$(HOME)/ort-aarch64/onnxruntime-linux-aarch64-1.20.1/lib -lonnxruntime" \
	go build -o otc ./bin/otc.go && scp otc $(PI_USER)@$(TARGET):/usr/bin/

.PHONY: otc

web:
	@echo "$(OK_COLOR)==> Building web content...$(NO_COLOR)"
	npm run build --prefix web
	@echo "$(OK_COLOR)==> Copying static content...$(NO_COLOR)"
	mkdir -p app/ios/OffTheCloud/web-dist
	cp -a web/dist/* app/ios/OffTheCloud/web-dist/
	scp -r web/dist/* $(PI_USER)@$(TARGET):/var/www/
	# Issue #95: the bridge fetches a device's own web assets straight from
	# it now (see staticassets.Resolve / ReqGetStaticAsset) instead of
	# serving a separate copy of its own - no reason left to also push one
	# to bridge/static/ here. bridge/static/ still holds the bridge's own
	# pages (landing.html, the admin panel) - those are deployed by
	# bridge/makefile's own target, unrelated to a device's web build.

.PHONY: web

# The desktop sync client for Linux and Windows (app/desktop, issues #119
# and #120): pure Go, so it cross-compiles from anywhere. `desktop-mac`
# builds the same program for this Mac, for development only - the real
# macOS client is the Swift app in app/macos. These are development builds
# ("dev": they never update themselves); published builds carry the version
# in app/desktop/VERSION and come from scripts/desktop-release.sh.
DESKTOP_VERSION ?= dev
DESKTOP_LDFLAGS := -s -w -X main.version=$(DESKTOP_VERSION)
desktop:
	@echo "$(OK_COLOR)==> Building the desktop sync client (Linux amd64/arm64, Windows amd64/arm64)...$(NO_COLOR)"
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(DESKTOP_LDFLAGS)" -o dist/otc-sync-linux-amd64 ./app/desktop/cmd/otc-sync
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(DESKTOP_LDFLAGS)" -o dist/otc-sync-linux-arm64 ./app/desktop/cmd/otc-sync
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(DESKTOP_LDFLAGS) -H windowsgui" -o dist/otc-sync-windows-amd64.exe ./app/desktop/cmd/otc-sync
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags "$(DESKTOP_LDFLAGS) -H windowsgui" -o dist/otc-sync-windows-arm64.exe ./app/desktop/cmd/otc-sync
	@ls -la dist/otc-sync-*

desktop-mac:
	go build -ldflags "$(DESKTOP_LDFLAGS)" -o dist/otc-sync-mac ./app/desktop/cmd/otc-sync

# Publishing (website downloads + the apps' self-update): signed, versioned.
# NOTES="what changed" make desktop-publish
desktop-publish:
	@[ -n "$(NOTES)" ] || { echo 'usage: NOTES="what changed" make desktop-publish'; exit 1; }
	scripts/desktop-release.sh "$(NOTES)"

.PHONY: desktop desktop-mac desktop-publish

# Issue #129: the Android app. `android` is the debug APK (adb install -r
# it); `android-release` is the Play bundle, signed with the upload key in
# ~/.otc/otc-upload.jks whose password comes from the Keychain for this one
# build - see docs/play-store/README.md. google-services.json is copied in
# by hand (never committed), or the build has no push notifications.
ANDROID_DIR := app/android
android:
	cd $(ANDROID_DIR) && ./gradlew -q assembleDebug
	@ls -la $(ANDROID_DIR)/app/build/outputs/apk/debug/app-debug.apk

android-release:
	@test -f $(HOME)/.otc/otc-upload.jks || { echo "no upload key at ~/.otc/otc-upload.jks"; exit 1; }
	@test -f $(ANDROID_DIR)/app/google-services.json || echo "warning: app/google-services.json is missing - no push notifications"
	cd $(ANDROID_DIR) && OTC_UPLOAD_PASSWORD="$$(security find-generic-password -s otc-android-upload -a otc-upload -w)" ./gradlew -q bundleRelease
	@ls -la $(ANDROID_DIR)/app/build/outputs/bundle/release/app-release.aab

.PHONY: android android-release

clean:
	@echo "$(OK_COLOR)==> Deletig Protobuf files...$(NO_COLOR)"
	-rm -rf proto/generated/
	@echo "$(OK_COLOR)==> Deletig binary files...$(NO_COLOR)"
	-rm otc
	@echo "$(OK_COLOR)==> Deletig web files...$(NO_COLOR)"
	- rm -rf app/ios/OffTheCloud/web-dist/*

.PHONY: clean

all: clean pb sync web pi
