NO_COLOR=\033[0m
OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

PROTO_SRC := proto
PROTO_OUT := $(PROTO_SRC)/generated
PROTOS    := $(notdir $(wildcard $(PROTO_SRC)/*.proto))

pb:
	@echo "$(OK_COLOR)==> Generating files...$(NO_COLOR)"
	mkdir -p $(PROTO_OUT)
	protoc -I=$(PROTO_SRC) \
	  --go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
	  --go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
	  $(PROTOS)
	@echo "$(OK_COLOR)==> Generated$(NO_COLOR)"

.PHONY: pb

otc:
	@echo "$(OK_COLOR)==> Compiling...$(NO_COLOR)"
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build bin/otc.go && scp otc otc@otc:/tmp/
	@echo "$(OK_COLOR)==> Done...$(NO_COLOR)"

.PHONY: otc

clean:
	@echo "$(OK_COLOR)==> Deletig Protobuf files...$(NO_COLOR)"
	-rm -rf proto/generated/
	@echo "$(OK_COLOR)==> Deletig binary files...$(NO_COLOR)"
	-rm otc

.PHONY: clean

all: clean pb otc
