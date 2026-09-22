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
    # Written after every join attempt for the setup wizard to read
    # (setup_wizard.py shows "wrong password" from it).
    "result_file": "/var/lib/otc/wifi_join_result.json",
    # Created by the setup wizard once the install has completed. Until it
    # exists the hotspot stays up no matter what - the person doing the
    # setup is on it - even after the device has joined a real network.
    "setup_done_marker": "/var/lib/otc/setup-done",
    "wifi_interface": "wlan0",
    # The hotspot lives on a second, virtual interface on the same radio,
    # so the device is an access point and a WiFi client at once and the
    # person's phone never has to leave the hotspot during setup. One
    # radio means one channel: the owner's network is joined on 2.4GHz
    # only while setting up (lifted afterwards, see lift_band_limit) and
    # the hotspot follows it onto that channel. Verified on a Pi 5.
    "ap_interface": "uap0",
    # The connection profile the join creates, so it can be found again.
    "sta_connection_name": "OTC-WiFi",
    "scan_file": "/var/lib/otc/wifi_scan.json",
    "band_lifted_marker": "/var/lib/otc/wifi-band-lifted",
    "ap_connection_name": "OTC-Setup",
    "ap_ssid": "Off The Cloud",
    "poll_s": 2,
}


def run(cmd, timeout=None):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except Exception as e:
        return subprocess.CompletedProcess(cmd, 1, "", str(e))


_wifi_unblocked = False


def ensure_wifi_unblocked():
    """Raspberry Pi's WiFi radio ships regulatory soft-blocked (rfkill)
    until a wireless country is set — normally satisfied by Raspberry Pi
    Imager's own "Customisation" step, which this image's whole point is
    to not need. Found the hard way on real Pi 5 hardware: `wifi:
    unavailable` in `nmcli device status` and an explicit "Wi-Fi is
    currently blocked by rfkill" login banner meant the radio was off at
    the driver level, so nothing here — AP mode included — could ever
    have worked regardless of any other fix. `00` is the generic "world"
    regulatory domain: conservative (reduced channels/power) but legal
    everywhere, which is the right default for a device that doesn't know
    where it physically is yet. The owner can set their real country
    later from Settings for full channel access.
    """
    global _wifi_unblocked
    if _wifi_unblocked:
        return
    # NetworkManager may still be coming up when this service starts
    # (the unit only orders after it, and its D-Bus API needs a moment);
    # a "wifi on" that can't reach it is silently lost. Only count the
    # radio as dealt with once NetworkManager itself reports it enabled,
    # so every poll until then tries again.
    # `iw reg set` before `rfkill unblock`: raspi-config's own
    # do_wifi_country does it in this order, and a regulatory domain set
    # while still soft-blocked is still honored once unblocked right after.
    run(["iw", "reg", "set", "00"])
    run(["rfkill", "unblock", "wifi"])
    # Found on real Pi 5 hardware with this image: rfkill was clear, both
    # interfaces existed, and NetworkManager still called them
    # "unavailable" - its own software WiFi switch ships *off* on stock
    # Raspberry Pi OS until Imager or raspi-config sets a country, which
    # this image never does. Nothing WiFi works until it's on.
    run(["nmcli", "radio", "wifi", "on"])
    if run(["nmcli", "-t", "radio", "wifi"]).stdout.strip() == "enabled":
        print("[network-setup] WiFi radio is on")
        _wifi_unblocked = True
    else:
        print("[network-setup] WiFi radio still off (NetworkManager not ready?) - will retry")


def setup_done():
    return Path(CONFIG["setup_done_marker"]).exists()


def ensure_ap_interface():
    """Create the virtual AP interface on top of the real radio if it
    isn't there yet and get NetworkManager to manage it. True when the
    interface is there and NetworkManager knows about it - the hotspot
    falls back to the real interface otherwise (see ensure_ap_mode)."""
    ap, sta = CONFIG["ap_interface"], CONFIG["wifi_interface"]
    if ap == sta:
        return True
    if f"Interface {ap}" not in run(["iw", "dev"]).stdout:
        add = run(["iw", "dev", sta, "interface", "add", ap, "type", "__ap"])
        if add.returncode != 0:
            print(f"[network-setup] could not create {ap}: {add.stderr or add.stdout}")
            return False
        # Its own MAC address: brcmfmac hands the virtual interface the
        # radio's MAC, and two interfaces sharing one address is a known
        # way for AP mode to silently not work. Locally administered
        # (bit 1 of the first byte), derived from the real one.
        try:
            mac = Path(f"/sys/class/net/{sta}/address").read_text().strip().split(":")
            mac[0] = f"{(int(mac[0], 16) | 0x02) ^ 0x04:02x}"
            run(["ip", "link", "set", ap, "address", ":".join(mac)])
        except Exception as e:
            print(f"[network-setup] could not set a MAC on {ap}: {e}")
        run(["ip", "link", "set", ap, "up"])
        print(f"[network-setup] created AP interface {ap}")
    # NetworkManager picks the new interface up asynchronously.
    for _ in range(10):
        devices = run(["nmcli", "-t", "-f", "DEVICE,STATE", "device", "status"]).stdout
        if any(line.startswith(ap + ":") for line in devices.splitlines()):
            run(["nmcli", "device", "set", ap, "managed", "yes"])
            return True
        time.sleep(1)
    print(f"[network-setup] NetworkManager never listed {ap}")
    return False


def sta_channel():
    """(channel, frequency MHz) of the network the client interface is
    *connected to*, or None. One radio means a hotspot on a second
    interface has to sit on the same channel. Only meaningful while
    actually associated: found on a real Pi 5 that an idle wlan0 reports
    channel 34 (5170 MHz), a Japan-only channel no phone will join and
    the driver refuses to start an AP on - which is what "802.1X
    supplicant took too long to authenticate" turned out to mean."""
    import re
    link = run(["iw", "dev", CONFIG["wifi_interface"], "link"]).stdout
    if "Connected to" not in link:
        return None
    m = re.search(r"channel (\d+) \((\d+) MHz", run(["iw", "dev", CONFIG["wifi_interface"], "info"]).stdout)
    if not m:
        return None
    return int(m.group(1)), int(m.group(2))


def prescan_networks():
    """Scan once, while nothing is using the radio, and keep the result
    for the wizard: scanning takes the one radio off the hotspot's
    channel for seconds, which drops the phone's captive-portal sheet.
    2.4GHz networks only - that is the band setup happens on."""
    res = run(["nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,FREQ", "dev", "wifi", "list",
               "ifname", CONFIG["wifi_interface"], "--rescan", "yes"], timeout=60)
    best = {}
    import re
    for line in res.stdout.splitlines():
        parts = re.split(r"(?<!\\):", line)
        if len(parts) < 4:
            continue
        ssid = parts[0].replace("\\:", ":")
        if not ssid or ssid == CONFIG["ap_ssid"]:
            continue
        try:
            signal, freq = int(parts[1]), int(parts[3].split()[0])
        except (ValueError, IndexError):
            continue
        if freq >= 3000:
            continue
        security = parts[2].strip()
        if ssid not in best or best[ssid]["signal"] < signal:
            best[ssid] = {"ssid": ssid, "signal": signal, "secured": security != "", "security": security, "freq": freq}
    networks = sorted(best.values(), key=lambda n: -n["signal"])
    try:
        Path(CONFIG["scan_file"]).parent.mkdir(parents=True, exist_ok=True)
        Path(CONFIG["scan_file"]).write_text(json.dumps({"at": time.time(), "networks": networks}))
        print(f"[network-setup] scanned {len(networks)} 2.4GHz networks")
    except Exception as e:
        print(f"[network-setup] could not save the scan: {e}")


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
    if setup_done():
        # The hotspot is only a fallback from here on; names must resolve
        # normally for whoever is on it.
        (conf_dir / "otc-captive.conf").unlink(missing_ok=True)
        return
    # Kept for the whole setup, even once the device is online: the
    # phone's captive-portal sheet stays open exactly as long as its
    # connectivity probe keeps landing on the wizard, and the sheet is
    # where the person is doing the setup.
    conf_dir.mkdir(parents=True, exist_ok=True)
    # Every name lands on the wizard - except the bridge's own domain,
    # which is forwarded normally so the finish page's link to
    # https://<name>.off-the.cloud works from inside the sheet (the
    # hotspot shares the device's internet connection).
    (conf_dir / "otc-captive.conf").write_text("address=/#/10.42.0.1\nserver=/off-the.cloud/#\n")


def ensure_ap_mode():
    """Bring up the temporary open AP if nothing else is connected and it
    isn't already up. Safe to call every poll: both checks are no-ops once
    the AP (or a real connection) is already active."""
    if ap_is_active():
        return
    # After setup the hotspot is only a fallback for a device that has
    # lost its network; during setup it is how the person reaches the
    # wizard, online or not.
    if setup_done() and has_connectivity():
        return
    if not setup_done() and not Path(CONFIG["scan_file"]).exists():
        prescan_networks()

    # The virtual interface lets the hotspot survive joining the owner's
    # WiFi; if it can't be had, the hotspot still has to appear - on the
    # real interface, as before, where joining a network takes it down.
    iface = CONFIG["ap_interface"] if ensure_ap_interface() else CONFIG["wifi_interface"]
    print(f"[network-setup] starting AP '{CONFIG['ap_ssid']}' on {iface}")
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
    # 2.4GHz channel 6, which every phone can see - unless the hotspot has
    # its own interface and the client side is connected, in which case
    # the one radio forces both onto the client's channel.
    band, channel = "bg", "6"
    sta = sta_channel() if iface != CONFIG["wifi_interface"] else None
    if sta:
        channel = str(sta[0])
        band = "a" if sta[1] >= 4900 else "bg"
    modify = run([
        "nmcli", "connection", "modify", name,
        "802-11-wireless.mode", "ap",
        "802-11-wireless.band", band,
        "802-11-wireless.channel", channel,
        "ipv4.method", "shared",
        "ipv4.addresses", "10.42.0.1/24",
    ])
    if modify.returncode != 0:
        print(f"[network-setup] failed to configure AP connection: {modify.stderr or modify.stdout}")
        return

    up = run(["nmcli", "connection", "up", name], timeout=60)
    if up.returncode != 0 and iface != CONFIG["wifi_interface"]:
        # The virtual interface exists but AP mode on it didn't take:
        # try once more on the real interface rather than show nothing.
        print(f"[network-setup] AP failed on {iface} ({up.stderr or up.stdout}); retrying on {CONFIG['wifi_interface']}")
        run(["nmcli", "connection", "modify", name, "connection.interface-name", CONFIG["wifi_interface"]])
        iface = CONFIG["wifi_interface"]
        up = run(["nmcli", "connection", "up", name], timeout=60)
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

    security = (request.get("security") or "").upper()
    print(f"[network-setup] Joining requested network: {ssid}")
    # A profile rather than `device wifi connect`, so the band can be
    # pinned: while setting up, the owner's network is joined on 2.4GHz
    # only, which is where the hotspot (same radio, same channel) can
    # live under the world regulatory domain. Lifted once setup is done.
    name = CONFIG["sta_connection_name"]
    run(["nmcli", "connection", "delete", name])
    add = ["nmcli", "connection", "add", "type", "wifi", "con-name", name,
           "ifname", CONFIG["wifi_interface"], "ssid", ssid, "802-11-wireless.band", "bg",
           "connection.autoconnect", "yes"]
    if password:
        # WPA3-only networks need SAE; anything with WPA2 (including
        # WPA2/WPA3 transition mode) takes the classic PSK.
        key_mgmt = "sae" if "WPA3" in security and "WPA2" not in security else "wpa-psk"
        add += ["802-11-wireless-security.key-mgmt", key_mgmt, "802-11-wireless-security.psk", password]
    result = run(add, timeout=30)
    ok = result.returncode == 0
    if ok:
        result = run(["nmcli", "connection", "up", name], timeout=90)
        ok = result.returncode == 0
    if not ok:
        run(["nmcli", "connection", "delete", name])
    if not ok:
        print(f"[network-setup] failed to join {ssid}: {result.stderr or result.stdout}")
    else:
        print(f"[network-setup] Joined {ssid}")
    try:
        Path(CONFIG["result_file"]).write_text(json.dumps({
            "ssid": ssid, "ok": ok,
            "error": "" if ok else "Could not join that network - check the password and try again.",
        }))
    except Exception as e:
        print(f"[network-setup] could not write {CONFIG['result_file']}: {e}")

    req_path.unlink(missing_ok=True)

    if ok:
        if setup_done():
            teardown_ap_mode()
        else:
            # Keep serving the person on the hotspot, but re-create it so
            # it follows the client's channel and stops hijacking DNS now
            # that the device is online (see write_captive_dns_config).
            print("[network-setup] setup in progress - restarting the AP alongside the new connection")
            run(["nmcli", "connection", "down", CONFIG["ap_connection_name"]])
            run(["nmcli", "connection", "delete", CONFIG["ap_connection_name"]])
            ensure_ap_mode()


def lift_band_limit():
    """Setup is done and the hotspot is gone: let the owner's network
    connection use 5GHz too. Once."""
    marker = Path(CONFIG["band_lifted_marker"])
    if marker.exists():
        return
    name = CONFIG["sta_connection_name"]
    if name in run(["nmcli", "-t", "-f", "NAME", "connection", "show"]).stdout.splitlines():
        run(["nmcli", "connection", "modify", name, "802-11-wireless.band", ""])
        run(["nmcli", "connection", "up", name], timeout=90)
        print("[network-setup] setup done - WiFi no longer limited to 2.4GHz")
    marker.touch()


def main():
    print("[network-setup] starting")
    ensure_wifi_unblocked()
    while True:
        ensure_wifi_unblocked()
        perform_pending_wifi_join()
        ensure_ap_mode()
        # Once set up, the hotspot is only for a device that has lost its
        # network - drop it as soon as a real connection is back.
        if setup_done() and has_connectivity() and ap_is_active():
            teardown_ap_mode()
        if setup_done() and not ap_is_active():
            lift_band_limit()
        time.sleep(CONFIG["poll_s"])


if __name__ == "__main__":
    main()
