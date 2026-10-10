#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# SPDX-License-Identifier: AGPL-3.0-or-later
"""
First-boot setup wizard for the OTC image (issue #38).

The flashable image is nothing but stock Raspberry Pi OS plus this service,
the hotspot script (network_setup.py) and their units: everything else a
device needs is installed by scripts/install.sh, fetched fresh from GitHub
at setup time - so there is nothing to keep up to date in the image itself.

This serves a small web page on port 80 (reached over the "Off The Cloud"
hotspot, where captive-portal detection opens it automatically, or at
http://otc.local/ on a wired network) that walks through:

  1. WiFi     - pick a network; the Pi joins it while keeping the hotspot
                up (network_setup.py does the joining), and waits until
                the bridge is reachable.
  2. Storage  - the USB disks found, with sizes: two make a RAID1 mirror,
                none keeps everything on the SD card. If the disks already
                hold an Off The Cloud array with a database on it (a dead
                Pi whose card was re-flashed - the disks survived), the
                person is offered to recover it instead: install.sh
                reassembles the array and the identity in that database
                (device UUID, name, bridge secret) is the one the device
                then presents to the bridge, so no new name is needed.
  3. Name     - fresh devices only: the <name>.off-the.cloud address,
                checked live against the bridge and reserved there, with a
                freshly generated identity, the moment it is confirmed.
  4. Install  - runs install.sh with all of the above; its numbered steps
                drive a progress bar, with the current step's text under
                it and the full log one tap away. Then it waits until the
                bridge reports the device connected - a recovered device
                that never shows up (its name was released, say) is asked
                for a name after all and registered anew.
  5. Done     - sends the browser to https://<name>.off-the.cloud, where the
                app's own first-run wizard asks for the owner password.

Root, standard library only, unauthenticated by design: it runs only on a
device that has nothing on it yet, and disables itself once the install
has completed (systemd also refuses to start it again once
/etc/otc/.install-complete exists).
"""

import base64
import hashlib
import json
import os
import re
import secrets
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.stdout.reconfigure(line_buffering=True)

CONFIG = {
    "port": int(os.environ.get("OTC_SETUP_PORT", "80")),
    "bridge": os.environ.get("OTC_BRIDGE", "off-the.cloud"),
    "install_url": os.environ.get(
        "OTC_INSTALL_URL",
        "https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/verified-install.sh"),
    # install.sh sources this for the device identity instead of
    # generating its own, so the name reserved on the bridge and the
    # identity the device later presents are the same thing.
    "env_file": "/etc/otc/otc-install.env",
    "state_file": "/var/lib/otc/setup-state.json",
    # The owner password, decrypted, for install.sh (root-only, tmpfs); it
    # shreds it once set.
    "owner_password_file": "/run/otc-setup/owner-password",
    # Issue #178: the optional profile and the face-recognition choice, for
    # install.sh's `otc init-profile` (root-only, tmpfs; shredded once set).
    "profile_file": "/run/otc-setup/profile.json",
    "join_request": "/var/lib/otc/wifi_join_request.json",
    "join_result": "/var/lib/otc/wifi_join_result.json",
    # network_setup.py keeps the hotspot up until this exists.
    "setup_done_marker": "/var/lib/otc/setup-done",
    # Issue #137: the wizard's last state, for setup_ble.py to answer a
    # phone that asks after the wizard itself has gone.
    "final_state_file": "/var/lib/otc/setup-final.json",
    "install_complete_marker": "/etc/otc/.install-complete",
    "install_log": "/var/log/otc/setup-install.log",
    "recovery_mount": "/mnt/otc-recovery-check",
    "wifi_interface": "wlan0",
    # Joining the owner's WiFi takes the hotspot down (one radio, one
    # interface), so the page gets this long to tell the person where to
    # continue before the join actually starts.
    "join_delay_s": 2,
    # network_setup.py scans before the hotspot starts and leaves the
    # result here: scanning while a phone is on the hotspot takes the
    # radio away for seconds and drops the captive-portal sheet.
    "scan_file": "/var/lib/otc/wifi_scan.json",
    # The hotspot's own address (network_setup.py pins it): where the
    # captive-portal probes get redirected.
    "ap_address": "10.42.0.1",
    # How long to wait for the freshly installed service to show up on the
    # bridge before calling it offline (it dials in seconds after start).
    "verify_timeout_s": 120,
    "dry_run": os.environ.get("OTC_SETUP_DRY_RUN") == "1",
    "dry_run_offline": os.environ.get("OTC_SETUP_DRY_RUN_OFFLINE") == "1",
}

NAME_RE = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
# Issue #135: the bridge's country list, fetched once per run.
COUNTRIES = {}
STEP_RE = re.compile(r"\[otc-install\] \[(\d+)/(\d+)\] (.*)")
ERROR_RE = re.compile(r"\[otc-install\] ERROR: (.*)")
INFO_RE = re.compile(r"\[otc-install\] (.*)")
# "md0 : active (auto-read-only) raid1 sda[0] sdb[1]" - members are the
# tokens carrying a [slot]; the level and any parenthesised state are not.
MDSTAT_RE = re.compile(r"^(md\d+)\s*:\s*active\b(.*)$")
MEMBER_RE = re.compile(r"^([A-Za-z0-9_/-]+)\[\d+\]")

# BEGIN GENERATED I18N
# Generated by make i18n (i18n/cmd/i18ngen) from i18n/strings, prefixes
# common and wiz: do not edit. Language code -> key -> text, a text being
# a string or a plural's {"one": ..., "other": ...}; shipping languages.
# English has every key, the others their translations: fall back to "en".
I18N = {
    "en": {
        "common.accept": "Accept",
        "common.add": "Add",
        "common.apply": "Apply",
        "common.back": "Back",
        "common.cancel": "Cancel",
        "common.choose": "Choose",
        "common.close": "Close",
        "common.connect": "Connect",
        "common.connecting": "Connecting…",
        "common.create": "Create",
        "common.decline": "Decline",
        "common.delete": "Delete",
        "common.deleting": "Deleting…",
        "common.deselect": "Deselect",
        "common.disconnect": "Disconnect",
        "common.done": "Done",
        "common.download": "Download",
        "common.loading": "Loading…",
        "common.next": "Next",
        "common.ok": "OK",
        "common.open": "Open",
        "common.password": "Password",
        "common.pause": "Pause",
        "common.quit": "Quit",
        "common.refresh": "Refresh",
        "common.remove": "Remove",
        "common.rename": "Rename",
        "common.resume": "Resume",
        "common.save": "Save",
        "common.search": "Search",
        "common.select": "Select",
        "common.send": "Send",
        "common.settings": "Settings",
        "common.share": "Share",
        "common.try_again": "Try again",
        "common.update": "Update",
        "common.updating": "Updating…",
        "common.upload": "Upload"
    }
}
# END GENERATED I18N


def run(cmd, timeout=30):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except Exception as e:  # noqa: BLE001 - reported to the page, never fatal here
        return subprocess.CompletedProcess(cmd, 1, "", str(e))


def read_json(path, default):
    try:
        return json.loads(Path(path).read_text())
    except Exception:  # noqa: BLE001
        return default


def write_json(path, data, mode=0o644):
    atomic_write(path, json.dumps(data), mode)


def atomic_write(path, text, mode=0o644):
    """Root writes into /var/lib/otc, which belongs to the otc user once
    installed: a plain write (or chmod) followed a symlink planted there.
    A fresh temp file (mkstemp: O_EXCL, a random name - several threads
    write at once) with its final mode from the start, renamed into place:
    a rename replaces a symlink instead of following it, and readers (this
    wizard, network_setup.py, setup_ble.py) never see a half-written
    file."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=str(p.parent), prefix=f".{p.name}.", suffix=".tmp")
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "w") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, p)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def human_size(size):
    return f"{size / 1e9:.0f} GB" if size >= 1e9 else f"{size / 1e6:.0f} MB"


# ---------------------------------------------------------------------------
# Connectivity, WiFi
# ---------------------------------------------------------------------------

def bridge_reachable():
    """The one connectivity test that matters: can this device reach the
    bridge it is about to register with."""
    try:
        with urllib.request.urlopen(f"https://{CONFIG['bridge']}/check_healty", timeout=4) as r:
            return r.status == 200
    except Exception:  # noqa: BLE001
        return False


def current_ssid():
    res = run(["nmcli", "-t", "-f", "ACTIVE,SSID,DEVICE", "dev", "wifi", "list", "--rescan", "no"])
    for line in res.stdout.splitlines():
        parts = line.split(":")
        if len(parts) >= 3 and parts[0] == "yes" and parts[2] == CONFIG["wifi_interface"]:
            return parts[1]
    return ""


def scan_wifi(rescan=False):
    """The pre-scan network_setup.py left (see CONFIG["scan_file"]), or a
    live scan when asked for one / there is none. Both bands, each network
    with the bands it was seen on: over the hotspot the join happens on
    2.4GHz, over Bluetooth on either (see network_setup.py)."""
    if not rescan:
        cached = read_json(CONFIG["scan_file"], None)
        if cached and cached.get("networks") is not None:
            return cached["networks"]
    res = run(["nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,FREQ", "dev", "wifi", "list",
               "ifname", CONFIG["wifi_interface"], "--rescan", "yes"], timeout=60)
    best = {}
    for line in res.stdout.splitlines():
        # nmcli -t escapes ':' inside fields as '\:'.
        parts = re.split(r"(?<!\\):", line)
        if len(parts) < 4:
            continue
        ssid = parts[0].replace("\\:", ":")
        if not ssid or ssid == "Off The Cloud":
            continue
        try:
            signal, freq = int(parts[1]), int(parts[3].split()[0])
        except (ValueError, IndexError):
            continue
        band = "2.4" if 2400 <= freq < 2500 else "5" if 4900 <= freq < 5900 else None
        if not band:
            continue
        security = parts[2].strip()
        bands = set(best[ssid]["bands"]) if ssid in best else set()
        bands.add(band)
        if ssid not in best or best[ssid]["signal"] < signal:
            best[ssid] = {"ssid": ssid, "signal": signal, "secured": security != "", "security": security, "freq": freq}
        best[ssid]["bands"] = sorted(bands)
    networks = sorted(best.values(), key=lambda n: -n["signal"])
    write_json(CONFIG["scan_file"], {"at": time.time(), "networks": networks})
    return networks


# ---------------------------------------------------------------------------
# Disks + an existing array to recover
# ---------------------------------------------------------------------------

def list_disks():
    """USB/SATA disks that are not the one the system booted from."""
    res = run(["lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,MODEL,TYPE,TRAN,MOUNTPOINTS"])
    try:
        tree = json.loads(res.stdout)
    except Exception:  # noqa: BLE001
        return []

    def holds_system(node):
        for mp in node.get("mountpoints") or []:
            if mp in ("/", "/boot", "/boot/firmware"):
                return True
        return any(holds_system(c) for c in node.get("children") or [])

    disks = []
    for node in tree.get("blockdevices", []):
        if node.get("type") != "disk" or holds_system(node):
            continue
        name = node.get("name", "")
        if name.startswith(("zram", "loop", "ram", "md")):
            continue
        size = int(node.get("size") or 0)
        disks.append({
            "path": node.get("path"),
            "size": size,
            "size_h": human_size(size),
            "model": (node.get("model") or "").strip(),
            "transport": node.get("tran") or "",
            "in_use": bool(node.get("mountpoints") and any(node.get("mountpoints"))),
        })
    return disks


def parent_disk(dev):
    """/dev/sda1 -> /dev/sda; a whole-disk member is returned as is."""
    res = run(["lsblk", "-no", "PKNAME", dev])
    parent = res.stdout.strip().splitlines()[0].strip() if res.stdout.strip() else ""
    return f"/dev/{parent}" if parent else dev


def detect_recovery():
    """An existing RAID1 array on the attached disks holding an Off The
    Cloud database: what a re-imaged device finds when the old Pi died but
    its disks didn't. Assembling is read-only as far as the data goes;
    install.sh assembles the very same way before it would ever wipe."""
    if CONFIG["dry_run"] and os.environ.get("OTC_SETUP_DRY_RUN_RECOVERY") == "1":
        return {"found": True, "device": "/dev/md0", "members": ["/dev/sda", "/dev/sdb"],
                "size_h": "1000 GB", "has_database": True}
    run(["mdadm", "--assemble", "--scan"], timeout=60)
    try:
        mdstat = Path("/proc/mdstat").read_text()
    except Exception:  # noqa: BLE001
        return {"found": False}
    for line in mdstat.splitlines():
        m = MDSTAT_RE.match(line.strip())
        if not m:
            continue
        device = f"/dev/{m.group(1)}"
        members = sorted({parent_disk("/dev/" + mm.group(1))
                          for tok in m.group(2).split() for mm in [MEMBER_RE.match(tok)] if mm})
        size = 0
        res = run(["lsblk", "-b", "-dno", "SIZE", device])
        try:
            size = int(res.stdout.strip())
        except ValueError:
            pass
        has_db = False
        mnt = Path(CONFIG["recovery_mount"])
        mnt.mkdir(parents=True, exist_ok=True)
        if run(["mount", "-o", "ro", device, str(mnt)]).returncode == 0:
            has_db = (mnt / "mysql" / "otc").is_dir()
            run(["umount", str(mnt)])
        return {"found": True, "device": device, "members": members,
                "size_h": human_size(size), "has_database": has_db}
    return {"found": False}


def wipe_array(arr):
    """The person chose a fresh start over recovering: take the old array
    apart so install.sh's own assemble-before-wipe finds nothing."""
    run(["mdadm", "--stop", arr["device"]], timeout=60)
    for member in arr.get("members", []):
        run(["mdadm", "--zero-superblock", member], timeout=60)
        run(["wipefs", "-a", member], timeout=60)


# ---------------------------------------------------------------------------
# Bridge name + identity
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Sealing the owner password (the page encrypts it before it leaves the
# phone): the hotspot is an open WiFi network and the Bluetooth link is not
# paired, so a password sent as is could be read by anyone nearby. The page
# has no WebCrypto (not a secure context over plain HTTP), so both sides
# are hand-written: RSA-2048 with OAEP (SHA-256, MGF1-SHA-256, empty
# label) - a one-off key pair made when the wizard starts, stdlib only.
# ---------------------------------------------------------------------------

_SMALL_PRIMES = [p for p in range(3, 2000) if all(p % q for q in range(2, int(p ** 0.5) + 1))]


def _probable_prime(n, rounds=40):
    if n < 2:
        return False
    for p in _SMALL_PRIMES:
        if n % p == 0:
            return n == p
    d, r = n - 1, 0
    while d % 2 == 0:
        d //= 2
        r += 1
    for _ in range(rounds):
        a = secrets.randbelow(n - 3) + 2
        x = pow(a, d, n)
        if x in (1, n - 1):
            continue
        for _ in range(r - 1):
            x = pow(x, 2, n)
            if x == n - 1:
                break
        else:
            return False
    return True


def _prime(bits):
    while True:
        c = secrets.randbits(bits) | (1 << (bits - 1)) | (1 << (bits - 2)) | 1
        if _probable_prime(c):
            return c


def _mgf1(seed, length):
    out, counter = b"", 0
    while len(out) < length:
        out += hashlib.sha256(seed + counter.to_bytes(4, "big")).digest()
        counter += 1
    return out[:length]


class SealKey:
    """RSA-2048 for OAEP-SHA-256; generated in the background at start."""

    def __init__(self):
        self.ready = threading.Event()
        threading.Thread(target=self._generate, daemon=True).start()

    def _generate(self):
        e = 65537
        while True:
            p, q = _prime(1024), _prime(1024)
            phi = (p - 1) * (q - 1)
            if p != q and phi % e and (p * q).bit_length() == 2048:
                break
        self.n, self.e, self.d = p * q, e, pow(e, -1, phi)
        self.ready.set()

    def public(self):
        self.ready.wait(60)
        return {"n": format(self.n, "x"), "e": self.e}

    def open(self, sealed_b64):
        """The plaintext bytes, or None when it isn't a valid seal for this key."""
        self.ready.wait(60)
        try:
            c = int.from_bytes(base64.b64decode(sealed_b64), "big")
        except Exception:  # noqa: BLE001
            return None
        k = (self.n.bit_length() + 7) // 8
        if c >= self.n:
            return None
        em = pow(c, self.d, self.n).to_bytes(k, "big")
        h = 32
        if em[0] != 0:
            return None
        masked_seed, masked_db = em[1:1 + h], em[1 + h:]
        seed = bytes(a ^ b for a, b in zip(masked_seed, _mgf1(masked_db, h)))
        db = bytes(a ^ b for a, b in zip(masked_db, _mgf1(seed, k - h - 1)))
        if db[:h] != hashlib.sha256(b"").digest():
            return None
        rest = db[h:].lstrip(b"\x00")
        if not rest or rest[0] != 1:
            return None
        return rest[1:]


SEAL = None  # the SealKey, made in main()


def setup_token():
    """One random token per setup, kept in the state file so a wizard
    restart doesn't orphan the page that already holds it."""
    with _state_lock:
        st = load_state()
        if not st.get("token"):
            st["token"] = secrets.token_urlsafe(32)
            save_state(st)
        return st["token"]


def device_id():
    """A short ID for telling devices apart while several are being set up
    at once: the last 4 hex digits of the Pi's serial number (the machine
    id elsewhere). The Bluetooth name, the setup page and the app's list
    of devices all show it."""
    for path in ("/proc/device-tree/serial-number", "/sys/firmware/devicetree/base/serial-number"):
        try:
            s = Path(path).read_text().strip("\x00\n ")
            if len(s) >= 4:
                return s[-4:].upper()
        except OSError:
            pass
    try:
        for line in Path("/proc/cpuinfo").read_text().splitlines():
            if line.startswith("Serial") and ":" in line:
                s = line.split(":", 1)[1].strip()
                if len(s) >= 4:
                    return s[-4:].upper()
    except OSError:
        pass
    try:
        return Path("/etc/machine-id").read_text().strip()[-4:].upper()
    except OSError:
        return hashlib.sha256(socket.gethostname().encode()).hexdigest()[-4:].upper()

_blinking = threading.Lock()


def blink_led(seconds=15):
    """"Blink its light" on the setup page: flashes the Pi's green activity
    LED so the owner can tell which box is which when setting up several
    at once, then puts its usual trigger back."""
    if not _blinking.acquire(blocking=False):
        return
    try:
        led = next((Path("/sys/class/leds", n) for n in ("ACT", "led0") if Path("/sys/class/leds", n).exists()), None)
        if led is None:
            return
        trigger = (led / "trigger").read_text()
        previous = trigger.split("[", 1)[1].split("]", 1)[0] if "[" in trigger else "none"
        (led / "trigger").write_text("none")
        on = False
        end = time.time() + seconds
        while time.time() < end:
            on = not on
            (led / "brightness").write_text("1" if on else "0")
            time.sleep(0.15)
        (led / "trigger").write_text(previous)
    except OSError as e:
        print(f"[otc-setup] blink failed: {e}")
    finally:
        _blinking.release()


def lan_address():
    """This device's address on the network it uses to reach the bridge."""
    res = run(["ip", "-4", "route", "get", "1.1.1.1"])
    m = re.search(r"\bsrc (\S+)", res.stdout)
    return m.group(1) if m else ""


def beacon_loop():
    """Issue #38 hand-off: while setting up, keep telling the bridge where
    this device is on the LAN, under the setup token, so the wizard page
    left open on the owner's phone (which lost the hotspot when the
    device joined their WiFi) can find it again by polling the bridge.
    Reported every few seconds while online; the bridge forgets it after
    a quarter of an hour."""
    while not Path(CONFIG["setup_done_marker"]).exists():
        addr = lan_address()
        if addr and not addr.startswith("10.42.0.") and bridge_reachable():
            try:
                bridge_post("/api/setup-beacon", {"token": setup_token(), "addr": addr})
            except Exception as e:  # noqa: BLE001
                print(f"[otc-setup] beacon failed: {e}")
        time.sleep(5)


def bridge_get(path, token=None):
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(f"https://{CONFIG['bridge']}{path}", headers=headers)
    with urllib.request.urlopen(req, timeout=8) as r:
        return r.status, json.loads(r.read().decode() or "{}")


def bridge_answer(path, token=None):
    """bridge_get, but the bridge's error answers come back as (status,
    data), as bridge_post's do, for the page to show (issue #189: a 500
    "could not check that name right now" read as "could not reach").
    Only a bridge that can't be reached raises."""
    try:
        return bridge_get(path, token)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:  # noqa: BLE001
            return e.code, {}


def bridge_post(path, body):
    data = json.dumps(body).encode()
    req = urllib.request.Request(f"https://{CONFIG['bridge']}{path}", data=data, method="POST",
                                 headers={"Content-Type": "application/json", "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:  # noqa: BLE001
            return e.code, {}


def new_identity():
    return {
        "DEVICE_UUID": str(uuid.uuid4()),
        "BRIDGE_SECRET": secrets.token_hex(24),
        "OTC_DB_PASS": secrets.token_urlsafe(24).replace("-", "").replace("_", ""),
    }


def write_env_file(identity):
    # 0600 from the moment it exists (it was briefly world-readable).
    atomic_write(CONFIG["env_file"],
        "# Generated by the OTC setup wizard. Keep out of git - this is the\n"
        "# device's DB password and bridge secret.\n"
        + "".join(f"{k}={v}\n" for k, v in identity.items()), 0o600)


def claim_name(name, setup_token):
    """Reserve name on the bridge with a fresh identity, for the account
    behind setup_token (issue #124). Returns (domain, identity) or raises
    with a message for the page. A name the account already owns is handed
    to this device - that is how a lost device is replaced."""
    identity = new_identity()
    status, data = bridge_post("/api/claim", {
        "name": name, "owner_uuid": identity["DEVICE_UUID"], "secret": identity["BRIDGE_SECRET"],
        "setup_token": setup_token})
    if status != 201:
        raise RuntimeError(data.get("error", "could not reserve that name"))
    return data.get("domain", f"{name}.{CONFIG['bridge']}"), identity


def account_sign_in(action, body):
    """Issue #124: sign in or sign up on the bridge for the person setting
    up, from here rather than the phone's browser - the hotspot's captive
    DNS only lets the device itself reach the bridge. Returns (status, data):
    data carries setup_token on success, error otherwise."""
    return bridge_post(f"/api/account/{action}?for=setup", body)


def setup_token_owner(token):
    """Who a typed setup code belongs to: (status, data). 200 with the
    account, 404 for a code the bridge doesn't know (or has expired), and
    502 with a "try again" message when the bridge couldn't answer
    (issue #189: a failed check read as an invalid code)."""
    try:
        status, data = bridge_answer("/api/account/setup-token-info?token=" + urllib.parse.quote(token))
    except Exception as e:  # noqa: BLE001
        return 502, {"error": f"could not reach {CONFIG['bridge']}: {e}"}
    if status == 200:
        return 200, data
    if status >= 500:
        return 502, {"error": data.get("error") or "could not check that setup code right now - try again"}
    return 404, {"error": "that setup code is not valid or has expired"}


def db_query(sql):
    """One value out of the device's own database, as root over the
    socket (install.sh leaves MariaDB's root on socket auth)."""
    res = run(["mysql", "-N", "-B", "otc", "-e", sql], timeout=30)
    return res.stdout.strip() if res.returncode == 0 else ""


def rebind_recovered_device(domain, identity):
    """A recovered device whose old name is gone from the bridge: give the
    database the new identity so the running service presents it."""
    sql = ("update settings set device_uuid='{u}', subdomain='{d}', bridge_secret='{s}'"
           .format(u=identity["DEVICE_UUID"], d=domain, s=identity["BRIDGE_SECRET"]))
    res = run(["mysql", "otc", "-e", sql], timeout=30)
    if res.returncode != 0:
        raise RuntimeError(f"could not update the device's settings: {res.stderr.strip()}")
    run(["systemctl", "restart", "otc.service"], timeout=60)


# ---------------------------------------------------------------------------
# The install run, then the bridge check
# ---------------------------------------------------------------------------

class Install:
    def __init__(self):
        self.lock = threading.Lock()
        self.reset()

    def reset(self):
        self.phase = "idle"   # idle | installing | verifying | online | offline | needs_name | failed
        self.recovery = False
        self.domain = ""
        self.step = 0
        self.total = 10
        self.text = ""
        self.detail = ""
        self.error = ""
        self.log = []

    def snapshot(self):
        with self.lock:
            return {
                "phase": self.phase, "recovery": self.recovery, "domain": self.domain,
                "step": self.step, "total": self.total, "text": self.text,
                "detail": self.detail, "error": self.error, "log_tail": self.log[-40:],
            }

    def start(self, name, disks, recovery):
        with self.lock:
            if self.phase in ("installing", "verifying"):
                return
            self.reset()
            self.phase = "installing"
            self.recovery = recovery
        threading.Thread(target=self._run, args=(name, disks), daemon=True).start()

    def _feed(self, line):
        line = line.rstrip("\n")
        with self.lock:
            self.log.append(line)
            if len(self.log) > 2000:
                del self.log[:500]
            m = STEP_RE.search(line)
            if m:
                self.step, self.total, self.text = int(m.group(1)), int(m.group(2)), m.group(3)
                self.detail = ""
                return
            m = ERROR_RE.search(line)
            if m:
                self.error = m.group(1)
                return
            m = INFO_RE.search(line)
            if m:
                self.detail = m.group(1)

    def _run(self, name, disks):
        env = dict(os.environ)
        if not self.recovery and Path(CONFIG["owner_password_file"]).exists():
            env["OTC_OWNER_PASSWORD_FILE"] = CONFIG["owner_password_file"]
        if not self.recovery and Path(CONFIG["profile_file"]).exists():
            env["OTC_SETUP_PROFILE_FILE"] = CONFIG["profile_file"]
        if self.recovery:
            # Recovering must never wipe: install.sh refuses to build a
            # fresh array in this mode and stops instead. (It used to get
            # the wipe confirmation here too, and when it missed the array
            # the wizard had assembled as /dev/md127 it went for the disks.)
            env["OTC_RECOVERY"] = "1"
        else:
            # A fresh install on disks the person picked; an old array on
            # them was already taken apart by wipe_array() after they
            # confirmed it.
            env["OTC_RAID_CONFIRM_WIPE"] = "yes"
        # Issue #124: no account, no bridge - the device stays on the home
        # network (install.sh's OTC_BRIDGE_ADDR="").
        if load_state().get("skip_bridge"):
            env["OTC_BRIDGE_ADDR"] = ""
        if len(disks) == 2:
            env["OTC_DISK1"], env["OTC_DISK2"] = disks
        elif len(disks) == 1:
            # A degraded array being recovered: install.sh assembles it by
            # scan; the disk variables only matter when nothing assembles.
            env["OTC_DISK1"] = env["OTC_DISK2"] = disks[0]
        else:
            env["OTC_SKIP_RAID"] = "1"
        if CONFIG["dry_run"]:
            cmd = ["bash", "-c",
                   'for i in 1 2 3 4 5 6 7 8 9 10; do echo "[otc-install] [$i/10] dry-run step $i"; '
                   'echo "[otc-install] doing things for step $i"; sleep 1; done; echo "[otc-install] Done."']
        else:
            # Issue #160: the image's own copy of verified-install.sh (with
            # the release key in /etc/otc) installs the newest *signed*
            # release - nothing from the branch runs. A wizard on an image
            # from before it fetches that same script.
            local_installer = "/usr/local/bin/otc-verified-install"
            if os.path.exists(local_installer):
                cmd = ["bash", local_installer, name]
            else:
                cmd = ["bash", "-c", 'curl -fsSL --retry 3 "$0" | bash -s -- "$1"', CONFIG["install_url"], name]
        Path(CONFIG["install_log"]).parent.mkdir(parents=True, exist_ok=True)
        # /var/log/otc is the otc user's once an install has run: appended
        # to as root without following a symlink put in the log's place.
        try:
            logfd = os.open(CONFIG["install_log"], os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o644)
        except OSError as e:
            with self.lock:
                self.phase, self.error = "failed", f"could not open the install log: {e}"
            return
        with os.fdopen(logfd, "a") as logf:
            logf.write(f"\n=== setup wizard: install started {time.strftime('%F %T')} name={name} disks={disks}\n")
            try:
                proc = subprocess.Popen(cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                        text=True, bufsize=1)
            except Exception as e:  # noqa: BLE001
                with self.lock:
                    self.phase, self.error = "failed", f"could not start the installer: {e}"
                return
            for line in proc.stdout:
                logf.write(line)
                logf.flush()
                self._feed(line)
            code = proc.wait()
            logf.write(f"=== installer exited with {code}\n")
        with self.lock:
            if code != 0:
                self.phase = "failed"
                if not self.error:
                    self.error = f"the installer exited with code {code}"
                return
            self.step, self.text = self.total, "Installed"
        self.verify()

    def verify(self):
        """Wait for the device to show up on the bridge under its domain."""
        if load_state().get("skip_bridge"):
            # Nothing to wait for: a local-only device never dials in.
            with self.lock:
                self.phase = "online"
            self.finish()
            return
        with self.lock:
            self.phase = "verifying"
            self.detail = ""
        # A fresh device, or a recovered one that has just been registered
        # under a new name, has the domain in the wizard's own state; a
        # recovered device otherwise answers to whatever its database says.
        domain = load_state().get("domain", "")
        if self.recovery and not domain:
            if CONFIG["dry_run"]:
                domain = "recovered-dry-run." + CONFIG["bridge"]
            else:
                domain = db_query("select subdomain from settings limit 1") or ""
        with self.lock:
            self.domain = domain
        online = False
        deadline = time.time() + CONFIG["verify_timeout_s"]
        while time.time() < deadline:
            if CONFIG["dry_run"]:
                time.sleep(3)
                online = not CONFIG["dry_run_offline"]
                break
            name = domain.split(".")[0] if domain else ""
            if name:
                try:
                    _, data = bridge_get("/api/device-online?name=" + urllib.parse.quote(name))
                    online = bool(data.get("online"))
                except Exception:  # noqa: BLE001
                    online = False
            if online:
                break
            time.sleep(5)
        with self.lock:
            if online:
                self.phase = "online"
            elif self.recovery:
                # The array was recovered but its name no longer answers on
                # the bridge (released, or never registered): ask for one.
                self.phase = "needs_name"
            else:
                self.phase = "offline"
        if online:
            self.finish()

    def finish(self):
        """The person is being sent to the app. After a grace period for
        them to read the address: mark the setup done (network_setup.py
        then drops the hotspot, which also closes a phone's captive-portal
        sheet), and get out of the way - the real service wants port 80."""
        # A phone on Bluetooth may miss the minute this process has left
        # (a dropped link, a locked screen): setup_ble.py answers it from
        # this instead, as "online" - "Open it anyway" counts as done too.
        try:
            final = state_snapshot()
            final["install"]["phase"] = "online"
            final["install"]["log_tail"] = []
            final["token"] = ""
            write_json(CONFIG["final_state_file"], final)
        except Exception as e:  # noqa: BLE001
            print("[otc-setup] could not save the final state:", e)
        threading.Thread(target=self._exit_later, daemon=True).start()

    def _exit_later(self):
        time.sleep(45)
        # Not touch(): that creates (or updates) a planted symlink's target.
        atomic_write(CONFIG["setup_done_marker"], "")
        time.sleep(15)
        if CONFIG["dry_run"]:
            return
        print("[otc-setup] install complete - handing over to otc.service")
        run(["systemctl", "disable", "otc-setup.service"])
        # Restart otc after this process has exited (it holds port 80).
        run(["systemd-run", "--on-active=3", "--quiet", "systemctl", "restart", "otc.service"])
        os._exit(0)


install = Install()


# ---------------------------------------------------------------------------
# State
# ---------------------------------------------------------------------------

def load_state():
    return read_json(CONFIG["state_file"], {})


def save_state(state):
    write_json(CONFIG["state_file"], state)


# The beacon thread and every request thread read, change and save the
# state file. Held only for that (never across a call to the bridge, which
# would stall every /api/state poll), so each saves just the keys it
# changed on a fresh copy, and none writes back a copy read seconds before.
_state_lock = threading.Lock()


def update_state(changes):
    with _state_lock:
        st = load_state()
        st.update(changes)
        save_state(st)
        return st


def state_snapshot():
    st = load_state()
    join = read_json(CONFIG["join_result"], {})
    return {
        "online": bridge_reachable(),
        "account": {"email": st.get("account_email", ""), "skip_bridge": bool(st.get("skip_bridge"))},
        "ssid": current_ssid(),
        "lan_addr": lan_address(),
        "token": setup_token(),
        "join_result": join,
        "name": st.get("name", ""),
        "domain": st.get("domain", ""),
        "device_id": device_id(),
        "bridge": CONFIG["bridge"],
        "install": install.snapshot(),
        "already_installed": Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"],
    }


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    server_version = "otc-setup/1"

    def log_message(self, fmt, *args):  # quieter journal
        if "/api/state" not in (args[0] if args else ""):
            print("[otc-setup]", self.address_string(), fmt % args)

    def send_json(self, status, data):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def via_bluetooth(self):
        """Issue #137: setup_ble.py forwards the app's requests from the
        device itself; everything over the hotspot comes from 10.42.0.x.
        Over Bluetooth the hotspot isn't needed, so 5GHz networks can be
        joined."""
        return self.client_address[0] in ("127.0.0.1", "::1")

    def read_body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n <= 0 or n > 256 * 1024:  # the setup profile picture (#178)
            return {}
        try:
            return json.loads(self.rfile.read(n).decode())
        except Exception:  # noqa: BLE001
            return {}

    # --- GET ---------------------------------------------------------------
    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        if path == "/":
            body = PAGE.encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)
        elif path == "/api/state":
            self.send_json(200, state_snapshot())
        elif path == "/api/wifi":
            rescan = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("rescan", ["0"])[0] == "1"
            self.send_json(200, {"networks": scan_wifi(rescan)})
        elif path == "/api/disks":
            self.send_json(200, {"disks": list_disks(), "recovery": detect_recovery()})
        elif path == "/api/pubkey":
            # The key the page seals the owner password with.
            self.send_json(200, SEAL.public())
        elif path == "/api/providers":
            # The sign-in providers the bridge offers ("google", "apple"):
            # inside the app (Bluetooth setup) they get a button - the app
            # runs the sign-in in the phone's own browser sheet, which has
            # internet, and hands back a setup code (issue #137).
            try:
                status, data = bridge_get("/api/account/providers")
                providers = data.get("providers", []) if status == 200 and isinstance(data, dict) else []
            except Exception:  # noqa: BLE001
                providers = []
            self.send_json(200, {"providers": [p for p in providers if p in ("google", "apple")]})
        elif path == "/api/countries":
            # Issue #135: the country picker of the account step. Fetched
            # through the device, not by the phone's browser - the page is
            # served from the hotspot's address, so a direct call to the
            # bridge is cross-origin (and, on the captive portal, may not
            # even reach it), which left the list empty.
            global COUNTRIES
            if not COUNTRIES:
                try:
                    status, data = bridge_get("/api/account/countries")
                    if status == 200 and isinstance(data, dict):
                        COUNTRIES = data
                except Exception as e:  # noqa: BLE001
                    self.send_json(502, {"error": f"could not reach {CONFIG['bridge']}: {e}"})
                    return
            self.send_json(200, COUNTRIES)
        elif path == "/api/name":
            name = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("name", [""])[0].strip().lower()
            if not NAME_RE.match(name):
                self.send_json(400, {"error": "lower-case letters, digits and hyphens only"})
                return
            try:
                # With the account's setup code, a taken name also says
                # whether it is this account's own and if that device is
                # online (the page warns before moving it).
                status, data = bridge_answer("/api/name-available?name=" + urllib.parse.quote(name),
                                             load_state().get("setup_token"))
                if status != 200 and not data.get("error"):
                    data = {"error": "could not check that name right now - try again"}
                self.send_json(status, data)
            except Exception as e:  # noqa: BLE001
                self.send_json(502, {"error": f"could not reach {CONFIG['bridge']}: {e}"})
        else:
            # Captive-portal probes (Apple's hotspot-detect.html, Android's
            # generate_204, Windows' connecttest.txt, ...) and anything else:
            # a redirect is what makes every phone open the "sign in" sheet.
            # Always to the hotspot's own address - the probe's Host header
            # is captive.apple.com or the like, which only resolves here
            # while the DNS trick is in effect.
            host = (self.headers.get("Host") or "").split(":")[0]
            target = host if host in (CONFIG["ap_address"], "otc.local", "otc") else CONFIG["ap_address"]
            self.send_response(302)
            self.send_header("Location", f"http://{target}/")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", "0")
            self.end_headers()

    def do_HEAD(self):
        # Same routing as GET, no body - some probes (and curl -I) use it.
        self.do_GET()

    # --- POST --------------------------------------------------------------
    def do_POST(self):
        path = urllib.parse.urlparse(self.path).path
        body = self.read_body()
        if Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"]:
            self.send_json(409, {"error": "this device is already set up"})
            return

        if path == "/api/identify":
            threading.Thread(target=blink_led, daemon=True).start()
            self.send_json(200, {"ok": True})
            return

        if path == "/api/wifi":
            ssid = str(body.get("ssid", ""))[:64]
            password = str(body.get("password", ""))[:128]
            if not ssid:
                self.send_json(400, {"error": "choose a network"})
                return
            known = next((n for n in scan_wifi() if n.get("ssid") == ssid), None)
            security = (known or {}).get("security", "")
            if known and known.get("secured") and not password:
                self.send_json(400, {"error": f"{ssid} needs a password"})
                return
            any_band = self.via_bluetooth()
            if known and known.get("bands") == ["5"] and not any_band:
                self.send_json(400, {"error": f"{ssid} is a 5 GHz network: the hotspot can't follow it there. "
                                              "Set the device up with the app's \"Set up a new device\" (Bluetooth) instead, "
                                              "or pick a 2.4 GHz network."})
                return
            print(f"[otc-setup] join requested: {ssid!r} security={security!r} password={len(password)} chars")
            Path(CONFIG["join_result"]).unlink(missing_ok=True)
            # network_setup.py (root, owns the radio) does the actual join
            # and moves the hotspot onto the joined network's channel; the
            # request is handed over after a moment so the page can say so.
            def later():
                time.sleep(CONFIG["join_delay_s"])
                write_json(CONFIG["join_request"], {"ssid": ssid, "password": password, "security": security, "any_band": any_band}, mode=0o600)
            threading.Thread(target=later, daemon=True).start()
            self.send_json(202, {"ok": True, "delay_s": CONFIG["join_delay_s"]})

        elif path == "/api/account":
            # Issue #124: sign in / sign up / a typed setup code / skip.
            action = str(body.get("action", ""))
            if action == "skip":
                update_state({"skip_bridge": True, "setup_token": "", "account_email": ""})
                self.send_json(200, {"ok": True})
                return
            if action == "code":
                token = str(body.get("setup_token", "")).strip()
                status, who = setup_token_owner(token)
                if status != 200:
                    self.send_json(status, who)
                    return
                update_state({"skip_bridge": False, "setup_token": token, "account_email": who.get("email", "")})
                self.send_json(200, {"email": who.get("email", "")})
                return
            if action not in ("login", "signup"):
                self.send_json(400, {"error": "unknown action"})
                return
            fields = {k: str(body.get(k, ""))[:255] for k in ("email", "password", "name", "surname", "country")}
            if action == "signup":
                fields["accept_terms"] = bool(body.get("accept_terms"))
            try:
                status, data = account_sign_in(action, fields)
            except Exception as e:  # noqa: BLE001
                self.send_json(502, {"error": f"could not reach {CONFIG['bridge']}: {e}"})
                return
            # The bridge gives no setup code to an account whose email isn't
            # confirmed yet: the page asks the person to open the link and
            # signs in again.
            if data.get("verify_email"):
                self.send_json(202, {"verify": True,
                                     "email": (data.get("account") or {}).get("email", fields["email"])})
                return
            if status not in (200, 201) or not data.get("setup_token"):
                self.send_json(status if status >= 400 else 502, {"error": data.get("error", "could not sign in")})
                return
            st = update_state({"skip_bridge": False, "setup_token": data["setup_token"],
                               "account_email": (data.get("account") or {}).get("email", fields["email"])})
            self.send_json(200, {"email": st["account_email"]})

        elif path == "/api/name":
            name = str(body.get("name", "")).strip().lower()
            if not NAME_RE.match(name):
                self.send_json(400, {"error": "lower-case letters, digits and hyphens only"})
                return
            st = load_state()
            snap = install.snapshot()
            rebind = snap["phase"] == "needs_name"
            if not rebind and st.get("name") == name and st.get("domain"):
                self.send_json(200, {"domain": st["domain"]})
                return
            if not st.get("setup_token"):
                self.send_json(401, {"error": "sign in to your Off The Cloud account first", "code": "login_required"})
                return
            try:
                domain, identity = claim_name(name, st["setup_token"])
                if rebind:
                    if not CONFIG["dry_run"]:
                        rebind_recovered_device(domain, identity)
                    write_env_file(identity)
                    update_state({"name": name, "domain": domain})
                    threading.Thread(target=install.verify, daemon=True).start()
                else:
                    write_env_file(identity)
                    update_state({"name": name, "domain": domain})
            except urllib.error.URLError as e:
                self.send_json(502, {"error": f"could not reach {CONFIG['bridge']}: {e}"})
                return
            except RuntimeError as e:
                self.send_json(409, {"error": str(e)})
                return
            self.send_json(200, {"domain": domain})

        elif path == "/api/local-name":
            # Issue #124: no bridge - the device is "otc" on the home
            # network; an identity is still written so install.sh has one.
            st = load_state()
            if not st.get("skip_bridge"):
                self.send_json(400, {"error": "an account was chosen"})
                return
            identity = new_identity()
            write_env_file(identity)
            update_state({"name": "otc", "domain": ""})
            self.send_json(200, {"domain": ""})

        elif path == "/api/install":
            st = load_state()
            mode = str(body.get("mode", "fresh"))
            disks = [str(d) for d in (body.get("disks") or [])]
            if not bridge_reachable():
                self.send_json(409, {"error": f"no internet connection - {CONFIG['bridge']} is not reachable"})
                return
            if mode == "recover":
                arr = detect_recovery()
                if not arr.get("found"):
                    self.send_json(400, {"error": "no existing array to recover"})
                    return
                disks = arr["members"][:2]
                update_state({"disks": disks})
                install.start("recovery", disks, recovery=True)
            else:
                if not st.get("name"):
                    self.send_json(400, {"error": "choose the device's name first"})
                    return
                # The owner password, sealed by the page (see SealKey). Kept
                # for install.sh in a root-only file on tmpfs; a retry that
                # doesn't send it again uses the one already there.
                sealed = body.get("owner_password")
                if sealed:
                    pw = SEAL.open(str(sealed))
                    try:
                        pw = pw.decode("utf-8") if pw is not None else None
                    except UnicodeDecodeError:
                        pw = None
                    if pw is None:
                        self.send_json(400, {"error": "the password could not be read - reload the page and try again"})
                        return
                    if len(pw) < 8 or "\n" in pw or "\r" in pw:
                        self.send_json(400, {"error": "the password must have 8 characters or more"})
                        return
                    pf = Path(CONFIG["owner_password_file"])
                    pf.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                    fd = os.open(str(pf), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
                    with os.fdopen(fd, "w") as fh:
                        fh.write(pw + "\n")
                # Issue #178: the owner's profile and whether face
                # recognition is on (off unless ticked) - all optional.
                prof = body.get("profile")
                if isinstance(prof, dict):
                    pname = str(prof.get("name", ""))[:100].strip()
                    ptext = str(prof.get("text", ""))[:500].strip()
                    pimg = str(prof.get("image", ""))
                    if pimg:
                        try:
                            raw = base64.b64decode(pimg, validate=True)
                        except (ValueError, TypeError):
                            raw = b""
                        # A JPEG the page cropped, small by construction.
                        if not (raw[:3] == b"\xff\xd8\xff" and len(raw) <= 96 * 1024):
                            self.send_json(400, {"error": "the profile picture could not be used - choose another one"})
                            return
                    pf = Path(CONFIG["profile_file"])
                    pf.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                    fd = os.open(str(pf), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
                    with os.fdopen(fd, "w") as fh:
                        json.dump({"name": pname, "text": ptext, "image": pimg, "faces": bool(prof.get("faces"))}, fh)
                known = {d["path"] for d in list_disks()}
                if len(disks) not in (0, 2) or any(d not in known for d in disks) or len(set(disks)) != len(disks):
                    self.send_json(400, {"error": "choose two disks for RAID1, or none"})
                    return
                arr = detect_recovery()
                if arr.get("found") and any(d in arr.get("members", []) for d in disks):
                    if not body.get("confirm_wipe"):
                        self.send_json(409, {"error": "those disks hold an existing array - confirm wiping it"})
                        return
                    if not CONFIG["dry_run"]:
                        wipe_array(arr)
                update_state({"disks": disks})
                install.start(st["name"], disks, recovery=False)
            self.send_json(202, {"ok": True})

        elif path == "/api/verify":
            # "Check again" after an offline verdict.
            if install.snapshot()["phase"] in ("offline", "needs_name"):
                threading.Thread(target=install.verify, daemon=True).start()
            self.send_json(202, {"ok": True})

        elif path == "/api/finish":
            # "Open it anyway": the person takes over from here.
            if install.snapshot()["phase"] in ("offline", "needs_name"):
                install.finish()
            self.send_json(202, {"ok": True})
        else:
            self.send_json(404, {"error": "not found"})


# ---------------------------------------------------------------------------
# The page
# ---------------------------------------------------------------------------

PAGE = r"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Off The Cloud setup</title>
<style>
:root{--bg:#1e1f22;--panel:#2a2b30;--line:#3a3b42;--ink:#eaf2ef;--dim:#a7b0ab;--ember:#f07a5a;--ember-ink:#1e1f22;--ok:#7fd1a5;--bad:#f28b82}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 -apple-system,system-ui,Segoe UI,Roboto,sans-serif}
main{max-width:560px;margin:0 auto;padding:24px 16px 48px}
h1{font-size:1.5rem;margin:8px 0 4px}p.lead{color:var(--dim);margin:0 0 20px}
ol.steps{list-style:none;padding:0;margin:0 0 20px;display:flex;gap:6px}
ol.steps li{flex:1;height:6px;border-radius:3px;background:var(--line)}ol.steps li.on{background:var(--ember)}ol.steps li.done{background:var(--ok)}
.card{background:var(--panel);border:1px solid var(--line);border-radius:14px;padding:18px}
h2{margin:0 0 6px;font-size:1.15rem}.hint{color:var(--dim);font-size:.95rem;margin:0 0 14px}
label{display:block;font-size:.9rem;color:var(--dim);margin:12px 0 4px}
input[type=text],input[type=password]{width:100%;font:inherit;padding:11px 12px;border-radius:10px;border:1px solid var(--line);background:var(--bg);color:var(--ink)}
input:focus{outline:2px solid var(--ember);border-color:transparent}
button{font:inherit;font-weight:600;padding:11px 16px;border-radius:10px;border:0;background:var(--ember);color:var(--ember-ink);cursor:pointer;margin-top:16px}
button.ghost{background:transparent;color:var(--dim);border:1px solid var(--line)}button:disabled{opacity:.5;cursor:default}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.row>*{margin-top:0}.row:has(>button:first-child){margin-top:16px}.row+.list{margin-top:12px}
.crop{display:flex;gap:14px;align-items:center;flex-wrap:wrap}.crop canvas{width:180px;height:180px;border-radius:12px;background:var(--bg);border:1px solid var(--line);touch-action:none;cursor:grab}
.crop .side{flex:1;min-width:160px}.crop input[type=range]{width:100%}.check{display:flex;gap:8px;align-items:center;color:var(--ink);margin-top:16px}
.prof{margin-top:28px;padding-top:20px;border-top:1px solid var(--line)}.prof h3{margin:0 0 4px}
.prof+.row{margin-top:32px;padding-top:20px;border-top:1px solid var(--line)}
ul.list{list-style:none;padding:0;margin:0;border:1px solid var(--line);border-radius:10px;overflow:hidden;max-height:300px;overflow-y:auto}
ul.list li{padding:11px 12px;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;cursor:pointer}
ul.list li:last-child{border-bottom:0}ul.list li.sel{background:rgba(240,122,90,.15)}ul.list li small{color:var(--dim)}
.msg{margin-top:12px;font-size:.95rem}.msg.bad{color:var(--bad)}.msg.ok{color:var(--ok)}
.recover{border:1px solid var(--ok);border-radius:10px;padding:12px;margin:0 0 14px}
.bar{height:12px;background:var(--line);border-radius:6px;overflow:hidden;margin-top:14px}.bar>div{height:100%;background:var(--ember);width:0;transition:width .6s}
.step{margin-top:10px;font-weight:600}.detail{color:var(--dim);font-size:.9rem;min-height:1.3em}
pre{background:var(--bg);border:1px solid var(--line);border-radius:10px;padding:10px;font-size:.8rem;max-height:260px;overflow:auto;white-space:pre-wrap;word-break:break-all}
.spin{display:inline-block;width:14px;height:14px;border:2px solid var(--dim);border-top-color:var(--ember);border-radius:50%;animation:s 1s linear infinite;vertical-align:-2px;margin-right:6px}@keyframes s{to{transform:rotate(360deg)}}
a{color:var(--ember)}
.sso{display:flex;align-items:center;gap:12px;width:100%;margin-top:10px;padding:12px 16px;border-radius:10px;font-weight:600;font-size:1rem;cursor:pointer}
.sso svg{width:22px;height:22px;flex:none}.sso span{flex:1;text-align:center;margin-right:34px}
.sso.apple{background:#000;color:#fff;border:1px solid #5a5b62}
.sso.google,.sso.email{background:transparent;color:var(--ink);border:1px solid var(--line)}
.sso-or{display:flex;align-items:center;gap:10px;color:var(--dim);font-size:.85rem;margin:14px 0 2px}.sso-or:before,.sso-or:after{content:"";flex:1;border-top:1px solid var(--line)}
</style></head><body><main>
<h1>Off The Cloud</h1><p class="lead">Let's set up your device.<span id="devid" hidden> This is device <b id="devidv"></b>. <a href="#" id="blink">Blink its light</a></span></p>
<ol class="steps"><li id="s1"></li><li id="s2"></li><li id="s3"></li><li id="s4"></li><li id="s5"></li></ol>
<div id="view" class="card">Loading…</div>
</main>
<script>
const $=s=>document.querySelector(s);const view=$('#view');
// Enter in a field presses the next button after it (Connect, Continue,
// Install...), as in any form; secondary buttons (.ghost) are skipped.
document.addEventListener('keydown',e=>{if(e.key!=='Enter'||e.isComposing||e.target.tagName!=='INPUT'||e.target.type==='checkbox')return;
 const b=[...view.querySelectorAll('button:not(.ghost)')].find(x=>!x.disabled&&(e.target.compareDocumentPosition(x)&Node.DOCUMENT_POSITION_FOLLOWING));
 if(b){e.preventDefault();b.click()}});
// The owner password (issue #137 follow-up): chosen with the device's name,
// sealed with the wizard's one-off RSA key before it leaves the phone - the
// hotspot is open WiFi and Bluetooth isn't paired - and handed to the app
// too, in the app, so it signs in by itself at the end.
const SHA256=(()=>{const K=[0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2];
const r=(x,n)=>(x>>>n)|(x<<(32-n));
return b=>{const l=b.length,bl=((l+9+63)>>6)<<6,m=new Uint8Array(bl);m.set(b);m[l]=0x80;const bits=l*8;m[bl-4]=bits>>>24;m[bl-3]=bits>>>16;m[bl-2]=bits>>>8;m[bl-1]=bits;m[bl-5]=Math.floor(bits/2**32);
let H=[0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];const w=new Uint32Array(64);
for(let o=0;o<bl;o+=64){for(let i=0;i<16;i++)w[i]=(m[o+4*i]<<24)|(m[o+4*i+1]<<16)|(m[o+4*i+2]<<8)|m[o+4*i+3];
for(let i=16;i<64;i++){const s0=r(w[i-15],7)^r(w[i-15],18)^(w[i-15]>>>3),s1=r(w[i-2],17)^r(w[i-2],19)^(w[i-2]>>>10);w[i]=(w[i-16]+s0+w[i-7]+s1)|0}
let[a,b2,c,d,e,f,g,h]=H;for(let i=0;i<64;i++){const S1=r(e,6)^r(e,11)^r(e,25),ch=(e&f)^(~e&g),t1=(h+S1+ch+K[i]+w[i])|0,S0=r(a,2)^r(a,13)^r(a,22),mj=(a&b2)^(a&c)^(b2&c),t2=(S0+mj)|0;h=g;g=f;f=e;e=(d+t1)|0;d=c;c=b2;b2=a;a=(t1+t2)|0}
H=[(H[0]+a)|0,(H[1]+b2)|0,(H[2]+c)|0,(H[3]+d)|0,(H[4]+e)|0,(H[5]+f)|0,(H[6]+g)|0,(H[7]+h)|0]}
const out=new Uint8Array(32);H.forEach((v,i)=>{out[4*i]=v>>>24;out[4*i+1]=v>>>16;out[4*i+2]=v>>>8;out[4*i+3]=v});return out}})();
function mgf1(seed,len){const out=new Uint8Array(len+32);let c=0,o=0;while(o<len){const b=new Uint8Array(seed.length+4);b.set(seed);b[seed.length]=c>>>24;b[seed.length+1]=c>>>16;b[seed.length+2]=c>>>8;b[seed.length+3]=c;out.set(SHA256(b),o);o+=32;c++}return out.slice(0,len)}
function modPow(b,e,m){let r=1n;b%=m;while(e>0n){if(e&1n)r=r*b%m;b=b*b%m;e>>=1n}return r}
function sealWith(pk,pw){const n=BigInt('0x'+pk.n),e=BigInt(pk.e),k=Math.ceil(pk.n.length/2),h=32,msg=new TextEncoder().encode(pw);if(msg.length>k-2*h-2)throw new Error('password too long');
const db=new Uint8Array(k-h-1);db.set(SHA256(new Uint8Array(0)));db[db.length-msg.length-1]=1;db.set(msg,db.length-msg.length);
const seed=crypto.getRandomValues(new Uint8Array(h)),dm=mgf1(seed,db.length);for(let i=0;i<db.length;i++)db[i]^=dm[i];const sm=mgf1(db,h);for(let i=0;i<h;i++)seed[i]^=sm[i];
const em=new Uint8Array(k);em.set(seed,1);em.set(db,1+h);let x=0n;for(const v of em)x=(x<<8n)|BigInt(v);let c=modPow(x,e,n);const out=new Uint8Array(k);for(let i=k-1;i>=0;i--){out[i]=Number(c&255n);c>>=8n}
let s='';for(const v of out)s+=String.fromCharCode(v);return btoa(s)}

let owner={pw:'',pw2:''};
// Issue #178: the owner's profile and the face-recognition choice, all
// optional (Settings has them too). The picture is cropped here to a
// circle's square, a small JPEG - it travels over Bluetooth with the
// install request.
let prof={name:'',text:'',image:'',faces:false};const crop={img:null,x:0,y:0,z:1};const CROP=240,OUT=320;
function profileFields(){return `<section class="prof"><h3>Your profile <span class="detail">(optional)</span></h3>
 <label>Your name</label><input type="text" id="pn" maxlength="100" value="${esc(prof.name)}" placeholder="How your friends see you">
 <label>About you</label><input type="text" id="pt" maxlength="500" value="${esc(prof.text)}" placeholder="A line about you">
 <label>Profile picture</label><div class="crop"><canvas id="pc" width="${CROP}" height="${CROP}"></canvas><div class="side"><input type="file" id="pf" accept="image/*"><label>Zoom</label><input type="range" id="pz" min="1" max="4" step="0.01" value="${crop.z}" ${crop.img?'':'disabled'}><p class="hint">Drag the photo to centre your face.</p></div></div>
 <label class="check"><input type="checkbox" id="pfc" ${prof.faces?'checked':''}> Recognise faces in my photos</label>
 <p class="hint">Groups the same people across your photos so you can search by person. It runs only on this device, but it looks at everyone in your photos, not just you. Off unless you tick it.</p>
 <p class="hint">All of this is optional - you can set it later in Settings.</p></section>`}
function cropScale(){const im=crop.img;return crop.z*Math.max(CROP/im.width,CROP/im.height)}
function cropClamp(){const s=cropScale(),mx=Math.max(0,(crop.img.width*s-CROP)/2),my=Math.max(0,(crop.img.height*s-CROP)/2);crop.x=Math.min(mx,Math.max(-mx,crop.x));crop.y=Math.min(my,Math.max(-my,crop.y))}
function cropPaint(ctx,size,mask){const k=size/CROP,s=cropScale()*k,w=crop.img.width*s,h=crop.img.height*s;ctx.fillStyle='#1e1f22';ctx.fillRect(0,0,size,size);ctx.drawImage(crop.img,(size-w)/2+crop.x*k,(size-h)/2+crop.y*k,w,h);
 if(mask){ctx.fillStyle='rgba(0,0,0,.55)';ctx.beginPath();ctx.rect(0,0,size,size);ctx.arc(size/2,size/2,size/2-2,0,Math.PI*2,true);ctx.fill('evenodd')}}
function cropDraw(){const c=$('#pc');if(!c)return;const ctx=c.getContext('2d');if(!crop.img){ctx.fillStyle='#1e1f22';ctx.fillRect(0,0,CROP,CROP);return}cropPaint(ctx,CROP,true)}
function cropSave(){if(!crop.img)return;const o=document.createElement('canvas');o.width=o.height=OUT;cropPaint(o.getContext('2d'),OUT,false);
 for(const q of [.85,.7,.55,.4]){const b64=o.toDataURL('image/jpeg',q).split(',')[1];if(b64.length<=60000||q===.4){prof.image=b64;return}}}
function wireProfile(){if(!$('#pn'))return;
 $('#pn').oninput=e=>{prof.name=e.target.value};$('#pt').oninput=e=>{prof.text=e.target.value};$('#pfc').onchange=e=>{prof.faces=e.target.checked};
 const c=$('#pc'),z=$('#pz');cropDraw();
 $('#pf').onchange=e=>{const f=e.target.files&&e.target.files[0];if(!f)return;const im=new Image();im.onload=()=>{crop.img=im;crop.z=1;crop.x=0;crop.y=0;z.disabled=false;z.value=1;cropDraw();cropSave()};const r=new FileReader();r.onload=()=>{im.src=r.result};r.readAsDataURL(f)};
 z.oninput=()=>{if(!crop.img)return;crop.z=+z.value;cropClamp();cropDraw();cropSave()};
 let drag=null;const k=()=>CROP/c.getBoundingClientRect().width;
 c.onpointerdown=e=>{if(!crop.img)return;drag={x:e.clientX,y:e.clientY,ox:crop.x,oy:crop.y};c.setPointerCapture(e.pointerId)};
 c.onpointermove=e=>{if(!drag)return;crop.x=drag.ox+(e.clientX-drag.x)*k();crop.y=drag.oy+(e.clientY-drag.y)*k();cropClamp();cropDraw()};
 c.onpointerup=c.onpointercancel=()=>{if(drag){drag=null;cropSave()}}}
const ownerValid=()=>owner.pw.length>=8&&owner.pw===owner.pw2;
async function installBody(){const b={mode:'fresh',disks:disks.sel,confirm_wipe:true,profile:{name:prof.name,text:prof.text,image:prof.image,faces:prof.faces}};if(owner.pw){b.owner_password=sealWith(await api('/api/pubkey'),owner.pw);if(window.otcSetupPassword)window.otcSetupPassword(owner.pw)}return b}
function ownerFields(){return `<label>Device password</label><input type="password" id="opw" autocomplete="new-password" value="${esc(owner.pw)}"><label>Repeat the password</label><input type="password" id="opw2" autocomplete="new-password" value="${esc(owner.pw2)}"><p class="hint" id="ohint" style="margin-top:8px">8 characters or more. It encrypts everything on the device and can't be recovered - keep it somewhere safe. You sign in to the device with it.</p>`}
function wireOwner(update){const a=$('#opw'),b=$('#opw2');if(!a)return;const on=()=>{owner.pw=a.value;owner.pw2=b.value;const h=$('#ohint');const mis=owner.pw2&&owner.pw!==owner.pw2;h.textContent=mis?"The two passwords don't match.":(owner.pw&&owner.pw.length<8?'8 characters or more.':"8 characters or more. It encrypts everything on the device and can't be recovered - keep it somewhere safe. You sign in to the device with it.");h.className=mis||(owner.pw&&owner.pw.length<8)?'hint bad':'hint';h.style.color=mis?'var(--bad)':'';update()};a.oninput=on;b.oninput=on}
let state=null,step=1,wifi={list:[],sel:null,joining:false},name={val:'',ok:null,domain:'',msg:''},disks={list:[],sel:[],recovery:null,loaded:false,wipe:false},acct={mode:'login',msg:'',busy:false,countries:null,email:false};
const api=async(p,o)=>{const r=await fetch(p,Object.assign({cache:'no-store'},o||{}));let j={};try{j=await r.json()}catch(e){}return{ok:r.ok,status:r.status,...j}};
const post=(p,b)=>api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(b||{})});
const esc=s=>String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
function marks(){for(let i=1;i<=5;i++){const li=$('#s'+i);li.className=i<step?'done':i===step?'on':''}}
let lastKey='';
async function refresh(){let st;try{st=await api('/api/state')}catch(e){return}
 if(st.device_id&&$('#devid').hidden){$('#devidv').textContent=st.device_id;$('#devid').hidden=false;$('#blink').onclick=e=>{e.preventDefault();post('/api/identify',{}).catch(()=>{});$('#blink').textContent='Blinking - look for the flashing green light';setTimeout(()=>{$('#blink').textContent='Blink its light'},15000)}}
 const first=state===null;state=st;
 const ph=state.install.phase;const jr=state.join_result||{};
 if(state.already_installed&&ph==='idle'){step=6}
 else if(ph!=='idle'){step=5}
 else if(step===1&&state.online&&(wifi.joining||(first&&jr.ok))){wifi.joining=false;step=2;if(!disks.loaded)loadDisks()}
 // Only touch the page when something it shows has changed - a re-render
 // while someone is typing a password throws their focus away.
 const key=JSON.stringify([step,state.online,state.ssid,jr,ph,state.install.step,state.install.detail,state.install.error,state.install.domain]);
 if(key===lastKey&&!first)return;lastKey=key;
 // A join that failed must be shown even while the joining view is up.
 if(step===1&&wifi.joining&&jr&&jr.ok===false){wifi.joining=false;render();return}
 if(typing()||(step===1&&wifi.joining&&!state.online))return;
 render()}
function typing(){const a=document.activeElement;return a&&(a.tagName==='INPUT')&&view.contains(a)&&a.value!==''}
function render(){marks();
 if(step===1)return renderWifi();if(step===2)return renderDisks();if(step===3)return renderAccount();if(step===4)return renderName(false);if(step===5)return renderInstall();
 view.innerHTML=`<h2>Already set up</h2><p class="hint">This device has finished its setup.</p><p><a href="https://${esc(state.domain||state.bridge)}">Open ${esc(state.domain||'the app')}</a></p>`}
function renderWifi(){const online=state&&state.online;const cur=state&&state.ssid;const jr=state&&state.join_result;
 view.innerHTML=`<h2>1 · Connect to your WiFi</h2><p class="hint">${online?`The device is online${cur?' via <b>'+esc(cur)+'</b>':''}. You can continue, or join a different network.`:(window.otcApp?'Choose the network the device should use - 2.4 or 5 GHz.':'Choose the network the device should use. Over this hotspot it joins on 2.4 GHz and moves to 5 GHz once set up; a network that is only 5 GHz needs the app\'s "Set up a new device" (Bluetooth).')}</p>
 <div class="row"><button class="ghost" id="rescan" style="margin-top:0">Scan again</button><span id="scanmsg" class="detail"></span></div>
 <ul class="list" id="nets">${wifi.list.map(n=>{const b=n.bands||['2.4'];const only5=b.length===1&&b[0]==='5';const off=only5&&!window.otcApp;return `<li ${off?'':`data-ssid="${esc(n.ssid)}"`} class="${wifi.sel===n.ssid?'sel':''}" ${off?'style="opacity:.5;cursor:default"':''}><span>${esc(n.ssid)}${n.secured?' 🔒':''}${off?'<br><small>5 GHz only - use the app to join it</small>':''}</span><small>${b.join(' · ')} GHz · ${n.signal}%</small></li>`}).join('')||'<li><small>No networks yet - tap Scan.</small></li>'}</ul>
 <label>Password</label><input type="password" id="pw" placeholder="${wifi.sel?'Password for '+esc(wifi.sel):'Pick a network first'}" ${wifi.sel?'':'disabled'}>
 <div class="row"><button id="join" ${wifi.sel&&!wifi.joining?'':'disabled'}>${wifi.joining?'<span class="spin"></span>Connecting…':'Connect'}</button>${online?'<button class="ghost" id="skip">Continue</button>':''}</div>
 <div class="msg ${jr&&jr.ok===false?'bad':''}" id="wmsg">${jr&&jr.ok===false?esc(jr.error||'Could not join that network - check the password.')+(window.otcApp?'':' The hotspot is back - reconnect to it.'):''}</div>`;
 if(wifi.joining){view.innerHTML=`<h2>1 · Joining ${esc(wifi.sel)}</h2>
  <div class="step"><span class="spin"></span>Connecting and waiting for the internet - up to a minute.</div>
  ${window.otcApp?'':`<p style="margin-top:12px;padding:10px 12px;border:1px solid var(--ember);border-radius:10px"><b>If you get disconnected, reconnect to the "Off The Cloud" WiFi.</b> The hotspot restarts for a few seconds to switch to your network's channel; most phones rejoin it by themselves, but if yours doesn't, pick "Off The Cloud" again in your WiFi settings and come back to this page.</p>`}
  <p class="hint">Keep this page open - it continues on its own once the device is online.</p>
  <p class="hint">A wrong password shows up here as an error; just try again.</p>`;if(!window.otcApp)pollBridge();return}
 $('#rescan').onclick=()=>scan(true);document.querySelectorAll('#nets li[data-ssid]').forEach(li=>li.onclick=()=>{wifi.sel=li.dataset.ssid;render();$('#pw').focus()});
 $('#join').onclick=async()=>{const pw=$('#pw').value;const net=wifi.list.find(n=>n.ssid===wifi.sel);
  if(net&&net.secured&&!pw){$('#wmsg').textContent='Enter the password for '+wifi.sel;$('#wmsg').className='msg bad';$('#pw').focus();return}
  const r=await post('/api/wifi',{ssid:wifi.sel,password:pw});if(!r.ok){$('#wmsg').textContent=r.error||'Failed';$('#wmsg').className='msg bad';return}
  wifi.joining=true;wifi.countdown=r.delay_s||15;render();const tick=setInterval(()=>{wifi.countdown=Math.max(0,wifi.countdown-1);const c=$('#cd');if(c)c.textContent=wifi.countdown;if(wifi.countdown===0)clearInterval(tick)},1000)};
 const sk=$('#skip');if(sk)sk.onclick=()=>{step=2;if(!disks.loaded)loadDisks();render()}}
let polling=false;
async function pollBridge(){if(polling||!state||!state.token)return;polling=true;
 // The bridge is the one place both sides can reach: the device reports
 // its LAN address there once it's online, this page (still open on the
 // phone, now back on the home WiFi) picks it up and follows.
 while(polling){try{const r=await fetch(`https://${state.bridge}/api/setup-lookup?token=${encodeURIComponent(state.token)}`,{cache:'no-store'});
  if(r.ok){const j=await r.json();if(j.addr){polling=false;location.href='http://'+j.addr+'/';return}}}catch(e){}
  await new Promise(res=>setTimeout(res,2000))}}
async function scan(rescan){$('#scanmsg').innerHTML='<span class="spin"></span>Scanning…';const r=await api('/api/wifi'+(rescan?'?rescan=1':''));wifi.list=r.networks||[];render()}
async function loadDisks(){disks.loaded=true;disks.loading=true;render();const r=await api('/api/disks');disks.list=r.disks||[];disks.recovery=r.recovery&&r.recovery.found?r.recovery:null;disks.loading=false;render()}
function renderDisks(){const two=disks.sel.length===2;const rec=disks.recovery;
 if(disks.loading){view.innerHTML='<h2>2 · Storage</h2><p class="hint"><span class="spin"></span>Looking at the attached disks…</p>';return}
 if(rec&&!disks.wipe){view.innerHTML=`<h2>2 · Storage</h2>
  <div class="recover"><b>Found an existing Off The Cloud storage</b><br><small>${esc(rec.members.join(' + '))} · ${esc(rec.size_h)}${rec.has_database?' · with its database':''}</small>
  <p class="hint" style="margin:8px 0 0">If this Pi is replacing one that died, recover it: everything on these disks - photos, files, its name and its bridge identity - comes back as it was, and no new name is needed.</p></div>
  <div class="row"><button id="recover">Recover this device</button><button class="ghost" id="wipe">Start fresh instead</button></div>
  <div class="msg" id="dmsg"></div>`;
  $('#recover').onclick=async()=>{$('#recover').disabled=true;const r=await post('/api/install',{mode:'recover'});if(!r.ok){$('#dmsg').textContent=r.error||'Could not start';$('#dmsg').className='msg bad';$('#recover').disabled=false;return}step=4;refresh()};
  $('#wipe').onclick=()=>{disks.wipe=true;disks.sel=rec.members.slice(0,2);render()};return}
 view.innerHTML=`<h2>2 · Storage</h2><p class="hint">Pick <b>two</b> disks to mirror them as RAID1 - one can fail without losing anything. Pick none to keep everything on the SD card for now.</p>
 <ul class="list" id="dl">${disks.list.map(d=>`<li data-p="${esc(d.path)}" class="${disks.sel.includes(d.path)?'sel':''}"><span>${esc(d.model||d.path)}<br><small>${esc(d.path)} · ${esc(d.transport||'')}${d.in_use?' · currently mounted':''}</small></span><small>${esc(d.size_h)}</small></li>`).join('')||'<li><small>No extra disks found. Plug them in and tap Rescan, or continue without.</small></li>'}</ul>
 <div class="msg bad">${two?'⚠ Both disks will be wiped'+(rec?' - including the existing Off The Cloud storage on them':'')+'.':disks.sel.length===1?'Pick a second disk for RAID1, or none.':''}</div>
 <div class="row"><button id="go" ${disks.sel.length===0||two?'':'disabled'}>${two?'Wipe both and continue':'Continue without extra disks'}</button><button class="ghost" id="rescan">Rescan</button>${rec?'<button class="ghost" id="back">Back</button>':''}</div>`;
 document.querySelectorAll('#dl li[data-p]').forEach(li=>li.onclick=()=>{const p=li.dataset.p;disks.sel=disks.sel.includes(p)?disks.sel.filter(x=>x!==p):disks.sel.length<2?[...disks.sel,p]:disks.sel;render()});
 $('#rescan').onclick=loadDisks;const bk=$('#back');if(bk)bk.onclick=()=>{disks.wipe=false;disks.sel=[];render()};
 $('#go').onclick=()=>{step=3;render()}}
// Issue #124: the account step. The bridge - reaching the device from
// anywhere, friends, sharing, push - needs an account that the device's
// name is registered to; the device itself works without one. Sign in or
// sign up goes through the device (the hotspot's captive DNS lets only it
// reach the bridge); Google/Apple accounts get a setup code from the
// account page on another device instead.
async function loadProviders(){if(acct.providers)return;acct.providers=[];if(!window.otcApp||!window.otcSetupSignIn)return;try{const r=await api('/api/providers');acct.providers=r.providers||[];if(step===3)render()}catch(e){}}
async function providerSignIn(p){acct.msg='';try{const tok=await window.otcSetupSignIn(p);if(!tok)return;const r=await post('/api/account',{action:'code',setup_token:tok});if(!r.ok){acct.msg=r.error||'Could not sign in';render();return}acct.mode='login';await refresh();render()}catch(e){acct.msg=String(e&&e.message||e||'Sign-in cancelled');render()}}
function renderAccount(){const a=state.account||{};const m=acct.mode;loadProviders();
 if(acct.verify&&!a.email){view.innerHTML=`<h2>3 · Confirm your email</h2><p class="hint">We sent a link to <b>${esc(acct.verify.email)}</b>. Open it - on this phone or any other device - to confirm the address is yours, then come back and continue. Can't find it? Look in the spam folder.</p>
  <div class="row"><button id="v-go">I've confirmed it - continue</button><button class="ghost" id="v-back">Back</button></div><div class="msg ${acct.msg?'bad':''}" id="amsg">${esc(acct.msg)}</div>`;
  $('#v-back').onclick=()=>{acct.verify=null;acct.msg='';acct.mode='login';render()};
  $('#v-go').onclick=async()=>{$('#v-go').disabled=true;$('#amsg').innerHTML='<span class="spin"></span>Checking…';$('#amsg').className='msg';
   const r=await post('/api/account',{action:'login',email:acct.verify.email,password:acct.verify.password});
   if(r.ok&&r.verify){acct.msg='Not confirmed yet - open the link in the email (we have just sent it again).';render();return}
   if(!r.ok){acct.msg=r.error||'Could not sign in';render();return}
   acct.verify=null;acct.msg='';acct.mode='login';await refresh();step=4;render()};
  return}
 if(a.email&&m!=='change'){view.innerHTML=`<h2>3 · Your account</h2><p class="hint">Signed in as <b>${esc(a.email)}</b>. The device's name will be registered to this account.</p>
  <div class="row"><button id="next">Continue</button><button class="ghost" id="change">Use another account</button></div>`;
  $('#next').onclick=()=>{step=4;render()};$('#change').onclick=()=>{acct.mode='change';render()};return}
 const tabs=`<div class="row" style="margin-bottom:6px"><button class="ghost" id="t-login" style="${m==='login'?'border-color:var(--ember);color:var(--ink)':''}">Sign in</button><button class="ghost" id="t-signup" style="${m==='signup'?'border-color:var(--ember);color:var(--ink)':''}">Create account</button><button class="ghost" id="t-code" style="${m==='code'?'border-color:var(--ember);color:var(--ink)':''}">I have a setup code</button></div>`;
 const terms=`<div class="hint" style="border:1px solid var(--line);border-radius:10px;padding:10px 12px;margin-top:12px">Creating an account is only necessary to use our bridge: you will get up to <b>5 domains</b> like <i>name</i>.${esc(state.bridge)} to connect to your device and also connect with the devices of your friends and family. If you only want to use this device in your local network as a NAS or with Tailscale Funnel, skip the creation of the account.<br><br>An account for up to 5 domains is <b>free for the first two years</b>, and after this period will cost 9.99 euros + VAT a year (we will e-mail you before the period ends). If you need more domains, please contact us at <b>info@off-the.cloud</b>.</div>`;
 let form='';
 if(m==='login')form=`<label>Email</label><input type="text" id="a-email" autocapitalize="none" autocomplete="email" inputmode="email"><label>Password</label><input type="password" id="a-pass" autocomplete="current-password">
  <div class="row"><button id="a-go">Sign in</button></div>${(acct.providers||[]).length?'':`<p class="hint" style="margin-top:10px">Signed up with Google or Apple? Open <b>${esc(state.bridge)}/account</b> on another device, get a setup code and choose "I have a setup code".</p>`}`;
 else if(m==='signup')form=`<div class="row"><div style="flex:1"><label>Name</label><input type="text" id="a-name" autocomplete="given-name"></div><div style="flex:1"><label>Surname</label><input type="text" id="a-surname" autocomplete="family-name"></div></div>
  <label>Country of residence</label><select id="a-country" style="width:100%;font:inherit;padding:11px 12px;border-radius:10px;border:1px solid var(--line);background:var(--bg);color:var(--ink)"><option value="">Loading…</option></select>
  <label>Email</label><input type="text" id="a-email" autocapitalize="none" autocomplete="email" inputmode="email"><label>Password (8 characters or more)</label><input type="password" id="a-pass" autocomplete="new-password">${terms}
  <label style="display:flex;gap:8px;align-items:flex-start;color:var(--ink);font-size:.95rem"><input type="checkbox" id="a-terms" style="width:auto;margin-top:4px"><span>I accept the <a href="https://${esc(state.bridge)}/terms" target="_blank">terms of use</a> and have read the <a href="https://${esc(state.bridge)}/privacy" target="_blank">privacy notice</a>.</span></label>
  <div class="row"><button id="a-go">Create account</button></div>`;
 else form=`<label>Setup code</label><input type="text" id="a-code" autocapitalize="characters" autocomplete="off" spellcheck="false" placeholder="e.g. K7PX2M4Q" style="font-family:ui-monospace,monospace;letter-spacing:.15em;text-transform:uppercase">
  <p class="hint">On any device, open <b>${esc(state.bridge)}/account</b>, sign in (with your password, Google or Apple) and tap <b>Get a setup code</b>.</p><div class="row"><button id="a-go">Use this code</button></div>`;
 // In the app: Apple / Google as the main way in, email behind its own
 // button (issue #137) - the look sign-in pages use, Apple's logo and all.
 const APPLE='<svg viewBox="0 0 814 1000" fill="currentColor" aria-hidden="true"><path d="M788 341c-6 4-107 61-107 188 0 147 129 199 133 200-1 3-21 71-68 141-43 61-87 122-155 122s-86-40-164-40c-76 0-104 41-166 41s-106-57-155-127C49 784 0 646 0 515 0 305 137 193 271 193c72 0 131 47 176 47 43 0 110-50 192-50 31 0 143 3 149 151zM535 150c34-40 58-96 58-152 0-8-1-15-2-22-55 2-121 37-160 83-31 35-60 91-60 148 0 9 2 17 2 20 4 1 10 2 16 2 50 0 112-33 146-79z"/></svg>';
 const GOOGLE='<svg viewBox="0 0 48 48" aria-hidden="true"><path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9.1 3.6l6.8-6.8C35.8 2.4 30.2 0 24 0 14.6 0 6.6 5.4 2.7 13.3l7.9 6.2C12.5 13.6 17.8 9.5 24 9.5z"/><path fill="#4285F4" d="M46.1 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.4c-.5 2.9-2.2 5.4-4.7 7.1l7.6 5.9c4.4-4.1 6.8-10.1 6.8-17.5z"/><path fill="#FBBC05" d="M10.5 28.8c-.5-1.4-.8-3-.8-4.8s.3-3.3.8-4.8l-7.9-6.2C.9 16.4 0 20.1 0 24s.9 7.6 2.6 10.9l7.9-6.1z"/><path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.6-5.9c-2.1 1.4-4.9 2.3-8.3 2.3-6.2 0-11.5-4.2-13.4-9.9l-7.9 6.1C6.6 42.6 14.6 48 24 48z"/></svg>';
 const EMAIL='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M16 8v5a3 3 0 0 0 6 0v-1a10 10 0 1 0-4 8"/></svg>';
 const hasProv=(acct.providers||[]).length>0;
 const provs=hasProv?`${acct.providers.slice().sort((a,b)=>a==='apple'?-1:b==='apple'?1:0).map(p=>`<button class="sso ${p}" data-provider="${p}">${p==='apple'?APPLE:GOOGLE}<span>Continue with ${p==='apple'?'Apple':'Google'}</span></button>`).join('')}${acct.email?'<div class="sso-or">or with your email</div>':`<button class="sso email" id="a-email-open">${EMAIL}<span>Continue with Email</span></button>`}`:'';
 view.innerHTML=`<h2>3 · Your Off The Cloud account</h2><p class="hint">An account links this device's name to you, so you can reach it from anywhere, share with friends and replace it if it is ever lost.</p>${provs}${!hasProv||acct.email?tabs+form:''}
  <div class="msg ${acct.msg?'bad':''}" id="amsg">${esc(acct.msg)}</div>
  <details style="margin-top:14px"><summary style="color:var(--dim);cursor:pointer">Continue without an account</summary>
   <p class="hint" style="margin-top:8px">Without an account the device does not use the bridge. It still works as a NAS on your home network: files, photo backup from the apps and the sync clients while at home, the photo gallery, tags and people. What needs the bridge: reaching the device from outside your home, the social features (friends, posts, comments), share links that work from anywhere, and push notifications. You can join the bridge later, from Settings in the web app.</p>
   <div class="row"><button class="ghost" id="a-skip">Continue without an account</button></div></details>
  <div class="row" style="margin-top:6px"><button class="ghost" id="back">Back</button></div>`;
 document.querySelectorAll('button[data-provider]').forEach(b=>b.onclick=()=>providerSignIn(b.dataset.provider));
 const eo=$('#a-email-open');if(eo)eo.onclick=()=>{acct.email=true;acct.msg='';render()};
 $('#back').onclick=()=>{step=2;render()};
 $('#a-skip').onclick=async()=>{const r=await post('/api/account',{action:'skip'});if(!r.ok){acct.msg=r.error||'Could not continue';render();return}step=4;refresh();render()};
 // In the app the email options only exist once "Continue with Email" is chosen.
 if(!$('#t-login'))return;
 $('#t-login').onclick=()=>{acct.mode='login';acct.msg='';render()};$('#t-signup').onclick=()=>{acct.mode='signup';acct.msg='';render()};$('#t-code').onclick=()=>{acct.mode='code';acct.msg='';render()};
 if(m==='signup')loadCountries();
 $('#a-go').onclick=async()=>{const b=$('#a-go');b.disabled=true;$('#amsg').innerHTML='<span class="spin"></span>One moment…';$('#amsg').className='msg';let body;
  if(m==='login')body={action:'login',email:$('#a-email').value,password:$('#a-pass').value};
  else if(m==='signup')body={action:'signup',email:$('#a-email').value,password:$('#a-pass').value,name:$('#a-name').value,surname:$('#a-surname').value,country:$('#a-country').value,accept_terms:$('#a-terms').checked};
  else body={action:'code',setup_token:$('#a-code').value};
  const r=await post('/api/account',body);if(!r.ok){acct.msg=r.error||'Could not sign in';render();return}
  if(r.verify){acct.verify={email:r.email||body.email,password:body.password};acct.msg='';render();return}
  acct.msg='';acct.mode='login';await refresh();step=4;render()}}
async function loadCountries(){if(!acct.countries){const r=await api('/api/countries');if(r.ok){delete r.ok;delete r.status;acct.countries=r}else{$('#amsg').textContent=r.error||'Could not load the country list - check the device is online';$('#amsg').className='msg bad';return}}
 const sel=$('#a-country');if(!sel)return;const cur=sel.value;sel.innerHTML='<option value="">Choose…</option>'+Object.entries(acct.countries).sort((x,y)=>x[1].localeCompare(y[1])).map(([c,n])=>`<option value="${c}" ${c===cur?'selected':''}>${esc(n)}</option>`).join('')}
function renderName(rebind){const a=state.account||{};
 if(a.skip_bridge&&!rebind){view.innerHTML=`<h2>4 · No bridge</h2><p class="hint">You chose to continue without an account, so the device gets no internet address. On your home network it answers as <b>otc.local</b>.</p>
  ${ownerFields()}
  ${profileFields()}
  <div class="row"><button id="claim" ${ownerValid()?'':'disabled'}>Install</button><button class="ghost" id="back">Back</button></div><div class="msg" id="nmsg"></div>`;
  wireOwner(()=>{$('#claim').disabled=!ownerValid()});wireProfile();
  $('#back').onclick=()=>{step=3;render()};
  $('#claim').onclick=async()=>{$('#claim').disabled=true;const r=await post('/api/local-name',{});if(!r.ok){$('#nmsg').textContent=r.error||'Could not continue';$('#nmsg').className='msg bad';$('#claim').disabled=false;return}
   const i=await post('/api/install',await installBody());if(!i.ok){$('#nmsg').textContent=i.error||'Could not start the install';$('#nmsg').className='msg bad';$('#claim').disabled=false;return}step=5;refresh()};return}
 view.innerHTML=`<h2>${rebind?'Name your recovered device':'4 · Name your device'}</h2><p class="hint">${rebind?`The device is back, but it isn't reaching the bridge as <b>${esc(state.install.domain||'its old name')}</b> - that name may have been released. Choose a name to register it again; everything else is already recovered.`:'This becomes its address on the internet, for you and for friends. Letters, digits and hyphens.'}</p>
 <label>Device name</label><div class="row"><input type="text" id="nm" value="${esc(name.val)}" autocapitalize="none" autocomplete="off" spellcheck="false" placeholder="e.g. casa" style="flex:1"><span class="detail" style="white-space:nowrap">.${esc(state.bridge)}</span></div>
 <div class="msg ${name.ok===true?'ok':name.ok===false?'bad':''}" id="nmsg">${esc(name.msg)}</div>
 ${rebind?'':ownerFields()}
 ${rebind?'':profileFields()}
 <div class="row"><button id="claim" ${name.ok&&(rebind||ownerValid())?'':'disabled'}>${rebind?'Register':'Continue'}</button>${rebind?'':'<button class="ghost" id="back">Back</button>'}</div>`;
 nameRebind=rebind;const inp=$('#nm');inp.focus();let t;inp.oninput=()=>{name.val=inp.value.trim().toLowerCase();name.ok=null;name.warn=false;name.msg='';clearTimeout(t);nameStatus();if(name.val)t=setTimeout(check,400)};
 if(!rebind){wireOwner(()=>{$('#claim').disabled=!(name.ok&&ownerValid())});wireProfile()}
 const bk=$('#back');if(bk)bk.onclick=()=>{step=3;render()};
 $('#claim').onclick=async()=>{$('#claim').disabled=true;$('#nmsg').innerHTML='<span class="spin"></span>Reserving…';const r=await post('/api/name',{name:name.val});
  if(!r.ok){name.ok=false;name.msg=r.error||'Could not reserve that name';if(r.code==='login_required'){step=3;acct.msg=r.error}render();return}
  name.domain=r.domain;if(rebind){refresh();return}
  const i=await post('/api/install',await installBody());if(!i.ok){name.msg=i.error||'Could not start the install';name.ok=false;render();return}step=5;refresh()}}
async function check(){const v=name.val;const r=await api('/api/name?name='+encodeURIComponent(v));if(name.val!==v)return;if(!r.ok){name.ok=false;name.msg=r.error||'Could not check that name'}else{name.warn=false;if(r.available){name.ok=true;name.msg=`${r.domain} is available`}else if(r.yours===false){name.ok=false;name.msg=`${r.domain} belongs to someone else - choose another name`}else if(r.yours){name.ok=true;name.warn=true;name.msg=r.online?`${r.domain} is in use by one of your devices, online right now. If you continue, that device is removed from this address (it keeps working at home) and this one takes it.`:`${r.domain} belongs to one of your devices (offline now). If you continue, that device is removed from this address and this one takes it.`}else{name.ok=true;name.warn=true;name.msg=`${r.domain} is already taken. If it is one of your devices, continuing removes that device from this address and gives it to this one.`}}nameStatus()}
// The name check's answer only changes the message and the button: the
// field itself is never redrawn, so the cursor stays where the person is
// typing (a full render put it back at the start, on Android).
let nameRebind=false;
function nameStatus(){const m=$('#nmsg'),c=$('#claim');if(!m||!c)return render();m.textContent=name.msg;m.className='msg '+(name.warn||name.ok===false?'bad':name.ok===true?'ok':'');c.disabled=!(name.ok&&(nameRebind||ownerValid()))}
function renderInstall(){const i=state.install;const pct=i.total?Math.round(100*i.step/i.total):0;const dom=i.domain||state.domain||name.domain;
 if(i.phase==='online'&&!dom){view.innerHTML=`<h2>Ready 🎉</h2><p class="hint">Everything is installed. On your home network the device answers at</p><p style="font-size:1.2rem"><a href="http://otc.local:8080"><b>http://otc.local:8080</b></a></p><p class="hint">Open that address from any device at home - it will ask you to choose the owner password first. Want it reachable from anywhere later? Create an account at ${esc(state.bridge)} and run the setup again.</p><p class="hint">The "Off The Cloud" hotspot switches off in a minute.</p>`;return}
 if(i.phase==='online'&&window.otcApp){view.innerHTML=`<h2>Ready 🎉</h2><p class="hint">${i.recovery?'Your device is back, with everything it had.':'Everything is installed.'}${dom?` It lives at <b>${esc(dom)}</b>.`:''}</p><p class="hint">${i.recovery?'Enter its password below to open it in the app.':'Choose its password below - the app then opens it straight away.'}</p>`;return}
 if(i.phase==='online'){view.innerHTML=`<h2>Ready 🎉</h2><p class="hint">${i.recovery?'Your device is back, with everything it had.':'Everything is installed.'} From now on it lives at</p><p style="font-size:1.2rem"><a href="https://${esc(dom)}"><b>https://${esc(dom)}</b></a></p><p class="hint">Open that address in your browser - remember it, it is how you reach your device from anywhere.${i.recovery?'':' It will ask you to choose the owner password first.'}</p><p class="hint">The "Off The Cloud" hotspot switches off in a minute.</p>`;return}
 if(i.phase==='needs_name'){renderName(true);return}
 if(i.phase==='verifying'){view.innerHTML=`<h2>5 · Almost there</h2><p class="hint">Installed. Waiting for the device to connect to the bridge${dom?' as <b>'+esc(dom)+'</b>':''}…</p><div class="bar"><div style="width:100%"></div></div><div class="step"><span class="spin"></span>Checking with ${esc(state.bridge)}</div>`;return}
 if(i.phase==='offline'){view.innerHTML=`<h2>5 · Installed, but not reachable yet</h2><p class="hint">The install finished, but ${esc(state.bridge)} doesn't see <b>${esc(dom)}</b> connected. Usually this just needs a moment more.</p>
  <div class="row"><button id="again">Check again</button><button class="ghost" id="anyway">Open it anyway</button></div>
  <details style="margin-top:14px"><summary style="color:var(--dim);cursor:pointer">Log</summary><pre>${esc((i.log_tail||[]).join('\n'))}</pre></details>`;
  $('#again').onclick=async()=>{await post('/api/verify');refresh()};$('#anyway').onclick=async()=>{await post('/api/finish');location.href='https://'+dom};return}
 const failed=i.phase==='failed';
 view.innerHTML=`<h2>5 · ${i.recovery?'Recovering':'Installing'}</h2><p class="hint">Downloading and setting everything up. This takes a while on a Raspberry Pi - keep the device powered.</p>
 ${dom&&!failed?(window.otcApp?`<p style="padding:10px 12px;border:1px solid var(--ok);border-radius:10px">The installation takes about 20 minutes. <b>Keep this screen open</b> and the phone next to the device to follow it here; when it is done the app offers to connect to <b>${esc(dom)}</b>. Closed it? Open <b>Set up a new device</b> again and the progress comes back.</p>`:`<p style="padding:10px 12px;border:1px solid var(--ok);border-radius:10px"><b>You can close this window and disconnect now.</b> The installation takes about 20 minutes. When it is complete, open <b>https://${esc(dom)}</b> from any network. To check the progress meanwhile, connect to the "Off The Cloud" WiFi again and this page comes back.</p>`):''}
 <div class="bar"><div style="width:${failed?100:pct}%;${failed?'background:var(--bad)':''}"></div></div>
 <div class="step">${failed?'Failed':'<span class="spin"></span>'+esc(i.text||'Starting…')} <small style="color:var(--dim)">${i.step}/${i.total}</small></div>
 <div class="detail">${esc(failed?(i.error||''):(i.detail||''))}</div>
 ${failed?'<div class="row"><button id="retry">Try again</button></div>':''}
 <details style="margin-top:14px"><summary style="color:var(--dim);cursor:pointer">Log</summary><pre>${esc((i.log_tail||[]).join('\n'))}</pre></details>`;
 const rt=$('#retry');if(rt)rt.onclick=async()=>{await post('/api/install',i.recovery?{mode:'recover'}:await installBody());refresh()}}
// The WiFi list on opening: in the app (Bluetooth) a fresh scan straight
// away - nothing to lose there. Over the hotspot the list saved before it
// started, since scanning takes the one radio off the hotspot and drops
// the phone's page - unless that list is empty, then a fresh one anyway.
(async()=>{await refresh();if(step===1){await scan(!!window.otcApp);if(!window.otcApp&&!wifi.list.length)await scan(true)}setInterval(refresh,3000)})();
</script></body></html>
"""


def main():
    if Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"]:
        print("[otc-setup] this device is already set up - nothing to do")
        return
    global SEAL
    SEAL = SealKey()
    Path(CONFIG["state_file"]).parent.mkdir(parents=True, exist_ok=True)
    threading.Thread(target=beacon_loop, daemon=True).start()
    # Security advisory (setup wizard): setup is over Bluetooth only - the
    # apps reach this through setup_ble.py on this machine. Listening on
    # every interface, unauthenticated, on an open hotspot let anyone
    # nearby drive a root installer (down to wiping the disks).
    bind = os.environ.get("OTC_SETUP_BIND", "127.0.0.1")
    srv = ThreadingHTTPServer((bind, CONFIG["port"]), Handler)
    print(f"[otc-setup] serving the setup wizard on port {CONFIG['port']}")
    srv.serve_forever()


if __name__ == "__main__":
    main()
