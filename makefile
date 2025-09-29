NO_COLOR=\033[0m

OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

PROTO_SRC := proto
PROTO_OUT := $(PROTO_SRC)/generated
PROTO_TS_OUT := web/src/proto
PROTOS    := $(notdir $(wildcard $(PROTO_SRC)/*.proto))

sync:
	# ssh otc@otc wget https://github.com/microsoft/onnxruntime/releases/download/v1.22.0/onnxruntime-linux-aarch64-1.22.0.tgz
	# ssh otc@otc sudo mkdir -p /opt/onnxruntime/lib
	# ssh otc@otc sudo cp onnxruntime-linux-aarch64-1.20.1/lib/*.so* /opt/onnxruntime/lib/
	rsync -avz --delete ./ otc@otc:/home/otc/otc/

.PHONY: sync

pi:
	@echo "$(OK_COLOR)==> Building for pi...$(NO_COLOR)"
	ssh otc@otc sudo systemctl stop otc
	ssh -tt otc@otc 'bash -lc "cd otc; CGO_ENABLED=1 go build -o otc ./bin/otc.go && sudo mv otc /usr/bin/"'
	ssh otc@otc sudo systemctl start otc

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
	go build -o otc ./bin/otc.go && scp otc otc@otc:/usr/bin/

.PHONY: otc

web:
	@echo "$(OK_COLOR)==> Building web content...$(NO_COLOR)"
	npm run build --prefix web
	@echo "$(OK_COLOR)==> Copying static content...$(NO_COLOR)"
	cp -a web/dist/* app/ios/OffTheCloud/web-dist/
	cp -a web/dist/* bridge/static/
	scp -r web/dist/* otc@otc:/var/www/

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
