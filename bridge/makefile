NO_COLOR=\033[0m
OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m

# Issue #144: the bridge runs on every cluster node (bridge/cluster/README.md).
# Built once, then installed and restarted one node at a time, so the other
# keeps serving while each restarts.
NODES ?= bridge1 bridge2
# The Fleet tab's agent runs on every host of the cluster, the redis one too
# (bridge/cluster/README.md, "Fleet tab and alerts").
FLEET_HOSTS ?= bridge1 bridge2 redis

# The commit both binaries report in the Fleet tab.
VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/alonsovidales/otc/bridge/fleet.Version=$(VERSION)

# The binary embeds the translations (emails, API errors, public pages:
# docs/i18n.md), so nothing is built or deployed until the root's `make
# i18n-check` passes in release mode - pass I18N_SCAN_FLAGS="-surface ..."
# on a machine without node for the web surface's scanner.
bridge:
	@echo "$(OK_COLOR)==> Checking the translations (make i18n-check)...$(NO_COLOR)"
	@$(MAKE) --no-print-directory -C .. i18n-check I18N_RELEASE=1
	@echo "$(OK_COLOR)==> Compiling...$(NO_COLOR)"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/otc_bridge bin/otc_bridge.go
	@for n in $(NODES); do \
		echo "$(OK_COLOR)==> Deploying to $$n...$(NO_COLOR)"; \
		scp -q bin/otc_bridge ubuntu@$$n:/tmp/otc_bridge && \
		ssh ubuntu@$$n 'sudo install -m 755 /tmp/otc_bridge /usr/bin/otc_bridge && rm /tmp/otc_bridge && sudo systemctl restart otc_bridge' && \
		ssh ubuntu@$$n 'rm -rf /tmp/otc_static && mkdir /tmp/otc_static' && \
		scp -q -r static/* ubuntu@$$n:/tmp/otc_static/ && \
		ssh ubuntu@$$n 'sudo cp -r /tmp/otc_static/. /var/www/ && rm -rf /tmp/otc_static' || exit 1; \
		sleep 5; \
	done
	@$(MAKE) --no-print-directory fleet

# otc-fleet-agent, its unit and this host's drop-in (cluster/fleet/<host>.conf)
# on every host. A host where it can't start yet (the one-time steps in the
# README not done) is reported, and doesn't stop the bridge's deploy.
fleet:
	@echo "$(OK_COLOR)==> Compiling the fleet agent...$(NO_COLOR)"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/otc_fleet_agent ./fleetagent
	@for h in $(FLEET_HOSTS); do \
		echo "$(OK_COLOR)==> Fleet agent on $$h...$(NO_COLOR)"; \
		ssh ubuntu@$$h 'rm -rf /tmp/otc_fleet && mkdir /tmp/otc_fleet' && \
		scp -q bin/otc_fleet_agent cluster/fleet/otc-fleet-agent.service cluster/fleet/$$h.conf ubuntu@$$h:/tmp/otc_fleet/ && \
		ssh ubuntu@$$h "(id -u otc-fleet >/dev/null 2>&1 || sudo useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin otc-fleet) && \
			sudo install -m 755 /tmp/otc_fleet/otc_fleet_agent /usr/local/bin/otc-fleet-agent && \
			sudo install -m 644 /tmp/otc_fleet/otc-fleet-agent.service /etc/systemd/system/otc-fleet-agent.service && \
			sudo install -D -m 644 /tmp/otc_fleet/$$h.conf /etc/systemd/system/otc-fleet-agent.service.d/host.conf && \
			rm -rf /tmp/otc_fleet && sudo systemctl daemon-reload && sudo systemctl enable -q otc-fleet-agent && \
			sudo systemctl restart otc-fleet-agent" \
		|| echo "$(WARN_COLOR)==> The fleet agent did not start on $$h: see bridge/cluster/README.md, Fleet tab and alerts$(NO_COLOR)"; \
	done

.PHONY: bridge fleet

all: bridge
