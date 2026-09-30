NO_COLOR=\033[0m
OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

# Issue #144: the bridge runs on every cluster node (bridge/cluster/README.md).
# Built once, then installed and restarted one node at a time, so the other
# keeps serving while each restarts.
NODES ?= bridge1 bridge2

bridge:
	@echo "$(OK_COLOR)==> Compiling...$(NO_COLOR)"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/otc_bridge bin/otc_bridge.go
	@for n in $(NODES); do \
		echo "$(OK_COLOR)==> Deploying to $$n...$(NO_COLOR)"; \
		scp -q bin/otc_bridge ubuntu@$$n:/tmp/otc_bridge && \
		ssh ubuntu@$$n 'sudo install -m 755 /tmp/otc_bridge /usr/bin/otc_bridge && rm /tmp/otc_bridge && sudo systemctl restart otc_bridge' && \
		ssh ubuntu@$$n 'rm -rf /tmp/otc_static && mkdir /tmp/otc_static' && \
		scp -q -r static/* ubuntu@$$n:/tmp/otc_static/ && \
		ssh ubuntu@$$n 'sudo cp -r /tmp/otc_static/. /var/www/ && rm -rf /tmp/otc_static' || exit 1; \
		sleep 5; \
	done

.PHONY: otc

all: bridge
