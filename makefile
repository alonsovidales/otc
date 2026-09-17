NO_COLOR=\033[0m

OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

PROTO_SRC := proto
PROTO_OUT := $(PROTO_SRC)/generated
PROTO_TS_OUT := web/src/proto
PROTOS    := $(notdir $(wildcard $(PROTO_SRC)/*.proto))
TARGET    := pit.otc
#TARGET    := otc

sync:
	#ssh otc@$(TARGET) wget https://github.com/microsoft/onnxruntime/releases/download/v1.22.0/onnxruntime-linux-aarch64-1.22.0.tgz
	#ssh otc@$(TARGET) sudo mkdir -p /opt/onnxruntime/lib
	#ssh otc@$(TARGET) sudo cp onnxruntime-linux-aarch64-1.22.0/lib/*.so* /opt/onnxruntime/lib/
	rsync -avz --delete \
	  --exclude='.git' --exclude='node_modules' --exclude='web/dist' --exclude='.env.pi' \
	  ./ otc@$(TARGET):/home/otc/otc/

.PHONY: sync

pi:
	@echo "$(OK_COLOR)==> Building for pi...$(NO_COLOR)"
	ssh otc@$(TARGET) sudo systemctl stop otc
	# PATH gets /usr/local/go/bin appended (not prepended) just for the
	# build command itself, on top of whatever this login shell's own
	# PATH already resolves - a device where `go` is already reachable is
	# unaffected either way. Needed on Cala specifically: `go` is
	# installed there but its login shell's own PATH never picks it up,
	# which used to fail this whole target outright with a plain
	# "go: command not found".
	ssh -tt otc@$(TARGET) 'bash -lc "cd otc && PATH=$$PATH:/usr/local/go/bin CGO_ENABLED=1 go build -o otc ./bin/otc.go && sudo mv otc /usr/bin/"'
	ssh otc@$(TARGET) sudo systemctl start otc

.PHONY: pi

pb:
	@echo "$(OK_COLOR)==> Generating Go files...$(NO_COLOR)"
	mkdir -p $(PROTO_OUT)
	npx protoc -I=$(PROTO_SRC) \
	  --go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
	  --go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
	  --ts_proto_out=$(PROTO_TS_OUT) \
	  --ts_proto_opt=enumsAsLiterals=true,oneof=unions,useEnumNamesAsValues=true,esModuleInterop=true,useOptionals=messages,outputServices=none,forceLong=bigint \
  	  --swift_out=./app/ios/OffTheCloud/OffTheCloud/ \
	  --swift_opt=Visibility=Public \
	  $(PROTOS)
	cp ./app/ios/OffTheCloud/OffTheCloud/messages.pb.swift app/macos/OffTheCloud/OffTheCloud/
	@echo "$(OK_COLOR)==> Generated$(NO_COLOR)"

.PHONY: pb

otc:
	@echo "$(OK_COLOR)==> Compiling...$(NO_COLOR)"
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
	CC=aarch64-unknown-linux-gnu-gcc \
	CGO_CFLAGS="-I$(HOME)/ort-aarch64/onnxruntime-linux-aarch64-1.20.1/include" \
	CGO_LDFLAGS="-L$(HOME)/ort-aarch64/onnxruntime-linux-aarch64-1.20.1/lib -lonnxruntime" \
	go build -o otc ./bin/otc.go && scp otc otc@$(TARGET):/usr/bin/

.PHONY: otc

web:
	@echo "$(OK_COLOR)==> Building web content...$(NO_COLOR)"
	npm run build --prefix web
	@echo "$(OK_COLOR)==> Copying static content...$(NO_COLOR)"
	mkdir -p app/ios/OffTheCloud/web-dist
	cp -a web/dist/* app/ios/OffTheCloud/web-dist/
	scp -r web/dist/* otc@$(TARGET):/var/www/
	# Issue #95: the bridge fetches a device's own web assets straight from
	# it now (see staticassets.Resolve / ReqGetStaticAsset) instead of
	# serving a separate copy of its own - no reason left to also push one
	# to bridge/static/ here. bridge/static/ still holds the bridge's own
	# pages (landing.html, the admin panel) - those are deployed by
	# bridge/makefile's own target, unrelated to a device's web build.

.PHONY: web

clean:
	@echo "$(OK_COLOR)==> Deletig Protobuf files...$(NO_COLOR)"
	-rm -rf proto/generated/
	@echo "$(OK_COLOR)==> Deletig binary files...$(NO_COLOR)"
	-rm otc
	@echo "$(OK_COLOR)==> Deletig web files...$(NO_COLOR)"
	- rm -rf app/ios/OffTheCloud/web-dist/*

.PHONY: clean

all: clean pb sync web pi
