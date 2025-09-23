NO_COLOR=\033[0m
OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

bridge:
	@echo "$(OK_COLOR)==> Compiling...$(NO_COLOR)"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/otc_bridge bin/otc_bridge.go && \
		ssh ubuntu@off-the.cloud sudo systemctl stop otc_bridge && \
		scp bin/otc_bridge ubuntu@off-the.cloud:/tmp/ && \
		ssh ubuntu@off-the.cloud "sudo mv /tmp/otc_bridge /usr/bin" && \
		ssh ubuntu@off-the.cloud sudo systemctl start otc_bridge
	@echo "$(OK_COLOR)==> Moving statics...$(NO_COLOR)"
	scp -r static/* ubuntu@off-the.cloud:/var/www/

.PHONY: otc

all: bridge
