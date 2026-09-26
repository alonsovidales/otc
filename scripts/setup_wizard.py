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

import json
import os
import re
import secrets
import subprocess
import sys
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
        "https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/install.sh"),
    # install.sh sources this for the device identity instead of
    # generating its own, so the name reserved on the bridge and the
    # identity the device later presents are the same thing.
    "env_file": "/etc/otc/otc-install.env",
    "state_file": "/var/lib/otc/setup-state.json",
    "join_request": "/var/lib/otc/wifi_join_request.json",
    "join_result": "/var/lib/otc/wifi_join_result.json",
    # network_setup.py keeps the hotspot up until this exists.
    "setup_done_marker": "/var/lib/otc/setup-done",
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
STEP_RE = re.compile(r"\[otc-install\] \[(\d+)/(\d+)\] (.*)")
ERROR_RE = re.compile(r"\[otc-install\] ERROR: (.*)")
INFO_RE = re.compile(r"\[otc-install\] (.*)")
# "md0 : active (auto-read-only) raid1 sda[0] sdb[1]" - members are the
# tokens carrying a [slot]; the level and any parenthesised state are not.
MDSTAT_RE = re.compile(r"^(md\d+)\s*:\s*active\b(.*)$")
MEMBER_RE = re.compile(r"^([A-Za-z0-9_/-]+)\[\d+\]")


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
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(data))
    os.chmod(p, mode)


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
    live scan when asked for one / there is none. 2.4GHz networks only:
    setup happens on that band (see network_setup.py)."""
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
        if freq >= 3000:
            continue
        security = parts[2].strip()
        if ssid not in best or best[ssid]["signal"] < signal:
            best[ssid] = {"ssid": ssid, "signal": signal, "secured": security != "", "security": security, "freq": freq}
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

def setup_token():
    """One random token per setup, kept in the state file so a wizard
    restart doesn't orphan the page that already holds it."""
    st = load_state()
    if not st.get("token"):
        st["token"] = secrets.token_urlsafe(32)
        save_state(st)
    return st["token"]


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


def bridge_get(path):
    req = urllib.request.Request(f"https://{CONFIG['bridge']}{path}",
                                 headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=8) as r:
        return r.status, json.loads(r.read().decode() or "{}")


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
    env_path = Path(CONFIG["env_file"])
    env_path.parent.mkdir(parents=True, exist_ok=True)
    env_path.write_text(
        "# Generated by the OTC setup wizard. Keep out of git - this is the\n"
        "# device's DB password and bridge secret.\n"
        + "".join(f"{k}={v}\n" for k, v in identity.items()))
    os.chmod(env_path, 0o600)


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
    """Who a typed setup code belongs to, or None."""
    try:
        status, data = bridge_get("/api/account/setup-token-info?token=" + urllib.parse.quote(token))
    except Exception:  # noqa: BLE001
        return None
    return data if status == 200 else None


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
            cmd = ["bash", "-c", 'curl -fsSL --retry 3 "$0" | bash -s -- "$1"', CONFIG["install_url"], name]
        Path(CONFIG["install_log"]).parent.mkdir(parents=True, exist_ok=True)
        with open(CONFIG["install_log"], "a") as logf:
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
        threading.Thread(target=self._exit_later, daemon=True).start()

    def _exit_later(self):
        time.sleep(45)
        Path(CONFIG["setup_done_marker"]).touch()
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

    def read_body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n <= 0 or n > 65536:
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
        elif path == "/api/name":
            name = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("name", [""])[0].strip().lower()
            if not NAME_RE.match(name):
                self.send_json(400, {"error": "lower-case letters, digits and hyphens only"})
                return
            try:
                status, data = bridge_get("/api/name-available?name=" + urllib.parse.quote(name))
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
            print(f"[otc-setup] join requested: {ssid!r} security={security!r} password={len(password)} chars")
            Path(CONFIG["join_result"]).unlink(missing_ok=True)
            # network_setup.py (root, owns the radio) does the actual join
            # and moves the hotspot onto the joined network's channel; the
            # request is handed over after a moment so the page can say so.
            def later():
                time.sleep(CONFIG["join_delay_s"])
                write_json(CONFIG["join_request"], {"ssid": ssid, "password": password, "security": security}, mode=0o600)
            threading.Thread(target=later, daemon=True).start()
            self.send_json(202, {"ok": True, "delay_s": CONFIG["join_delay_s"]})

        elif path == "/api/account":
            # Issue #124: sign in / sign up / a typed setup code / skip.
            action = str(body.get("action", ""))
            st = load_state()
            if action == "skip":
                st.update({"skip_bridge": True, "setup_token": "", "account_email": ""})
                save_state(st)
                self.send_json(200, {"ok": True})
                return
            if action == "code":
                token = str(body.get("setup_token", "")).strip()
                who = setup_token_owner(token)
                if not who:
                    self.send_json(404, {"error": "that setup code is not valid or has expired"})
                    return
                st.update({"skip_bridge": False, "setup_token": token, "account_email": who.get("email", "")})
                save_state(st)
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
            if status not in (200, 201) or not data.get("setup_token"):
                self.send_json(status if status >= 400 else 502, {"error": data.get("error", "could not sign in")})
                return
            st.update({"skip_bridge": False, "setup_token": data["setup_token"],
                       "account_email": (data.get("account") or {}).get("email", fields["email"])})
            save_state(st)
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
                    st.update({"name": name, "domain": domain})
                    save_state(st)
                    threading.Thread(target=install.verify, daemon=True).start()
                else:
                    write_env_file(identity)
                    st.update({"name": name, "domain": domain})
                    save_state(st)
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
            st.update({"name": "otc", "domain": ""})
            save_state(st)
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
                st["disks"] = disks
                save_state(st)
                install.start("recovery", disks, recovery=True)
            else:
                if not st.get("name"):
                    self.send_json(400, {"error": "choose the device's name first"})
                    return
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
                st["disks"] = disks
                save_state(st)
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
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.row>*{margin-top:0}
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
</style></head><body><main>
<h1>Off The Cloud</h1><p class="lead">Let's set up your device.</p>
<ol class="steps"><li id="s1"></li><li id="s2"></li><li id="s3"></li><li id="s4"></li><li id="s5"></li></ol>
<div id="view" class="card">Loading…</div>
</main>
<script>
const $=s=>document.querySelector(s);const view=$('#view');
let state=null,step=1,wifi={list:[],sel:null,joining:false},name={val:'',ok:null,domain:'',msg:''},disks={list:[],sel:[],recovery:null,loaded:false,wipe:false},acct={mode:'login',msg:'',busy:false,countries:null};
const api=async(p,o)=>{const r=await fetch(p,Object.assign({cache:'no-store'},o||{}));let j={};try{j=await r.json()}catch(e){}return{ok:r.ok,status:r.status,...j}};
const post=(p,b)=>api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(b||{})});
const esc=s=>String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
function marks(){for(let i=1;i<=5;i++){const li=$('#s'+i);li.className=i<step?'done':i===step?'on':''}}
let lastKey='';
async function refresh(){let st;try{st=await api('/api/state')}catch(e){return}
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
 view.innerHTML=`<h2>1 · Connect to your WiFi</h2><p class="hint">${online?`The device is online${cur?' via <b>'+esc(cur)+'</b>':''}. You can continue, or join a different network.`:'Choose the network the device should use (2.4 GHz networks are listed; it can move to 5 GHz once set up).'}</p>
 <div class="row"><button class="ghost" id="rescan" style="margin-top:0">Scan again</button><span id="scanmsg" class="detail"></span></div>
 <ul class="list" id="nets">${wifi.list.map(n=>`<li data-ssid="${esc(n.ssid)}" class="${wifi.sel===n.ssid?'sel':''}"><span>${esc(n.ssid)}${n.secured?' 🔒':''}</span><small>${n.signal}%</small></li>`).join('')||'<li><small>No networks yet - tap Scan.</small></li>'}</ul>
 <label>Password</label><input type="password" id="pw" placeholder="${wifi.sel?'Password for '+esc(wifi.sel):'Pick a network first'}" ${wifi.sel?'':'disabled'}>
 <div class="row"><button id="join" ${wifi.sel&&!wifi.joining?'':'disabled'}>${wifi.joining?'<span class="spin"></span>Connecting…':'Connect'}</button>${online?'<button class="ghost" id="skip">Continue</button>':''}</div>
 <div class="msg ${jr&&jr.ok===false?'bad':''}" id="wmsg">${jr&&jr.ok===false?esc(jr.error||'Could not join that network - check the password.')+' The hotspot is back - reconnect to it.':''}</div>`;
 if(wifi.joining){view.innerHTML=`<h2>1 · Joining ${esc(wifi.sel)}</h2>
  <div class="step"><span class="spin"></span>Connecting and waiting for the internet - up to a minute.</div>
  <p style="margin-top:12px;padding:10px 12px;border:1px solid var(--ember);border-radius:10px"><b>If you get disconnected, reconnect to the "Off The Cloud" WiFi.</b> The hotspot restarts for a few seconds to switch to your network's channel; most phones rejoin it by themselves, but if yours doesn't, pick "Off The Cloud" again in your WiFi settings and come back to this page.</p>
  <p class="hint">Keep this page open - it continues on its own once the device is online.</p>
  <p class="hint">A wrong password shows up here as an error; just try again.</p>`;pollBridge();return}
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
function renderAccount(){const a=state.account||{};const m=acct.mode;
 if(a.email&&m!=='change'){view.innerHTML=`<h2>3 · Your account</h2><p class="hint">Signed in as <b>${esc(a.email)}</b>. The device's name will be registered to this account.</p>
  <div class="row"><button id="next">Continue</button><button class="ghost" id="change">Use another account</button></div>`;
  $('#next').onclick=()=>{step=4;render()};$('#change').onclick=()=>{acct.mode='change';render()};return}
 const tabs=`<div class="row" style="margin-bottom:6px"><button class="ghost" id="t-login" style="${m==='login'?'border-color:var(--ember);color:var(--ink)':''}">Sign in</button><button class="ghost" id="t-signup" style="${m==='signup'?'border-color:var(--ember);color:var(--ink)':''}">Create account</button><button class="ghost" id="t-code" style="${m==='code'?'border-color:var(--ember);color:var(--ink)':''}">I have a setup code</button></div>`;
 const terms=`<div class="hint" style="border:1px solid var(--line);border-radius:10px;padding:10px 12px;margin-top:12px">Free for the first <b>2 years</b>, then 19.99 € a year (we email you before, nothing to pay today). Up to <b>5 domains</b> per account, extra ones 5 € a year each - write to info@off-the.cloud. An account unused for 6 months is removed. We keep only your name, country of residence and email.</div>`;
 let form='';
 if(m==='login')form=`<label>Email</label><input type="text" id="a-email" autocapitalize="none" autocomplete="email" inputmode="email"><label>Password</label><input type="password" id="a-pass" autocomplete="current-password">
  <div class="row"><button id="a-go">Sign in</button></div><p class="hint" style="margin-top:10px">Signed up with Google or Apple? Open <b>${esc(state.bridge)}/account</b> on another device, get a setup code and choose "I have a setup code".</p>`;
 else if(m==='signup')form=`<div class="row"><div style="flex:1"><label>Name</label><input type="text" id="a-name" autocomplete="given-name"></div><div style="flex:1"><label>Surname</label><input type="text" id="a-surname" autocomplete="family-name"></div></div>
  <label>Country of residence</label><select id="a-country" style="width:100%;font:inherit;padding:11px 12px;border-radius:10px;border:1px solid var(--line);background:var(--bg);color:var(--ink)"><option value="">Loading…</option></select>
  <label>Email</label><input type="text" id="a-email" autocapitalize="none" autocomplete="email" inputmode="email"><label>Password (8 characters or more)</label><input type="password" id="a-pass" autocomplete="new-password">${terms}
  <label style="display:flex;gap:8px;align-items:flex-start;color:var(--ink);font-size:.95rem"><input type="checkbox" id="a-terms" style="width:auto;margin-top:4px">I accept the terms above.</label>
  <div class="row"><button id="a-go">Create account</button></div>`;
 else form=`<label>Setup code</label><input type="text" id="a-code" autocapitalize="characters" autocomplete="off" spellcheck="false" placeholder="e.g. K7PX2M4Q" style="font-family:ui-monospace,monospace;letter-spacing:.15em;text-transform:uppercase">
  <p class="hint">On any device, open <b>${esc(state.bridge)}/account</b>, sign in (with your password, Google or Apple) and tap <b>Get a setup code</b>.</p><div class="row"><button id="a-go">Use this code</button></div>`;
 view.innerHTML=`<h2>3 · Your Off The Cloud account</h2><p class="hint">An account links this device's name to you, so you can reach it from anywhere, share with friends and replace it if it is ever lost.</p>${tabs}${form}
  <div class="msg ${acct.msg?'bad':''}" id="amsg">${esc(acct.msg)}</div>
  <details style="margin-top:14px"><summary style="color:var(--dim);cursor:pointer">Continue without an account</summary>
   <p class="hint" style="margin-top:8px">Without an account the device does not use the bridge. It still works as a NAS on your home network: files, photo backup from the apps and the sync clients while at home, the photo gallery, tags and people. What needs the bridge: reaching the device from outside your home, the social features (friends, posts, comments), share links that work from anywhere, and push notifications. You can create an account later and set the device up again.</p>
   <div class="row"><button class="ghost" id="a-skip">Continue without an account</button></div></details>
  <div class="row" style="margin-top:6px"><button class="ghost" id="back">Back</button></div>`;
 $('#t-login').onclick=()=>{acct.mode='login';acct.msg='';render()};$('#t-signup').onclick=()=>{acct.mode='signup';acct.msg='';render()};$('#t-code').onclick=()=>{acct.mode='code';acct.msg='';render()};
 $('#back').onclick=()=>{step=2;render()};
 if(m==='signup')loadCountries();
 $('#a-skip').onclick=async()=>{const r=await post('/api/account',{action:'skip'});if(!r.ok){acct.msg=r.error||'Could not continue';render();return}step=4;refresh();render()};
 $('#a-go').onclick=async()=>{const b=$('#a-go');b.disabled=true;$('#amsg').innerHTML='<span class="spin"></span>One moment…';$('#amsg').className='msg';let body;
  if(m==='login')body={action:'login',email:$('#a-email').value,password:$('#a-pass').value};
  else if(m==='signup')body={action:'signup',email:$('#a-email').value,password:$('#a-pass').value,name:$('#a-name').value,surname:$('#a-surname').value,country:$('#a-country').value,accept_terms:$('#a-terms').checked};
  else body={action:'code',setup_token:$('#a-code').value};
  const r=await post('/api/account',body);if(!r.ok){acct.msg=r.error||'Could not sign in';render();return}
  acct.msg='';acct.mode='login';await refresh();step=4;render()}}
async function loadCountries(){if(!acct.countries){try{const r=await fetch(`https://${state.bridge}/api/account/countries`,{cache:'no-store'});acct.countries=await r.json()}catch(e){acct.countries={}}}
 const sel=$('#a-country');if(!sel)return;const cur=sel.value;sel.innerHTML='<option value="">Choose…</option>'+Object.entries(acct.countries).sort((x,y)=>x[1].localeCompare(y[1])).map(([c,n])=>`<option value="${c}" ${c===cur?'selected':''}>${esc(n)}</option>`).join('')}
function renderName(rebind){const a=state.account||{};
 if(a.skip_bridge&&!rebind){view.innerHTML=`<h2>4 · No bridge</h2><p class="hint">You chose to continue without an account, so the device gets no internet address. On your home network it answers as <b>otc.local</b>.</p>
  <div class="row"><button id="claim">Install</button><button class="ghost" id="back">Back</button></div><div class="msg" id="nmsg"></div>`;
  $('#back').onclick=()=>{step=3;render()};
  $('#claim').onclick=async()=>{$('#claim').disabled=true;const r=await post('/api/local-name',{});if(!r.ok){$('#nmsg').textContent=r.error||'Could not continue';$('#nmsg').className='msg bad';$('#claim').disabled=false;return}
   const i=await post('/api/install',{mode:'fresh',disks:disks.sel,confirm_wipe:true});if(!i.ok){$('#nmsg').textContent=i.error||'Could not start the install';$('#nmsg').className='msg bad';$('#claim').disabled=false;return}step=5;refresh()};return}
 view.innerHTML=`<h2>${rebind?'Name your recovered device':'4 · Name your device'}</h2><p class="hint">${rebind?`The device is back, but it isn't reaching the bridge as <b>${esc(state.install.domain||'its old name')}</b> - that name may have been released. Choose a name to register it again; everything else is already recovered.`:'This becomes its address on the internet, for you and for friends. Letters, digits and hyphens.'}</p>
 <label>Device name</label><div class="row"><input type="text" id="nm" value="${esc(name.val)}" autocapitalize="none" autocomplete="off" spellcheck="false" placeholder="e.g. casa" style="flex:1"><span class="detail" style="white-space:nowrap">.${esc(state.bridge)}</span></div>
 <div class="msg ${name.ok===true?'ok':name.ok===false?'bad':''}" id="nmsg">${esc(name.msg)}</div>
 <div class="row"><button id="claim" ${name.ok?'':'disabled'}>${rebind?'Register':'Continue'}</button>${rebind?'':'<button class="ghost" id="back">Back</button>'}</div>`;
 const inp=$('#nm');inp.focus();let t;inp.oninput=()=>{name.val=inp.value.trim().toLowerCase();name.ok=null;name.msg='';clearTimeout(t);if(!name.val)return render();t=setTimeout(check,400)};
 const bk=$('#back');if(bk)bk.onclick=()=>{step=3;render()};
 $('#claim').onclick=async()=>{$('#claim').disabled=true;$('#nmsg').innerHTML='<span class="spin"></span>Reserving…';const r=await post('/api/name',{name:name.val});
  if(!r.ok){name.ok=false;name.msg=r.error||'Could not reserve that name';if(r.code==='login_required'){step=3;acct.msg=r.error}render();return}
  name.domain=r.domain;if(rebind){refresh();return}
  const i=await post('/api/install',{mode:'fresh',disks:disks.sel,confirm_wipe:true});if(!i.ok){name.msg=i.error||'Could not start the install';name.ok=false;render();return}step=5;refresh()}}
async function check(){const v=name.val;const r=await api('/api/name?name='+encodeURIComponent(v));if(name.val!==v)return;if(!r.ok){name.ok=false;name.msg=r.error||'Could not check that name'}else{name.ok=r.available;name.msg=r.available?`${r.domain} is available`:`${r.domain} is already taken - if it is one of your own, continuing moves it to this device`;if(!r.available)name.ok=true}render()}
function renderInstall(){const i=state.install;const pct=i.total?Math.round(100*i.step/i.total):0;const dom=i.domain||state.domain||name.domain;
 if(i.phase==='online'&&!dom){view.innerHTML=`<h2>Ready 🎉</h2><p class="hint">Everything is installed. On your home network the device answers at</p><p style="font-size:1.2rem"><a href="http://otc.local:8080"><b>http://otc.local:8080</b></a></p><p class="hint">Open that address from any device at home - it will ask you to choose the owner password first. Want it reachable from anywhere later? Create an account at ${esc(state.bridge)} and run the setup again.</p><p class="hint">The "Off The Cloud" hotspot switches off in a minute.</p>`;return}
 if(i.phase==='online'){view.innerHTML=`<h2>Ready 🎉</h2><p class="hint">${i.recovery?'Your device is back, with everything it had.':'Everything is installed.'} From now on it lives at</p><p style="font-size:1.2rem"><a href="https://${esc(dom)}"><b>https://${esc(dom)}</b></a></p><p class="hint">Open that address in your browser - remember it, it is how you reach your device from anywhere.${i.recovery?'':' It will ask you to choose the owner password first.'}</p><p class="hint">The "Off The Cloud" hotspot switches off in a minute.</p>`;return}
 if(i.phase==='needs_name'){renderName(true);return}
 if(i.phase==='verifying'){view.innerHTML=`<h2>5 · Almost there</h2><p class="hint">Installed. Waiting for the device to connect to the bridge${dom?' as <b>'+esc(dom)+'</b>':''}…</p><div class="bar"><div style="width:100%"></div></div><div class="step"><span class="spin"></span>Checking with ${esc(state.bridge)}</div>`;return}
 if(i.phase==='offline'){view.innerHTML=`<h2>5 · Installed, but not reachable yet</h2><p class="hint">The install finished, but ${esc(state.bridge)} doesn't see <b>${esc(dom)}</b> connected. Usually this just needs a moment more.</p>
  <div class="row"><button id="again">Check again</button><button class="ghost" id="anyway">Open it anyway</button></div>
  <details style="margin-top:14px"><summary style="color:var(--dim);cursor:pointer">Log</summary><pre>${esc((i.log_tail||[]).join('\n'))}</pre></details>`;
  $('#again').onclick=async()=>{await post('/api/verify');refresh()};$('#anyway').onclick=async()=>{await post('/api/finish');location.href='https://'+dom};return}
 const failed=i.phase==='failed';
 view.innerHTML=`<h2>5 · ${i.recovery?'Recovering':'Installing'}</h2><p class="hint">Downloading and setting everything up. This takes a while on a Raspberry Pi - keep the device powered.</p>
 ${dom&&!failed?`<p style="padding:10px 12px;border:1px solid var(--ok);border-radius:10px"><b>You can close this window and disconnect now.</b> The installation takes about 20 minutes. When it is complete, open <b>https://${esc(dom)}</b> from any network. To check the progress meanwhile, connect to the "Off The Cloud" WiFi again and this page comes back.</p>`:''}
 <div class="bar"><div style="width:${failed?100:pct}%;${failed?'background:var(--bad)':''}"></div></div>
 <div class="step">${failed?'Failed':'<span class="spin"></span>'+esc(i.text||'Starting…')} <small style="color:var(--dim)">${i.step}/${i.total}</small></div>
 <div class="detail">${esc(failed?(i.error||''):(i.detail||''))}</div>
 ${failed?'<div class="row"><button id="retry">Try again</button></div>':''}
 <details style="margin-top:14px"><summary style="color:var(--dim);cursor:pointer">Log</summary><pre>${esc((i.log_tail||[]).join('\n'))}</pre></details>`;
 const rt=$('#retry');if(rt)rt.onclick=async()=>{await post('/api/install',i.recovery?{mode:'recover'}:{mode:'fresh',disks:disks.sel,confirm_wipe:true});refresh()}}
(async()=>{await refresh();if(step===1)scan(false);setInterval(refresh,3000)})();
</script></body></html>
"""


def main():
    if Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"]:
        print("[otc-setup] this device is already set up - nothing to do")
        return
    Path(CONFIG["state_file"]).parent.mkdir(parents=True, exist_ok=True)
    threading.Thread(target=beacon_loop, daemon=True).start()
    srv = ThreadingHTTPServer(("0.0.0.0", CONFIG["port"]), Handler)
    print(f"[otc-setup] serving the setup wizard on port {CONFIG['port']}")
    srv.serve_forever()


if __name__ == "__main__":
    main()
