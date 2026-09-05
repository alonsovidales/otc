#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
First-boot WiFi setup for OTC devices (issue #38).

The otc service (see network/network.go) runs unprivileged and can safely
*scan* for networks (nmcli's read-only queries need no special rights), but
actually bringing up an access point or joining a network needs root. This
script — installed as its own root-owned systemd service, same pattern as
raid_watch.py for storage — does that privileged half:

  - If the device has no working network connection (and isn't already
    broadcasting one), it brings up a temporary open WiFi network named
    "Off The Cloud" so a phone/laptop can join it and reach the device's
    own setup wizard.
  - It polls CONFIG["request_file"] for a join request written by the otc
    service (network.RequestJoin) — {"ssid": ..., "password": ...} — and
    when one shows up, tries to join that real network. On success, the
    temporary AP is torn down since it's no longer needed.

Run as root (a normal user's `nmcli` can scan but can't create/activate
connections). Requires NetworkManager (nmcli) — the default network stack
on Raspberry Pi OS since Bookworm, so no extra packages need installing on
a device that has no internet yet to install them with.
"""

import json
import os
import subprocess
import sys
import time
from pathlib import Path

sys.stdout.reconfigure(line_buffering=True)

CONFIG = {
    "request_file": "/var/lib/otc/wifi_join_request.json",
    "wifi_interface": "wlan0",
    "ap_connection_name": "OTC-Setup",
    "ap_ssid": "Off The Cloud",
    "poll_s": 2,
}


def run(cmd):
    try:
        return subprocess.run(cmd, capture_output=True, text=True)
    except Exception as e:
        return subprocess.CompletedProcess(cmd, 1, "", str(e))


def has_connectivity():
    """True if some connection (WiFi, ethernet, whatever) is actually up —
    not just that a radio is powered on."""
    res = run(["nmcli", "-t", "-f", "STATE", "general", "status"])
    return res.stdout.strip() == "connected"


def ap_is_active():
    res = run(["nmcli", "-t", "-f", "NAME", "connection", "show", "--active"])
    names = res.stdout.strip().splitlines()
    return CONFIG["ap_connection_name"] in names


def write_captive_dns_config():
    """NetworkManager's shared-mode connections run their own internal
    dnsmasq, which by default just forwards DNS queries upstream — with no
    upstream (we're offline, that's the whole reason the AP exists),
    lookups just fail rather than resolving. Without this, a phone/laptop
    joining the AP can't even resolve the domain names iOS/Android/Windows
    use to detect a captive portal, so the automatic "Sign in to network"
    popup never fires — the person has to know to open a browser and type
    an address manually. Dropping a wildcard rule here (read when NM starts
    the shared dnsmasq instance, i.e. before `connection up`) resolves
    every hostname to the AP's own gateway IP instead, so those probe
    requests actually reach otc's web server. See ensure_ap_mode's pinned
    ipv4.addresses for why 10.42.0.1 specifically.
    """
    conf_dir = Path("/etc/NetworkManager/dnsmasq-shared.d")
    conf_dir.mkdir(parents=True, exist_ok=True)
    (conf_dir / "otc-captive.conf").write_text("address=/#/10.42.0.1\n")


def ensure_ap_mode():
    """Bring up the temporary open AP if nothing else is connected and it
    isn't already up. Safe to call every poll: both checks are no-ops once
    the AP (or a real connection) is already active."""
    if has_connectivity() or ap_is_active():
        return

    print(f"[network-setup] No working connection — starting AP '{CONFIG['ap_ssid']}'")
    iface = CONFIG["wifi_interface"]
    name = CONFIG["ap_connection_name"]

    write_captive_dns_config()

    # Reuse a stale profile from a previous boot rather than erroring on
    # "already exists" if one's still lying around.
    run(["nmcli", "connection", "delete", name])

    add = run([
        "nmcli", "connection", "add",
        "type", "wifi",
        "ifname", iface,
        "con-name", name,
        "autoconnect", "no",
        "ssid", CONFIG["ap_ssid"],
    ])
    if add.returncode != 0:
        print(f"[network-setup] failed to create AP connection: {add.stderr or add.stdout}")
        return

    # 802-11-wireless-security is deliberately left unset — no keys means
    # an open network, matching the "flash it, join one open network,
    # fill in a form" setup flow (issue #38). ipv4.method=shared makes
    # NetworkManager run its own DHCP/NAT for whoever joins; the address is
    # pinned (rather than left to NM's own shared-mode default, which
    # happens to also be 10.42.0.1/24 today but isn't a documented
    # guarantee) so it's certain to match write_captive_dns_config's
    # wildcard target above.
    modify = run([
        "nmcli", "connection", "modify", name,
        "802-11-wireless.mode", "ap",
        "802-11-wireless.band", "bg",
        "ipv4.method", "shared",
        "ipv4.addresses", "10.42.0.1/24",
    ])
    if modify.returncode != 0:
        print(f"[network-setup] failed to configure AP connection: {modify.stderr or modify.stdout}")
        return

    up = run(["nmcli", "connection", "up", name])
    if up.returncode != 0:
        print(f"[network-setup] failed to start AP: {up.stderr or up.stdout}")
    else:
        print(f"[network-setup] AP '{CONFIG['ap_ssid']}' is up on {iface}")


def teardown_ap_mode():
    name = CONFIG["ap_connection_name"]
    if not ap_is_active():
        return
    print(f"[network-setup] Tearing down AP '{name}' — real network is up")
    run(["nmcli", "connection", "down", name])
    run(["nmcli", "connection", "delete", name])


def perform_pending_wifi_join():
    """Reads CONFIG['request_file'] (written by the otc service via
    network.RequestJoin) and tries to join that network, then removes the
    request file either way — a bad password isn't something retrying
    on its own will ever fix, so this doesn't loop on failure; the owner
    just tries again from the setup UI, which writes a fresh request."""
    req_path = Path(CONFIG["request_file"])
    if not req_path.exists():
        return

    try:
        request = json.loads(req_path.read_text())
    except Exception as e:
        print(f"[network-setup] could not parse {req_path}: {e}")
        req_path.unlink(missing_ok=True)
        return

    ssid = request.get("ssid") or ""
    password = request.get("password") or ""
    if not ssid:
        print("[network-setup] join request had no ssid, ignoring")
        req_path.unlink(missing_ok=True)
        return

    print(f"[network-setup] Joining requested network: {ssid}")
    cmd = ["nmcli", "device", "wifi", "connect", ssid, "ifname", CONFIG["wifi_interface"]]
    if password:
        cmd += ["password", password]

    result = run(cmd)
    if result.returncode != 0:
        print(f"[network-setup] failed to join {ssid}: {result.stderr or result.stdout}")
    else:
        print(f"[network-setup] Joined {ssid}")
        teardown_ap_mode()

    req_path.unlink(missing_ok=True)


def main():
    print("[network-setup] starting")
    while True:
        perform_pending_wifi_join()
        ensure_ap_mode()
        time.sleep(CONFIG["poll_s"])


if __name__ == "__main__":
    main()
