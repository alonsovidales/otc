#!/usr/bin/python3 -I
# SPDX-License-Identifier: AGPL-3.0-or-later
"""The root half of Settings > Wi-Fi: joins the Wi-Fi network the owner
chose - a new one, or the same one with its new password - and keeps it
only if it works.

The device (otc.service, unprivileged with NoNewPrivileges) writes its
request to /run/otc/wifi-join.request (a tmpfs: it holds the password) and
otc-wifi-join.path starts this as root. The answer goes to
/run/otc-wifi-join/status.json, root's own directory, which the device
reads (network.Status):

    {"v": 1, "id": ..., "state": "joining|connected|failed", "code": ...,
     "ssid": ..., "left": bool, "restored": bool, "uptime": <seconds>}

code is one of CODES (the device's dev.status.wifi_join.* texts); left: the
device had left its Wi-Fi network for the new one; restored: and is back on
it.

How a change goes, safest first:

  1. The request is taken (renamed into root's directory, never
     followed), checked - a regular file of the otc user's, one link, at
     most 1 KiB, exactly the keys it should have - and validated with the
     device's own rules. Nothing else the otc user wrote is opened.
  2. At most 10 changes in 15 minutes; never during an update
     (/run/otc-update.lock) nor a Wi-Fi restart (/run/otc-wifi.lock, held
     for the whole change and shared with otc-wifi-restart-runner).
  3. The network is looked for before anything changes (a name that isn't
     there, a security this doesn't join, a missing password: refused with
     the current connection untouched).
  4. The new profile is a trial, written to NetworkManager's volatile
     directory (/run/NetworkManager/system-connections, RAM) with
     autoconnect off: a wrong password never reaches the SD card, and the
     password never appears on a command line. A reboot or power cut
     during the trial throws it away; the old profile was never touched.
  5. The device joins it, needs an address, then must reach the internet
     through it: the bridge when it has one (curl bound to the Wi-Fi
     interface - binding the address alone would leave through a cable),
     else its gateway. A captive portal or an isolated guest network fails
     here.
  6. Only then is it saved (`nmcli connection modify` without --temporary
     moves it to /etc), checked to be saved, flushed (os.sync), recorded
     as committed - and only then are the old OTC-WiFi profiles deleted
     and other profiles for the same network kept from autoconnecting with
     their old password.
  7. Any failure after the switch deletes the trial and brings the
     previous connection back. ExecStopPost (--stopped) does the same for
     a run cut short, and never undoes a committed change; the next change
     does it first for a run whose --stopped never ran (systemctl kill).

The journal gets no network name and no password: lengths and codes only.

--selftest checks all of it without NetworkManager or root (on a Mac too):
python3 scripts/device-runner/otc-wifi-join-runner.py --selftest
"""

import fcntl
import ipaddress
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import uuid as uuidlib

# Every code this writes: the device's dev.status.wifi_join.* table but
# no_answer, which only the device decides (devtext's
# TestWifiJoinRunnerCodes compares the two).
CODES = frozenset({
    "joining", "connected", "wrong_password", "auth_failed", "not_found", "timed_out",
    "no_address", "no_internet", "unsupported_security", "password_required", "open_network",
    "bad_request", "no_wifi", "busy", "update_running", "too_many", "interrupted", "join_failed",
})

MAX_REQUEST = 1024
MAX_SSID = 32
MAX_RUNS = 10
RUNS_WINDOW = 900.0
LOCK_WAIT = 10.0
# `nmcli --wait` for the new network: longer than NetworkManager's own DHCP
# timeout (ipv4.dhcp-timeout, 45 s), so a slow DHCP server isn't cut short.
JOIN_WAIT = 60
SCAN_RETRIES = 3
SCAN_RETRY_WAIT = 5.0
SCAN_RETRY_FOR = 40.0
OUR_NAME = "OTC-WiFi"
TRIAL_NAME = "OTC-WiFi-new"
PRIORITY = 10
REQUEST_KEYS = frozenset({"v", "id", "ssid", "psk", "hidden"})
ID_RE = re.compile(r"[0-9a-f]{16}")
UUID_RE = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
IFACE_RE = re.compile(r"[A-Za-z0-9_.-]{1,15}")
HOST_RE = re.compile(r"[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?")
ENV_RE = re.compile(r"[A-Za-z0-9_-]{1,32}")
TRIAL_FILE_RE = re.compile(r"otc-wifi-[0-9a-f]{8}\.nmconnection(\.tmp)?")
UNSUPPORTED = ("802.1X", "WEP", "OWE")
ADDR_GEN_MODES = frozenset({"eui64", "stable-privacy", "default-or-eui64", "default"})
WIFI_TYPES = ("802-11-wireless", "wifi")

SUBPROCESS_ENV = {"LC_ALL": "C.UTF-8", "PATH": "/usr/sbin:/usr/bin:/sbin:/bin"}


class Paths:
    def __init__(self, root=""):
        self.request = root + "/run/otc/wifi-join.request"
        self.state_dir = root + "/run/otc-wifi-join"
        self.nm_dir = root + "/run/NetworkManager"
        self.nm_run_dir = self.nm_dir + "/system-connections"
        self.wifi_lock = root + "/run/otc-wifi.lock"
        self.update_lock = root + "/run/otc-update.lock"
        # The device's request for an update, before the update runner
        # takes it: only ever lstat'ed (the otc user's directory).
        self.update_request = root + "/var/lib/otc/update.request"
        self.etc = root + "/etc"

    @property
    def status(self):
        return os.path.join(self.state_dir, "status.json")

    @property
    def state(self):
        return os.path.join(self.state_dir, "state.json")

    @property
    def history(self):
        return os.path.join(self.state_dir, "history")


def log(msg):
    print("[otc-wifi-join] " + msg, flush=True)


def real_run(argv, timeout):
    """(returncode, stdout, stderr) of argv - never a shell - in the C
    locale with UTF-8. A timeout is 124, a missing program 127."""
    try:
        p = subprocess.run(argv, capture_output=True, timeout=timeout, env=SUBPROCESS_ENV,
                           stdin=subprocess.DEVNULL)
        return p.returncode, p.stdout.decode("utf-8", "replace"), p.stderr.decode("utf-8", "replace")
    except subprocess.TimeoutExpired:
        return 124, "", "Timeout (runner)"
    except OSError:
        return 127, "", "could not run"


def real_uptime():
    with open("/proc/uptime") as f:
        return float(f.read().split()[0])


def otc_uid():
    import pwd
    return pwd.getpwnam("otc").pw_uid


class Ctx:
    """What the runner touches outside itself: the selftest stands in for
    every part."""

    def __init__(self, paths=None):
        self.paths = paths or Paths()
        self.run = real_run
        self.uptime = real_uptime
        self.otc_uid = otc_uid
        self.root_uid = 0
        self.sleep = time.sleep
        self.clock = time.monotonic
        self.sync = os.sync
        self.which = shutil.which


class Fail(Exception):
    """A change that ends with code."""

    def __init__(self, code):
        super().__init__(code)
        self.code = code


class BadRequest(Exception):
    """A request that can't be read; rid is its id when that much was."""

    def __init__(self, rid=""):
        super().__init__("bad request")
        self.rid = rid


# ---------------------------------------------------------------- rules

def no_control(s):
    return all(ord(c) >= 0x20 and ord(c) != 0x7F for c in s)


def utf8_len(s):
    """The UTF-8 length of s, or None for a string that has none (a lone
    surrogate from JSON's \\ud800)."""
    try:
        return len(s.encode("utf-8"))
    except UnicodeEncodeError:
        return None


def valid_ssid(s):
    n = utf8_len(s) if isinstance(s, str) else None
    return n is not None and 1 <= n <= MAX_SSID and no_control(s)


def valid_psk(s):
    """Empty (an open network), 8-63 bytes of UTF-8 without control
    characters, or exactly 64 hexadecimal digits - the device's rule."""
    if not isinstance(s, str):
        return False
    n = utf8_len(s)
    if n is None:
        return False
    if n == 0:
        return True
    if n == 64:
        return all(c in "0123456789abcdefABCDEF" for c in s)
    return 8 <= n <= 63 and no_control(s)


# ---------------------------------------------------------------- request

class Request:
    def __init__(self, rid, ssid, psk, hidden):
        self.id, self.ssid, self.psk, self.hidden = rid, ssid, psk, hidden


def _no_duplicates(pairs):
    out = {}
    for k, v in pairs:
        if k in out:
            raise ValueError("duplicate key")
        out[k] = v
    return out


def _no_constant(_name):
    raise ValueError("not JSON")


def parse_request(data):
    try:
        obj = json.loads(data.decode("utf-8"), object_pairs_hook=_no_duplicates, parse_constant=_no_constant)
    except (UnicodeDecodeError, ValueError, RecursionError):
        raise BadRequest()
    if not isinstance(obj, dict):
        raise BadRequest()
    rid = obj.get("id")
    rid = rid if isinstance(rid, str) and ID_RE.fullmatch(rid) else ""
    v = obj.get("v")
    if set(obj) != REQUEST_KEYS or type(v) is not int or v != 1 or not rid:
        raise BadRequest(rid)
    ssid, psk, hidden = obj["ssid"], obj["psk"], obj["hidden"]
    if not valid_ssid(ssid) or not valid_psk(psk) or type(hidden) is not bool:
        raise BadRequest(rid)
    return Request(rid, ssid, psk, hidden)


def discard(path):
    """Removes what was taken: a file, or a directory renamed in."""
    try:
        st = os.lstat(path)
    except FileNotFoundError:
        return
    if stat.S_ISDIR(st.st_mode):
        shutil.rmtree(path, ignore_errors=True)
    else:
        try:
            os.unlink(path)
        except OSError:
            pass


def read_taken(path, uid, sleep):
    """The request taken to path: a regular file owned by uid with one
    link and at most MAX_REQUEST bytes, read without following a link and
    without blocking on a FIFO."""
    try:
        st = os.lstat(path)
    except OSError:
        raise BadRequest()
    if not stat.S_ISREG(st.st_mode):
        raise BadRequest()
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    except OSError:
        raise BadRequest()
    try:
        st = os.fstat(fd)
        # The device links its temporary file into place and removes the
        # temporary name right after: a moment's second link is that.
        for _ in range(10):
            if st.st_nlink <= 1:
                break
            sleep(0.1)
            st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != uid or st.st_nlink != 1 or st.st_size > MAX_REQUEST:
            raise BadRequest()
        data = b""
        while len(data) <= MAX_REQUEST:
            chunk = os.read(fd, MAX_REQUEST + 1 - len(data))
            if not chunk:
                break
            data += chunk
        if len(data) > MAX_REQUEST:
            raise BadRequest()
    finally:
        os.close(fd)
    return parse_request(data)


def take_request(ctx):
    """The request, or None when there is none (a spurious trigger).
    Renamed into root's directory first: both are on the /run tmpfs, so
    the rename is atomic and moves a symlink rather than following it,
    and the otc user can no longer touch the name."""
    taken = os.path.join(ctx.paths.state_dir, "req.%d" % os.getpid())
    discard(taken)
    try:
        os.rename(ctx.paths.request, taken)
    except FileNotFoundError:
        return None
    except OSError:
        # Never left there: the path unit would start this again and
        # again.
        discard(ctx.paths.request)
        raise BadRequest()
    try:
        return read_taken(taken, ctx.otc_uid(), ctx.sleep)
    finally:
        discard(taken)


# ---------------------------------------------------------------- files

def prepare_state_dir(ctx):
    d = ctx.paths.state_dir
    try:
        os.mkdir(d, 0o755)
    except FileExistsError:
        pass
    st = os.lstat(d)
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != ctx.root_uid:
        raise SystemExit("state directory is not root's own directory")
    # The device reads its answer as the otc user: never mind the umask.
    os.chmod(d, 0o755)


def write_atomic(ctx, path, text, mode):
    fd, tmp = tempfile.mkstemp(dir=ctx.paths.state_dir, prefix=".tmp.")
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def write_status(ctx, rid, state, code, ssid="", left=False, restored=False):
    if code not in CODES:
        raise ValueError("unknown code")
    rec = {"v": 1, "id": rid, "state": state, "code": code, "ssid": ssid,
           "left": bool(left), "restored": bool(left and restored), "uptime": round(ctx.uptime(), 2)}
    write_atomic(ctx, ctx.paths.status, json.dumps(rec) + "\n", 0o644)


def read_json(path):
    try:
        with open(path, "rb") as f:
            obj = json.loads(f.read(65536).decode("utf-8"))
        return obj if isinstance(obj, dict) else None
    except (OSError, ValueError):
        return None


def write_state(ctx, state):
    write_atomic(ctx, ctx.paths.state, json.dumps(state) + "\n", 0o600)


def clear_state(ctx):
    try:
        os.unlink(ctx.paths.state)
    except FileNotFoundError:
        pass


def too_many(ctx):
    """True when MAX_RUNS changes ran in the last RUNS_WINDOW seconds of
    uptime; otherwise this one is recorded."""
    now = ctx.uptime()
    runs = []
    try:
        with open(ctx.paths.history) as f:
            for line in f.read(65536).split():
                try:
                    t = float(line)
                except ValueError:
                    continue
                if 0 <= now - t < RUNS_WINDOW:
                    runs.append(t)
    except OSError:
        pass
    if len(runs) >= MAX_RUNS:
        return True
    runs.append(now)
    write_atomic(ctx, ctx.paths.history, "".join("%.2f\n" % t for t in runs), 0o600)
    return False


def update_running(ctx):
    """An update asked for or running: its request still waiting for the
    update runner, the runner's unit active (downloading, before
    update.sh), or update.sh holding /run/otc-update.lock (shared) - an
    exclusive lock that can't be had means it runs. Released at once:
    update.sh waits only 10 s for its own."""
    if os.path.lexists(ctx.paths.update_request):
        return True
    rc, out, _ = ctx.run(["systemctl", "is-active", "otc-update.service"], 10)
    if out.strip() in ("active", "activating", "reloading"):
        return True
    try:
        fd = os.open(ctx.paths.update_lock, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except FileNotFoundError:
        return False
    except OSError:
        return True
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        return True
    else:
        fcntl.flock(fd, fcntl.LOCK_UN)
        return False
    finally:
        os.close(fd)


def take_wifi_lock(ctx):
    """The lock both Wi-Fi runners take (the restart runner without
    waiting), held until this process ends: its fd, or None after
    LOCK_WAIT seconds."""
    fd = os.open(ctx.paths.wifi_lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    deadline = ctx.clock() + LOCK_WAIT
    while True:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except OSError:
            if ctx.clock() >= deadline:
                os.close(fd)
                return None
            ctx.sleep(0.5)


# ---------------------------------------------------------------- nmcli output

def split_terse(line):
    """One line of nmcli's terse output: ':' inside a field is '\\:',
    '\\' is '\\\\'."""
    fields, cur, i = [], [], 0
    while i < len(line):
        c = line[i]
        if c == "\\" and i + 1 < len(line):
            cur.append(line[i + 1])
            i += 2
            continue
        if c == ":":
            fields.append("".join(cur))
            cur = []
        else:
            cur.append(c)
        i += 1
    fields.append("".join(cur))
    return fields


def terse_rows(out, n):
    rows = []
    for line in out.splitlines():
        if not line:
            continue
        f = split_terse(line)
        if len(f) >= n:
            rows.append(f)
    return rows


def unescape_value(v):
    """A -g value: '\\:' is ':' and '\\\\' is '\\', as in a terse line
    (an unescaped ':' is kept as it is)."""
    out, i, v = [], 0, v.rstrip("\r\n")
    while i < len(v):
        if v[i] == "\\" and i + 1 < len(v):
            out.append(v[i + 1])
            i += 2
        else:
            out.append(v[i])
            i += 1
    return "".join(out)


def choose_iface(rows):
    """The Wi-Fi client interface: never the setup hotspot's uap* nor
    p2p*; the connected one first, then wlan0, then the first."""
    cands = [r for r in rows if r[1] == "wifi" and IFACE_RE.fullmatch(r[0])
             and not r[0].startswith(("uap", "p2p"))]
    for r in cands:
        if r[2].startswith("connected"):
            return r[0]
    for r in cands:
        if r[0] == "wlan0":
            return r[0]
    return cands[0][0] if cands else None


def ap_key_mgmt(security):
    """What joins one access point the scan shows with security: "open",
    "wpa-psk" or "sae"; None for one this doesn't join (802.1X, WEP, OWE,
    anything unknown)."""
    s = security.strip().upper()
    if s in ("", "--"):
        return "open"
    if any(w in s for w in UNSUPPORTED):
        return None
    if "WPA1" in s or "WPA2" in s:
        return "wpa-psk"
    if "WPA3" in s:
        return "sae"
    return None


def key_mgmt_for(securities, have_psk):
    """How to join a network from the security of every access point the
    scan shows with its name - only the ones this can join count, so a WEP
    or 802.1X copy can't block the real network. When any of them is
    secured the network is: never joined open, whatever access point is
    loudest - an open copy of its name (anyone can broadcast one) never
    takes the device over and the owner's password is never dropped. Fail
    for no password on a secured network, a password for an open one, or
    nothing this joins."""
    kinds = [k for k in (ap_key_mgmt(s) for s in securities) if k]
    if not kinds:
        raise Fail("unsupported_security")
    if "wpa-psk" in kinds or "sae" in kinds:
        if not have_psk:
            raise Fail("password_required")
        return "wpa-psk" if "wpa-psk" in kinds else "sae"
    if have_psk:
        raise Fail("open_network")
    return "open"


def classify(stderr):
    """Why `nmcli connection up` failed, from its words (LC_ALL=C)."""
    e = stderr.lower()
    if any(w in e for w in ("secrets were required", "no-secrets", "passwords or encryption keys are required")):
        return "wrong_password"
    if any(w in e for w in ("supplicant disconnected", "took too long to authenticate",
                             "supplicant-disconnect", "supplicant-timeout")):
        return "auth_failed"
    if any(w in e for w in ("could not be found", "ssid-not-found", "no network with ssid")):
        return "not_found"
    if any(w in e for w in ("ip configuration could not be reserved", "ip-config-unavailable")):
        return "no_address"
    if "timeout" in e or "timed out" in e:
        return "timed_out"
    return "join_failed"


# ---------------------------------------------------------------- keyfile

def keyfile_escape(s):
    """A GKeyFile string value: '\\' doubled and every space '\\s' (a
    leading one would be dropped otherwise). Control characters can't be
    there (valid_psk)."""
    return s.replace("\\", "\\\\").replace(" ", "\\s")


def ssid_bytes_list(ssid):
    """The name as NetworkManager's byte list: no escaping to get wrong."""
    return "".join("%d;" % b for b in ssid.encode("utf-8"))


def keyfile_text(uuid, iface, ssid, key_mgmt, psk, hidden, ip=None):
    lines = [
        "[connection]",
        "id=" + TRIAL_NAME,
        "uuid=" + uuid,
        "type=wifi",
        "interface-name=" + iface,
        "autoconnect=false",
        "autoconnect-priority=%d" % PRIORITY,
        "",
        "[wifi]",
        "mode=infrastructure",
        "ssid=" + ssid_bytes_list(ssid),
        # The same address on the network as before, whatever
        # NetworkManager's global MAC setting: reservations hold.
        "cloned-mac-address=permanent",
    ]
    if hidden:
        lines.append("hidden=true")
    if key_mgmt != "open":
        lines += ["", "[wifi-security]", "key-mgmt=" + key_mgmt, "psk=" + keyfile_escape(psk)]
    ip = ip or {}
    for fam in ("ipv4", "ipv6"):
        sec = list(ip.get(fam) or ["method=auto"])
        if fam == "ipv6" and not any(x.startswith("addr-gen-mode=") for x in sec):
            # A keyfile without it means EUI-64 (addresses made from the
            # MAC), not what NetworkManager gives a profile it creates.
            sec.append("addr-gen-mode=stable-privacy")
        lines += ["", "[%s]" % fam] + sec
    return "\n".join(lines) + "\n"


IP_METHODS = {"ipv4": {"auto", "manual"}, "ipv6": {"auto", "manual", "dhcp", "ignore", "disabled"}}


def split_list(v):
    return [x.strip() for x in re.split(r"[,|]", v) if x.strip() and x.strip() != "--"]


def ip_section(fam, method, addresses, gateway, dns):
    """A copied [ipv4]/[ipv6] section, validated value by value; None if
    anything doesn't read as it should."""
    version = 4 if fam == "ipv4" else 6
    if method not in IP_METHODS[fam]:
        return None
    lines = ["method=" + method]
    try:
        gw = gateway.strip()
        if gw in ("", "--"):
            gw = ""
        else:
            if ipaddress.ip_address(gw).version != version:
                return None
        addrs = split_list(addresses)
        for i, a in enumerate(addrs, 1):
            if ipaddress.ip_interface(a).version != version:
                return None
            lines.append("address%d=%s%s" % (i, a, "," + gw if (i == 1 and gw) else ""))
        if method == "manual" and not addrs:
            return None
        servers = split_list(dns)
        for s in servers:
            if ipaddress.ip_address(s).version != version:
                return None
        if servers:
            lines.append("dns=" + "".join(s + ";" for s in servers))
    except ValueError:
        return None
    return lines


# ---------------------------------------------------------------- the change

def nm(ctx, *args, timeout=15):
    return ctx.run(["nmcli"] + list(args), timeout)


def connections(ctx):
    """Every profile NetworkManager has: [uuid, name, type, filename]."""
    rc, out, _ = nm(ctx, "-t", "-f", "UUID,NAME,TYPE,FILENAME", "connection", "show")
    if rc != 0:
        return []
    return [r for r in terse_rows(out, 4) if UUID_RE.fullmatch(r[0])]


def active_rows(ctx):
    """The active connections: [uuid, device, state] (state activated,
    activating, ...)."""
    rc, out, _ = nm(ctx, "-t", "-f", "UUID,DEVICE,STATE", "connection", "show", "--active")
    if rc != 0:
        return []
    return [r for r in terse_rows(out, 3) if UUID_RE.fullmatch(r[0])]


def profile_ssid(ctx, uuid):
    """A Wi-Fi profile's network name, or None."""
    rc, out, _ = nm(ctx, "-t", "-g", "802-11-wireless.ssid", "connection", "show", "uuid", uuid)
    return unescape_value(out) if rc == 0 else None


def iface_connected(ctx, iface):
    rc, out, _ = nm(ctx, "-t", "-g", "GENERAL.STATE", "device", "show", iface)
    return rc == 0 and out.strip().startswith("100")


def volatile(ctx, filename):
    """A profile file in RAM: not saved."""
    return filename.startswith("/run/") or filename.startswith(ctx.paths.nm_run_dir + "/")


def saved_filename(ctx, uuid):
    """Where NetworkManager keeps uuid's profile ("" if it has none)."""
    for r in connections(ctx):
        if r[0] == uuid:
            return r[3]
    return ""


def retire_old(ctx, keep):
    """After a commit (and --stopped finishing one): our other profiles go
    (the image's, pinned to 2.4 GHz, too - and with them the old password),
    so two "OTC-WiFi" profiles never coexist; someone else's for the same
    network stay but no longer autoconnect with their old password."""
    ssid = profile_ssid(ctx, keep)
    for r in connections(ctx):
        uid, name, typ = r[0], r[1], r[2]
        if uid == keep:
            continue
        if name in (OUR_NAME, TRIAL_NAME):
            nm(ctx, "connection", "delete", "uuid", uid)
            continue
        if not ssid or typ not in WIFI_TYPES:
            continue
        if profile_ssid(ctx, uid) == ssid:
            nm(ctx, "connection", "modify", "uuid", uid, "connection.autoconnect", "no")
            log("another profile for the same network no longer connects by itself")


def ensure_nm_run_dir(ctx):
    """NetworkManager's volatile profile directory, created 0755 if
    missing (whatever the umask) - only where NetworkManager has its own
    /run directory."""
    d = ctx.paths.nm_run_dir
    if not os.path.isdir(ctx.paths.nm_dir):
        return False
    try:
        os.mkdir(d, 0o755)
        os.chmod(d, 0o755)
    except FileExistsError:
        pass
    st = os.lstat(d)
    return stat.S_ISDIR(st.st_mode) and st.st_uid == ctx.root_uid


def housekeeping(ctx):
    """Trial profiles a run cut short left in NetworkManager's volatile
    directory: they hold a password, and NetworkManager loads them again
    if it restarts. A profile is deleted only while it is still that
    unsaved trial (its file this one, its name the trial's, not active);
    anything else only loses the stray file - a saved profile is never
    deleted from here."""
    d = ctx.paths.nm_run_dir
    if not os.path.isdir(d):
        return
    names = [n for n in os.listdir(d) if TRIAL_FILE_RE.fullmatch(n)]
    if not names:
        return
    profiles = connections(ctx)
    active = {r[0] for r in active_rows(ctx)}
    for name in names:
        path = os.path.join(d, name)
        for uid, pname, _, filename in profiles:
            if filename == path and pname == TRIAL_NAME and uid not in active:
                nm(ctx, "connection", "delete", "uuid", uid)
        if os.path.lexists(path):
            try:
                os.unlink(path)
            except OSError:
                pass
            nm(ctx, "connection", "reload")
        log("removed a trial profile a previous run left")


def take_or_wait_lock(ctx):
    """take_wifi_lock, but never fails: --stopped restores the network
    even when it can't have the lock."""
    try:
        return take_wifi_lock(ctx)
    except OSError:
        return None


class Join:
    def __init__(self, ctx, req):
        self.ctx, self.req = ctx, req
        self.iface = self.prev = self.trial = self.trial_path = None
        self.left = False

    def nm(self, *args, timeout=15):
        return nm(self.ctx, *args, timeout=timeout)

    def status(self, state, code, left=False, restored=False):
        write_status(self.ctx, self.req.id, state, code, self.req.ssid, left, restored)

    def beat(self):
        """"joining" again with the uptime now: the device takes a change
        whose word is too old for one cut off (network.cStaleJoining)."""
        self.status("joining", "joining")

    def save_state(self):
        write_state(self.ctx, {"id": self.req.id, "iface": self.iface, "prev_uuid": self.prev,
                               "trial_uuid": self.trial, "trial_path": self.trial_path})

    def run(self):
        """The change, start to end. A Fail ends it here; anything else
        (a crash) leaves state.json for --stopped."""
        ctx = self.ctx
        self.status("joining", "joining")
        lock = None
        try:
            if too_many(ctx):
                raise Fail("too_many")
            if update_running(ctx):
                raise Fail("update_running")
            lock = take_wifi_lock(ctx)
            if lock is None:
                raise Fail("busy")
            # A run cut short whose --stopped never ran (systemctl kill
            # kills the whole unit, ExecStopPost with it - seen with a real
            # systemd): what it left is put right first, so this change
            # starts from the network the device really has.
            leftover = read_json(ctx.paths.state)
            if leftover:
                log("a change cut short earlier is put right first")
                put_right(ctx, leftover)
            self.beat()
            self.change()
            clear_state(ctx)
        except Fail as f:
            self.fail(f.code)
        if lock is not None:
            os.close(lock)

    def fail(self, code):
        """Ends the change with code: before the switch nothing was
        touched; after it the trial goes and the previous connection comes
        back."""
        restored = False
        if self.trial:
            self.drop_trial()
        if self.left:
            rc, _, _ = self.nm("--wait", "40", "connection", "up", "uuid", self.prev, "ifname", self.iface, timeout=60)
            restored = rc == 0
        log("failed: %s%s" % (code, (", previous network restored" if restored else ", previous network NOT restored") if self.left else ""))
        self.status("failed", code, self.left, restored)
        clear_state(self.ctx)

    def drop_trial(self):
        self.nm("connection", "delete", "uuid", self.trial)
        if self.trial_path and os.path.lexists(self.trial_path):
            try:
                os.unlink(self.trial_path)
            except OSError:
                pass
            self.nm("connection", "reload")
        self.trial = None

    def active_uuid(self):
        """The connection working on the interface now: activated, the
        device connected. One still trying (activating - NetworkManager
        retrying an old password, say) is no network to go back to."""
        for r in active_rows(self.ctx):
            if r[1] == self.iface and r[2] == "activated":
                return r[0] if iface_connected(self.ctx, self.iface) else None
        return None

    def scan(self):
        """[(ssid-hex, signal, security, active)]; the rescan may be
        refused (NetworkManager throttles them), then the list it has."""
        args = ["-t", "-f", "SSID-HEX,SIGNAL,SECURITY,ACTIVE", "device", "wifi", "list", "ifname", self.iface, "--rescan"]
        rc, out, _ = self.nm(*args, "yes", timeout=30)
        if rc != 0:
            rc, out, _ = self.nm(*args, "no", timeout=15)
        if rc != 0:
            raise Fail("join_failed")
        rows = []
        for r in terse_rows(out, 4):
            try:
                signal = int(r[1])
            except ValueError:
                signal = 0
            rows.append((r[0].strip().upper(), signal, r[2], r[3] == "yes"))
        return rows

    def addressing_from(self, same_network):
        """The profile whose fixed address and DNS the new one keeps: the
        previous connection when it is on the same network, else one of
        ours for that network that isn't active (the router's password
        changed and the device came back over a cable). None for none."""
        if self.prev and same_network:
            return self.prev
        for r in connections(self.ctx):
            if r[1] == OUR_NAME and r[2] in WIFI_TYPES and profile_ssid(self.ctx, r[0]) == self.req.ssid:
                return r[0]
        return None

    def ip_settings(self, uuid):
        """uuid's addressing as [ipv4]/[ipv6] sections, or None to leave
        them automatic."""
        ip = {}
        for fam in ("ipv4", "ipv6"):
            vals = []
            for prop in ("method", "addresses", "gateway", "dns"):
                rc, out, _ = self.nm("-t", "-g", "%s.%s" % (fam, prop), "connection", "show", "uuid", uuid)
                if rc != 0:
                    return None
                vals.append(unescape_value(out))
            vals[0] = vals[0] or "auto"
            sec = ip_section(fam, *vals)
            if sec is None:
                log("the previous connection's %s settings weren't copied" % fam)
                return None
            if fam == "ipv6":
                rc, out, _ = self.nm("-t", "-g", "ipv6.addr-gen-mode", "connection", "show", "uuid", uuid)
                mode = unescape_value(out).strip() if rc == 0 else ""
                if mode in ADDR_GEN_MODES:
                    sec.append("addr-gen-mode=" + mode)
            ip[fam] = sec
        return ip

    def change(self):
        ctx, req = self.ctx, self.req
        rc, out, _ = self.nm("-t", "-g", "RUNNING", "general", timeout=10)
        if rc != 0 or out.strip() != "running":
            raise Fail("no_wifi")
        housekeeping(ctx)
        rc, out, _ = self.nm("-t", "-f", "DEVICE,TYPE,STATE", "device", timeout=10)
        self.iface = choose_iface(terse_rows(out, 3)) if rc == 0 else None
        if not self.iface:
            raise Fail("no_wifi")
        if ctx.which("rfkill"):
            ctx.run(["rfkill", "unblock", "wifi"], 10)
        self.nm("radio", "wifi", "on", timeout=10)

        self.prev = self.active_uuid()
        self.save_state()

        # Looked for before anything changes. NetworkManager answers a
        # rescan asked too soon after the last one with the list it has,
        # silently: a network not in it is looked for again, a few times.
        target = req.ssid.encode("utf-8").hex().upper()
        give_up = ctx.clock() + SCAN_RETRY_FOR
        rows = self.scan()
        for _ in range(SCAN_RETRIES):
            if any(r[0] == target for r in rows) or ctx.clock() >= give_up:
                break
            ctx.sleep(SCAN_RETRY_WAIT)
            rows = self.scan()
        self.beat()
        seen = [r for r in rows if r[0] == target]
        same_network = any(r[3] for r in seen)
        if seen:
            key_mgmt = key_mgmt_for([r[2] for r in seen], bool(req.psk))
        elif req.hidden:
            key_mgmt = "wpa-psk" if req.psk else "open"
        else:
            raise Fail("not_found")
        psk = req.psk if key_mgmt != "open" else ""
        src = self.addressing_from(same_network)
        ip = self.ip_settings(src) if src else None
        log("joining (ssid %d bytes, %s%s%s)" % (len(req.ssid.encode("utf-8")), key_mgmt,
                                                 ", hidden" if req.hidden else "",
                                                 ", same network, its addressing kept" if ip else ""))

        # The trial profile, in RAM.
        if not ensure_nm_run_dir(ctx):
            log("NetworkManager has no volatile profile directory")
            raise Fail("join_failed")
        u = str(uuidlib.uuid4())
        path = os.path.join(ctx.paths.nm_run_dir, "otc-wifi-%s.nmconnection" % u[:8])
        tmp = path + ".tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                f.write(keyfile_text(u, self.iface, req.ssid, key_mgmt, psk, req.hidden, ip))
            os.rename(tmp, path)
        except BaseException:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise
        self.trial, self.trial_path = u, path
        self.save_state()
        rc, _, _ = self.nm("connection", "load", path)
        if rc != 0:
            raise Fail("join_failed")

        # The switch.
        self.beat()
        self.left = bool(self.prev)
        rc, _, err = self.nm("--wait", str(JOIN_WAIT), "connection", "up", "uuid", u, "ifname", self.iface,
                             timeout=JOIN_WAIT + 15)
        if rc != 0:
            code = classify(err) if rc != 124 else "timed_out"
            if code == "not_found" and seen:
                # NetworkManager (1.52, seen with a real one) says the
                # network "could not be found" when a network the scan just
                # showed turns the device away: a wrong WPA3 (SAE) password,
                # an access point refusing the station or not answering it.
                # Not a missing network: the password or the signal.
                code = "auth_failed"
            raise Fail(code)
        why = self.wait_up()
        if why:
            raise Fail(why)
        self.beat()
        if not self.reachable():
            raise Fail("no_internet")
        self.beat()

        # Kept: saved, checked, on disk - then committed.
        rc, _, _ = self.nm("connection", "modify", "uuid", u, "connection.id", OUR_NAME, "connection.autoconnect", "yes")
        if rc != 0 or not self.saved(u):
            log("the new profile could not be saved")
            raise Fail("join_failed")
        ctx.sync()
        self.trial = None
        write_state(ctx, {"id": req.id, "iface": self.iface, "committed": u})
        self.left = False
        retire_old(ctx, u)
        ctx.sync()
        log("connected; the new network is saved")
        self.status("connected", "connected")

    def wait_up(self):
        """"" once the interface is on the trial with an address (15 s at
        most), else why not."""
        deadline = self.ctx.clock() + 15
        on_trial = False
        while True:
            on_trial = self.active_uuid() == self.trial
            if on_trial and self.has_address():
                return ""
            if self.ctx.clock() >= deadline:
                return "no_address" if on_trial else "join_failed"
            self.ctx.sleep(1)

    def has_address(self):
        rc, out, _ = self.nm("-t", "-g", "IP4.ADDRESS", "device", "show", self.iface)
        if rc == 0 and split_list(unescape_value(out)):
            return True
        rc, out, _ = self.nm("-t", "-g", "IP6.ADDRESS", "device", "show", self.iface)
        if rc != 0:
            return False
        return any(not a.lower().startswith("fe80") for a in split_list(unescape_value(out)))

    def reachable(self):
        """The internet through the new interface: the bridge when the
        device has one (any HTTP answer), else its gateway."""
        host = bridge_host(self.ctx)
        if host and self.ctx.which("curl"):
            for attempt in range(2):
                rc, _, _ = self.ctx.run(["curl", "--interface", self.iface, "-sS", "-o", "/dev/null",
                                         "--connect-timeout", "10", "--max-time", "15",
                                         "https://%s/" % host], 25)
                if rc == 0:
                    return True
                if attempt == 0:
                    self.ctx.sleep(3)
            log("the bridge can't be reached through the new network")
            return False
        gw = ""
        for prop in ("IP4.GATEWAY", "IP6.GATEWAY"):
            rc, out, _ = self.nm("-t", "-g", prop, "device", "show", self.iface)
            v = unescape_value(out) if rc == 0 else ""
            try:
                ipaddress.ip_address(v)
                gw = v
                break
            except ValueError:
                continue
        if not gw:
            log("the new network has no gateway")
            return False
        if not self.ctx.which("ping"):
            return True
        # Twice, as the bridge: the first packets right after joining can
        # be lost (seen with a real NetworkManager).
        for attempt in range(2):
            rc, _, _ = self.ctx.run(["ping", "-I", self.iface, "-c", "3", "-W", "3", gw], 15)
            if rc == 0:
                return True
            if attempt == 0:
                self.ctx.sleep(3)
        log("the new network's gateway doesn't answer")
        return False

    def saved(self, u):
        filename = saved_filename(self.ctx, u)
        return bool(filename) and not volatile(self.ctx, filename)


def bridge_host(ctx):
    """[otc] bridge-addr from the device's root-owned config
    (/etc/otc_<env>.ini, <env> from otc.service's ExecStart), or None."""
    rc, out, _ = ctx.run(["systemctl", "show", "-p", "ExecStart", "--value", "otc.service"], 10)
    env = "dev"
    m = re.search(r"argv\[\]=/usr/bin/otc ([^ ;]+)", out if rc == 0 else "")
    if m and ENV_RE.fullmatch(m.group(1)):
        env = m.group(1)
    path = os.path.join(ctx.paths.etc, "otc_%s.ini" % env)
    try:
        st = os.lstat(path)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != ctx.root_uid or st.st_mode & 0o022:
            return None
        with open(path, encoding="utf-8", errors="replace") as f:
            text = f.read(262144)
    except OSError:
        return None
    section = ""
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("["):
            section = s.replace(" ", "")
            continue
        if section == "[otc]" and "=" in s:
            k, v = s.split("=", 1)
            if k.strip() == "bridge-addr":
                v = v.strip()
                return v if HOST_RE.fullmatch(v) else None
    return None


# ---------------------------------------------------------------- entry points

def sweep(ctx):
    """What a run killed outright (SIGKILL) left in root's directory: a
    taken request (req.<pid>, the password in the clear) and temporary
    files."""
    for name in os.listdir(ctx.paths.state_dir):
        if name.startswith(("req.", ".tmp.")):
            discard(os.path.join(ctx.paths.state_dir, name))


def main(ctx):
    os.umask(0o077)
    prepare_state_dir(ctx)
    sweep(ctx)
    try:
        req = take_request(ctx)
    except BadRequest as e:
        log("refused: the request couldn't be read")
        # Counted like any run: a flood of bad requests is bounded too.
        too_many(ctx)
        write_status(ctx, e.rid, "failed", "bad_request")
        return 0
    if req is None:
        log("no request: nothing to do")
        return 0
    Join(ctx, req).run()
    return 0


def put_right(ctx, state):
    """What a run cut short left (its state.json), put right: a change
    committed, or whose profile is already saved (cut short between saving
    it and recording the commit), stands and is finished - the old profiles
    retired, all flushed; otherwise its trial is dropped (only one whose
    file is still in the volatile directory) and the previous connection
    brought back when the interface isn't connected. state.json is cleared.
    (connected, left, restored) for the answer."""

    def uuid_of(key):
        v = state.get(key)
        return v if isinstance(v, str) and UUID_RE.fullmatch(v) else None

    committed = uuid_of("committed")
    trial = uuid_of("trial_uuid")
    filename = saved_filename(ctx, trial) if trial else ""
    if not committed and filename and not volatile(ctx, filename):
        committed = trial
    if committed:
        log("cut short after the new network was saved: it stays")
        retire_old(ctx, committed)
        ctx.sync()
        clear_state(ctx)
        return True, False, False
    iface = state.get("iface")
    iface = iface if isinstance(iface, str) and IFACE_RE.fullmatch(iface) else None
    prev = uuid_of("prev_uuid")
    trial_path = state.get("trial_path") if isinstance(state.get("trial_path"), str) else None
    left = bool(trial and prev)
    if trial:
        if volatile(ctx, filename) or (not filename and trial_path):
            nm(ctx, "connection", "delete", "uuid", trial)
            if trial_path and os.path.dirname(trial_path) == ctx.paths.nm_run_dir and os.path.lexists(trial_path):
                try:
                    os.unlink(trial_path)
                except OSError:
                    pass
                nm(ctx, "connection", "reload")
    restored = False
    if prev and iface:
        if not iface_connected(ctx, iface):
            nm(ctx, "--wait", "40", "connection", "up", "uuid", prev, "ifname", iface, timeout=60)
        restored = iface_connected(ctx, iface)
    clear_state(ctx)
    return False, left, restored


def stopped(ctx):
    """ExecStopPost, after every run: a run cut short (killed, timed out,
    crashed) is put right (put_right) and answered - "connected" when its
    change stands, else "interrupted". Nothing while the system shuts
    down. (systemctl kill kills this too: the next change puts it right.)"""
    os.umask(0o077)
    rc, out, _ = ctx.run(["systemctl", "is-system-running"], 10)
    if out.strip() == "stopping":
        return 0
    try:
        prepare_state_dir(ctx)
    except (OSError, SystemExit):
        return 0
    state = read_json(ctx.paths.state) or {}
    st = read_json(ctx.paths.status) or {}
    joining = st.get("state") == "joining"
    if not state and not joining:
        return 0
    rid = st.get("id") if isinstance(st.get("id"), str) and ID_RE.fullmatch(st.get("id")) else ""
    ssid = st.get("ssid") if valid_ssid(st.get("ssid")) else ""
    # The Wi-Fi restart runner keeps off while this puts things back.
    lock = take_or_wait_lock(ctx)
    try:
        connected, left, restored = put_right(ctx, state)
        if joining:
            if connected:
                write_status(ctx, rid, "connected", "connected", ssid)
            else:
                log("interrupted%s" % ((", previous network restored" if restored else ", previous network NOT restored") if left else ""))
                write_status(ctx, rid, "failed", "interrupted", ssid, left, restored)
        return 0
    finally:
        if lock is not None:
            os.close(lock)


# ---------------------------------------------------------------- selftest

class FakeNM:
    """NetworkManager as nmcli shows it, enough for the runner's calls."""

    def __init__(self, paths, ssid="home", security="WPA2", psk_ok="right-password", active=True,
                 reachable=True, saves=True, has_curl=True, rescan_refused=False, wired_only=False):
        self.paths = paths
        self.calls = []
        self.iface = "wlan0"
        self.networks = {ssid.encode().hex().upper(): (70, security)}
        # More access points: (ssid-hex, signal, security), beside networks'.
        self.more_aps = []
        # The previous connection still trying (NetworkManager retrying an
        # old password), not working.
        self.prev_activating = False
        self.update_active = False
        self.psk_ok = psk_ok
        self.reachable = reachable
        self.saves = saves
        self.has_curl = has_curl
        # The network seen but turning the device away (a wrong WPA3
        # password, a refused station): NetworkManager's "could not be
        # found".
        self.turned_away = False
        self.rescan_refused = rescan_refused
        self.scans = 0
        # The first scans that don't show the network yet (NetworkManager's
        # throttled rescan answering with its old list).
        self.unseen_scans = 0
        self.fail_on = None
        self.conns = {}
        self.active = None
        if not wired_only:
            u = "11111111-1111-4111-8111-111111111111"
            self.conns[u] = {"name": OUR_NAME, "type": "802-11-wireless", "file": "/etc/NetworkManager/system-connections/OTC-WiFi.nmconnection",
                             "ssid": ssid, "autoconnect": True, "ipv4.method": "auto"}
            if active:
                self.active = u
        self.connected = self.active is not None
        self.bridge = "off-the.cloud"
        self.loaded = []

    def which(self, name):
        if name == "curl" and not self.has_curl:
            return None
        return "/usr/bin/" + name

    def run(self, argv, timeout):
        self.calls.append(list(argv))
        if self.fail_on and self.fail_on(argv):
            raise RuntimeError("cut short")
        a = argv
        if a[0] == "systemctl":
            if a[1] == "show":
                return 0, "{ path=/usr/bin/otc ; argv[]=/usr/bin/otc prod ; ignore_errors=no }\n", ""
            if a[1] == "is-active":
                return (0, "active\n", "") if self.update_active else (3, "inactive\n", "")
            return 0, "running\n", ""
        if a[0] == "rfkill":
            return 0, "", ""
        if a[0] == "curl":
            return (0 if self.reachable else 7), "", ""
        if a[0] == "ping":
            return (0 if self.reachable else 1), "", ""
        assert a[0] == "nmcli"
        a = a[1:]
        if a[:3] == ["-t", "-g", "RUNNING"]:
            return 0, "running\n", ""
        if a[:3] == ["-t", "-f", "DEVICE,TYPE,STATE"]:
            st = "connected" if self.connected else "disconnected"
            return 0, "eth0:ethernet:connected\nwlan0:wifi:%s\np2p-dev-wlan0:wifi-p2p:disconnected\nlo:loopback:connected (externally)\n" % st, ""
        if a[:2] == ["radio", "wifi"]:
            return 0, "", ""
        if a[:4] == ["-t", "-f", "UUID,DEVICE,STATE", "connection"]:
            st = "activating" if self.prev_activating else "activated"
            return 0, ("%s:wlan0:%s\n" % (self.active, st) if self.active else "") + "22222222-2222-4222-8222-222222222222:eth0:activated\n", ""
        if a[:3] == ["-t", "-f", "SSID-HEX,SIGNAL,SECURITY,ACTIVE"]:
            if a[-1] == "yes" and self.rescan_refused:
                return 1, "", "Error: Scanning not allowed immediately following previous scan."
            self.scans += 1
            cur = self.conns.get(self.active, {}).get("ssid") if self.active else None
            cur_hex = cur.encode().hex().upper() if cur else None
            aps = [(h, sig, sec) for h, (sig, sec) in self.networks.items()] + self.more_aps
            lines = ["%s:%d:%s:%s" % (h, sig, sec.replace(":", "\\:"), "yes" if h == cur_hex else "no")
                     for h, sig, sec in aps if self.scans > self.unseen_scans or h == cur_hex]
            lines.append("7765616B:20:WPA2:no")  # "weak"
            return 0, "\n".join(lines) + "\n", ""
        if a[:2] == ["connection", "load"]:
            path = a[2]
            text = open(path, encoding="utf-8").read()
            self.loaded.append(text)
            u = re.search(r"^uuid=(.*)$", text, re.M).group(1)
            ssid = bytes(int(x) for x in re.search(r"^ssid=(.*)$", text, re.M).group(1).split(";") if x).decode()
            m = re.search(r"^psk=(.*)$", text, re.M)
            psk = m.group(1).replace("\\s", " ").replace("\\\\", "\\") if m else ""
            self.conns[u] = {"name": TRIAL_NAME, "type": "802-11-wireless", "file": path, "ssid": ssid,
                             "psk": psk, "autoconnect": False}
            return 0, "", ""
        if a[:2] == ["connection", "reload"]:
            return 0, "", ""
        if a[0] == "--wait" and a[2:4] == ["connection", "up"]:
            u = a[5]
            c = self.conns.get(u)
            if c is None:
                return 10, "", "Error: unknown connection"
            h = c["ssid"].encode().hex().upper()
            self.active, self.connected, self.prev_activating = None, False, False
            secs = [sec for hh, (sig, sec) in self.networks.items() if hh == h] + [sec for hh, sig, sec in self.more_aps if hh == h]
            if not secs:
                return 4, "", "Error: Connection activation failed: The Wi-Fi network could not be found."
            if c["name"] == TRIAL_NAME and self.turned_away:
                return 4, "", "Error: Connection activation failed: The Wi-Fi network could not be found."
            if c["name"] == TRIAL_NAME:
                if c.get("psk"):
                    if not any(x not in ("", "--") for x in secs):
                        return 4, "", "Error: Connection activation failed: The Wi-Fi network could not be found."
                    if c["psk"] != self.psk_ok:
                        return 4, "", "Error: Connection activation failed: (7) Secrets were required, but not provided."
                elif not any(x in ("", "--") for x in secs):
                    return 4, "", "Error: Connection activation failed: (7) Secrets were required, but not provided."
            self.active, self.connected = u, True
            return 0, "", ""
        if a[:2] == ["-t", "-g"] and a[3:5] == ["device", "show"]:
            prop = a[2]
            if prop == "GENERAL.STATE":
                return 0, ("100 (connected)\n" if self.connected else "30 (disconnected)\n"), ""
            if prop == "IP4.ADDRESS":
                return 0, ("192.168.1.20/24\n" if self.connected else ""), ""
            if prop == "IP6.ADDRESS":
                return 0, "fe80\\:\\:1/64\n", ""
            if prop == "IP4.GATEWAY":
                return 0, ("192.168.1.1\n" if self.connected else ""), ""
            return 0, "", ""
        if a[:2] == ["-t", "-g"] and a[3:5] == ["connection", "show"]:
            c = self.conns.get(a[6], {})
            prop = a[2]
            if prop == "802-11-wireless.ssid":
                return 0, c.get("ssid", "") + "\n", ""
            return 0, c.get(prop, "") + "\n", ""
        if a[:2] == ["connection", "modify"]:
            u = a[3]
            c = self.conns[u]
            kv = dict(zip(a[4::2], a[5::2]))
            if "connection.id" in kv:
                c["name"] = kv["connection.id"]
                if self.saves and c["file"].startswith(self.paths.nm_run_dir):
                    os.unlink(c["file"])
                    c["file"] = "/etc/NetworkManager/system-connections/%s.nmconnection" % u
            if "connection.autoconnect" in kv:
                c["autoconnect"] = kv["connection.autoconnect"] == "yes"
            return 0, "", ""
        if a[:3] == ["connection", "delete", "uuid"]:
            c = self.conns.pop(a[3], None)
            if c and c["file"].startswith(self.paths.nm_run_dir) and os.path.exists(c["file"]):
                os.unlink(c["file"])
            if self.active == a[3]:
                self.active, self.connected = None, False
            return (0 if c else 10), "", ""
        if a[:4] in (["-t", "-f", "UUID,NAME,TYPE,FILENAME", "connection"], ["-t", "-f", "UUID,FILENAME", "connection"]):
            rows = []
            for u, c in self.conns.items():
                f = c["file"].replace(":", "\\:")
                rows.append("%s:%s:%s:%s" % (u, c["name"], c["type"], f) if a[2] == "UUID,NAME,TYPE,FILENAME" else "%s:%s" % (u, f))
            return 0, "\n".join(rows) + "\n", ""
        raise AssertionError("unexpected nmcli call: %r" % argv)


def selftest():
    def expect(cond, what):
        if not cond:
            raise AssertionError(what)

    # The rules, byte for byte as the device's (network/join.go).
    for s, ok in [("", False), ("a", True), ("x" * 32, True), ("x" * 33, False), ("é" * 16, True),
                  ("é" * 17, False), ("tab\there", False), ("del\x7f", False), ("\ud800", False),
                  ("café ☕", True), (None, False), (5, False)]:
        expect(valid_ssid(s) == ok, "ssid %r" % (s,))
    for s, ok in [("", True), ("x" * 7, False), ("x" * 8, True), ("x" * 63, True), ("a" * 64, True),
                  ("0123456789abcdefABCDEF0123456789abcdef0123456789abcdef0123456789", True),
                  ("g" * 64, False), ("x" * 65, False), ("ñ" * 31 + "x", True), ("ñ" * 32, False),
                  ("pass\tword", False), ("pass word", True), ("\ud800" * 8, False), (None, False)]:
        expect(valid_psk(s) == ok, "psk %r" % (s,))

    with tempfile.TemporaryDirectory() as tmp:
        paths = Paths(tmp)
        for d in (os.path.dirname(paths.request), paths.etc, paths.nm_dir):
            os.makedirs(d)
        ctx = Ctx(paths)
        ctx.root_uid = os.getuid()
        ctx.otc_uid = lambda: os.getuid()
        ctx.sleep = lambda s: None
        up = [1000.0]
        ctx.uptime = lambda: up[0]
        clock = [0.0]

        def tick(s):
            clock[0] += s
        ctx.clock = lambda: clock[0]
        syncs = []
        ctx.sync = lambda: syncs.append(True)
        old_umask = os.umask(0o077)
        try:
            prepare_state_dir(ctx)
        finally:
            os.umask(old_umask)
        expect(stat.S_IMODE(os.lstat(paths.state_dir).st_mode) == 0o755, "state directory mode")

        good = {"v": 1, "id": "0123456789abcdef", "ssid": "home", "psk": "right-password", "hidden": False}

        def put(content, mode="w"):
            discard(paths.request)
            with open(paths.request, mode) as f:
                f.write(content)

        def take():
            try:
                return take_request(ctx)
            except BadRequest as e:
                return e

        put(json.dumps(good))
        r = take()
        expect(isinstance(r, Request) and r.ssid == "home" and r.psk == "right-password" and r.hidden is False, "good request")
        expect(not os.path.lexists(paths.request) and os.listdir(paths.state_dir) == [], "request taken and removed")
        expect(take() is None, "no request")

        # Every malformed or planted request is refused, its id kept when
        # it read.
        bad = [
            (json.dumps(dict(good, extra=1)), "0123456789abcdef"),
            (json.dumps({k: v for k, v in good.items() if k != "hidden"}), "0123456789abcdef"),
            ('{"v":1,"v":1,"id":"0123456789abcdef","ssid":"home","psk":"","hidden":false}', ""),
            (json.dumps(dict(good, v=True)), "0123456789abcdef"),
            (json.dumps(dict(good, v=2)), "0123456789abcdef"),
            (json.dumps(dict(good, id="0123456789ABCDEF")), ""),
            (json.dumps(dict(good, id="0123456789abcdef\n")), ""),
            (json.dumps(dict(good, ssid="")), "0123456789abcdef"),
            (json.dumps(dict(good, ssid="x" * 33)), "0123456789abcdef"),
            (json.dumps(dict(good, psk="short")), "0123456789abcdef"),
            ('{"v":1,"id":"0123456789abcdef","ssid":"\\ud800","psk":"","hidden":false}', "0123456789abcdef"),
            (json.dumps(dict(good, hidden=1)), "0123456789abcdef"),
            ("{not json", ""),
            ("[1,2]", ""),
            ('{"v":NaN}', ""),
            (json.dumps(dict(good, psk="p" * 1100)), ""),
        ]
        for content, rid in bad:
            put(content)
            r = take()
            expect(isinstance(r, BadRequest) and r.rid == rid, "bad request %r -> %r" % (content[:40], r))
        put(b"\xff\xfe{}", "wb")
        expect(isinstance(take(), BadRequest), "not UTF-8")
        # A symlink, a FIFO, a directory, someone else's file, a hard link.
        target = os.path.join(tmp, "secret")
        with open(target, "w") as f:
            f.write(json.dumps(good))
        discard(paths.request)
        os.symlink(target, paths.request)
        expect(isinstance(take(), BadRequest) and os.path.exists(target), "symlink")
        os.mkfifo(paths.request)
        expect(isinstance(take(), BadRequest), "fifo")
        os.mkdir(paths.request)
        open(os.path.join(paths.request, "x"), "w").close()
        expect(isinstance(take(), BadRequest) and os.listdir(paths.state_dir) == [], "directory")
        put(json.dumps(good))
        ctx.otc_uid = lambda: os.getuid() + 1
        expect(isinstance(take(), BadRequest), "owner")
        ctx.otc_uid = lambda: os.getuid()
        put(json.dumps(good))
        os.link(paths.request, os.path.join(tmp, "second-link"))
        expect(isinstance(take(), BadRequest), "hard link")
        os.unlink(os.path.join(tmp, "second-link"))
        expect(os.path.exists(target) and json.loads(open(target).read()) == good, "nothing followed")

        # The keyfile: the name as bytes, the password escaped.
        kf = keyfile_text("u-u-i-d", "wlan0", "a b;#=[é", "wpa-psk", " p\\ss w;#=[ñ ", True)
        expect("ssid=97;32;98;59;35;61;91;195;169;\n" in kf, "ssid list")
        expect("psk=\\sp\\\\ss\\sw;#=[ñ\\s\n" in kf, "psk escaped: " + kf)
        expect("hidden=true" in kf and "autoconnect=false" in kf and "cloned-mac-address=permanent" in kf, "keyfile flags")
        expect("[ipv4]\nmethod=auto" in kf and "[ipv6]\nmethod=auto" in kf, "keyfile ip")
        kf = keyfile_text("u", "wlan0", "open", "open", "", False)
        expect("[wifi-security]" not in kf and "psk=" not in kf and "hidden" not in kf, "open keyfile")
        sec = ip_section("ipv4", "manual", "192.168.1.10/24, 192.168.1.11/24", "192.168.1.1", "192.168.1.1,8.8.8.8")
        expect(sec == ["method=manual", "address1=192.168.1.10/24,192.168.1.1", "address2=192.168.1.11/24",
                       "dns=192.168.1.1;8.8.8.8;"], "ipv4 copied: %r" % (sec,))
        expect(ip_section("ipv6", "auto", "", "--", "") == ["method=auto"], "ipv6 auto")
        expect(ip_section("ipv4", "manual", "x", "", "") is None and ip_section("ipv4", "shared", "", "", "") is None
               and ip_section("ipv4", "manual", "fd00::1/64", "", "") is None, "bad ip refused")

        # nmcli's words.
        for err, code in [("Error: Connection activation failed: (7) Secrets were required, but not provided.", "wrong_password"),
                          ("Error: Connection activation failed: 802.1X supplicant took too long to authenticate", "auth_failed"),
                          ("Error: Connection activation failed: 802.1X supplicant disconnected", "auth_failed"),
                          ("Error: Connection activation failed: The Wi-Fi network could not be found", "not_found"),
                          ("Error: Connection activation failed: IP configuration could not be reserved (no available address, timeout, etc.).", "no_address"),
                          ("Error: Timeout expired (40 seconds)", "timed_out"),
                          ("Error: something else", "join_failed"), ("", "join_failed")]:
            expect(classify(err) == code, "classify %r" % err)
        def km_of(secs, psk):
            try:
                return key_mgmt_for(secs, psk)
            except Fail as f:
                return f.code
        for secs, psk, km in [
                (["WPA2"], True, "wpa-psk"), (["WPA1 WPA2"], True, "wpa-psk"), (["WPA2 WPA3"], True, "wpa-psk"),
                (["WPA3"], True, "sae"), ([""], False, "open"), (["--"], False, "open"),
                (["WPA2"], False, "password_required"), ([""], True, "open_network"),
                # An open copy of a secured network's name, louder or not:
                # never joined open, the password never dropped.
                (["--", "WPA2"], True, "wpa-psk"), (["", "WPA2"], False, "password_required"),
                (["--", "WPA3"], True, "sae"), (["WPA3", "WPA2"], True, "wpa-psk"),
                # Only the access points this joins count: a WEP or 802.1X
                # copy blocks nothing.
                (["WEP", "WPA2"], True, "wpa-psk"), (["WPA2 802.1X", "--"], False, "open"),
                (["WPA2 802.1X"], True, "unsupported_security"), (["WEP"], False, "unsupported_security"),
                (["OWE"], False, "unsupported_security"), (["WPA3 802.1X"], True, "unsupported_security"),
                (["XYZ"], True, "unsupported_security")]:
            expect(km_of(secs, psk) == km, "security %r, password %s: %r" % (secs, psk, km_of(secs, psk)))
        expect(split_terse("a\\:b:c\\\\d:") == ["a:b", "c\\d", ""], "terse split")
        expect(unescape_value("fd00\\:\\:1/64 | fe80::2/64\n") == "fd00::1/64 | fe80::2/64", "-g value")
        expect(choose_iface([["uap0", "wifi", "connected"], ["p2p-dev-wlan0", "wifi", "x"], ["wlan1", "wifi", "disconnected"],
                             ["wlan0", "wifi", "disconnected"]]) == "wlan0", "interface: wlan0 over the first")
        expect(choose_iface([["wlan0", "wifi", "disconnected"], ["wlan1", "wifi", "connected"]]) == "wlan1", "interface: connected first")
        expect(choose_iface([["eth0", "ethernet", "connected"], ["uap0", "wifi", "connected"]]) is None, "no client interface")

        # The status file: readable by the device, atomic, codes only.
        old_umask = os.umask(0o077)
        try:
            write_status(ctx, "0123456789abcdef", "failed", "wrong_password", "home", True, True)
        finally:
            os.umask(old_umask)
        expect(stat.S_IMODE(os.stat(paths.status).st_mode) == 0o644, "status mode")
        st = json.loads(open(paths.status).read())
        expect(st == {"v": 1, "id": "0123456789abcdef", "state": "failed", "code": "wrong_password", "ssid": "home",
                      "left": True, "restored": True, "uptime": 1000.0}, "status %r" % st)
        write_status(ctx, "", "failed", "bad_request", "", False, True)
        expect(json.loads(open(paths.status).read())["restored"] is False, "restored only with left")
        try:
            write_status(ctx, "", "failed", "no_answer")
            expect(False, "no_answer written")
        except ValueError:
            pass

        # The rate limit: 10 in 15 minutes of uptime.
        for i in range(10):
            up[0] = 2000.0 + i
            expect(not too_many(ctx), "run %d allowed" % i)
        expect(too_many(ctx), "11th refused")
        up[0] = 2000.0 + RUNS_WINDOW + 1
        expect(not too_many(ctx), "allowed again after the window")
        os.unlink(paths.history)
        up[0] = 3000.0

        # The locks.
        expect(not update_running(ctx), "no update lock")
        lk = os.open(paths.update_lock, os.O_RDWR | os.O_CREAT, 0o600)
        expect(not update_running(ctx), "update lock free")
        fcntl.flock(lk, fcntl.LOCK_SH)
        expect(update_running(ctx), "update lock held")
        os.close(lk)
        fd = take_wifi_lock(ctx)
        expect(fd is not None, "wifi lock")
        other = os.open(paths.wifi_lock, os.O_RDWR)
        try:
            fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
            expect(False, "the lock was free while held")
        except OSError:
            pass
        os.close(other)
        os.close(fd)

        with open(os.path.join(paths.etc, "otc_prod.ini"), "w") as f:
            f.write("[otc]\nbridge-addr = off-the.cloud\nbridge-default=x\n[otc-api]\nbridge-addr=evil\n")
        os.chmod(os.path.join(paths.etc, "otc_prod.ini"), 0o644)

        def change(nm, req=None, setup=None):
            for p in (paths.status, paths.state, paths.history, paths.wifi_lock):
                if os.path.exists(p):
                    os.unlink(p)
            ctx.run, ctx.which = nm.run, nm.which
            clock[0] = 0.0
            ctx.sleep = tick
            del syncs[:]
            put(json.dumps(req or good))
            if setup:
                setup()
            try:
                main(ctx)
            except RuntimeError:
                # Killed: ExecStopPost runs --stopped, which nothing cuts.
                nm.fail_on = None
                stopped(ctx)
            return json.loads(open(paths.status).read())

        def nm_calls(nm, *prefix):
            return [c for c in nm.calls if c[1:1 + len(prefix)] == list(prefix)]

        def ups(nm):
            return [c for c in nm.calls if c[0] == "nmcli" and c[3:5] == ["connection", "up"]]

        def nmcli_calls(nm):
            return [c for c in nm.calls if c[0] == "nmcli"]

        # A new password for the same network: joined, checked through
        # the bridge, saved, the old profile gone, synced before and after.
        nm = FakeNM(paths)
        old = nm.active
        st = change(nm)
        expect(st["state"] == "connected" and st["code"] == "connected" and st["id"] == good["id"], "joined: %r" % st)
        expect(old not in nm.conns and len(nm.conns) == 1, "old profile deleted")
        new = list(nm.conns.values())[0]
        expect(new["name"] == OUR_NAME and new["autoconnect"] and new["file"].startswith("/etc/"), "new profile saved")
        expect(os.listdir(paths.nm_run_dir) == [], "no trial left in RAM")
        expect(not os.path.exists(paths.state), "state cleared")
        expect(len(syncs) == 2, "synced twice")
        curl = [c for c in nm.calls if c[0] == "curl"]
        expect(curl and curl[0][:3] == ["curl", "--interface", "wlan0"] and curl[0][-1] == "https://off-the.cloud/", "bridge checked")
        expect(not any("right-password" in " ".join(c) for c in nm.calls), "the password on no command line")
        order = [i for i, c in enumerate(nm.calls) if c[1:3] in (["connection", "modify"], ["connection", "delete"])]
        expect(nm.calls[order[0]][1:3] == ["connection", "modify"], "saved before anything is deleted")
        expect(stat.S_IMODE(os.stat(paths.status).st_mode) == 0o644, "status readable")

        # The wrong password: the trial dropped, the previous network back.
        nm = FakeNM(paths)
        old = nm.active
        st = change(nm, dict(good, psk="wrong-password"))
        expect(st["code"] == "wrong_password" and st["left"] and st["restored"], "wrong password: %r" % st)
        expect(nm.active == old and list(nm.conns) == [old], "back on the previous profile")
        expect(os.listdir(paths.nm_run_dir) == [], "trial removed")

        # Connected but no internet (a captive portal): rolled back.
        nm = FakeNM(paths, reachable=False)
        old = nm.active
        st = change(nm)
        expect(st["code"] == "no_internet" and st["left"] and st["restored"] and nm.active == old and list(nm.conns) == [old],
               "no internet: %r" % st)
        # No bridge configured: the gateway answers for it.
        os.rename(os.path.join(paths.etc, "otc_prod.ini"), os.path.join(paths.etc, "otc_prod.ini.off"))
        nm = FakeNM(paths)
        st = change(nm)
        expect(st["code"] == "connected" and any(c[0] == "ping" for c in nm.calls) and not any(c[0] == "curl" for c in nm.calls),
               "gateway check: %r" % st)
        os.rename(os.path.join(paths.etc, "otc_prod.ini.off"), os.path.join(paths.etc, "otc_prod.ini"))

        # Saving didn't move it out of RAM (netplan, an old NetworkManager):
        # not kept.
        nm = FakeNM(paths, saves=False)
        old = nm.active
        st = change(nm)
        expect(st["code"] == "join_failed" and st["restored"] and list(nm.conns) == [old], "not saved: %r" % st)

        # Refused before anything changes.
        for kw, req, code in [({}, dict(good, ssid="elsewhere"), "not_found"),
                              ({"security": "WPA2 802.1X"}, None, "unsupported_security"),
                              ({}, dict(good, psk=""), "password_required")]:
            nm = FakeNM(paths, **kw)
            old = nm.active
            st = change(nm, req)
            expect(st["code"] == code and not st["left"] and nm.active == old, "%s: %r" % (code, st))
            expect(not ups(nm) and not nm_calls(nm, "connection", "load"), code + " touched nothing")
        # An open network: no password needed, none written; a password
        # given for it is refused, never dropped.
        nm = FakeNM(paths, security="--")
        st = change(nm, dict(good, psk=""))
        expect(st["code"] == "connected" and "[wifi-security]" not in nm.loaded[0], "open network: %r" % st)
        nm = FakeNM(paths, security="--")
        old = nm.active
        st = change(nm, dict(good, psk="a-password-for-it"))
        expect(st["code"] == "open_network" and not st["left"] and nm.active == old and not ups(nm), "open, with a password: %r" % st)
        # Someone broadcasts an open copy of the secured network's name,
        # louder: the password is used, the open copy never joined.
        nm = FakeNM(paths)
        nm.more_aps.append(("home".encode().hex().upper(), 99, "--"))
        st = change(nm)
        expect(st["code"] == "connected" and "key-mgmt=wpa-psk" in nm.loaded[0] and "psk=right-password" in nm.loaded[0],
               "open copy beside the secured one: %r" % st)
        nm = FakeNM(paths)
        nm.more_aps.append(("home".encode().hex().upper(), 99, "--"))
        old = nm.active
        st = change(nm, dict(good, psk=""))
        expect(st["code"] == "password_required" and nm.active == old and not ups(nm), "open copy, no password: %r" % st)
        # A hidden network not in the scan.
        nm = FakeNM(paths, ssid="hidden-net")
        st = change(nm, dict(good, ssid="hidden-net", hidden=True))
        expect(st["code"] == "connected", "hidden: %r" % st)
        # The rescan refused: the cached list.
        nm = FakeNM(paths, rescan_refused=True)
        expect(change(nm)["code"] == "connected", "rescan refused")
        # A network the first scans don't show yet (a throttled rescan
        # answered with the old list): looked for again.
        nm = FakeNM(paths)
        nm.networks["new-name".encode().hex().upper()] = (60, "WPA2")
        nm.unseen_scans = 2
        st = change(nm, dict(good, ssid="new-name"))
        expect(st["code"] == "connected" and nm.scans == 3, "seen on the third scan: %r %d" % (st, nm.scans))
        nm = FakeNM(paths)
        nm.networks["new-name".encode().hex().upper()] = (60, "WPA2")
        nm.unseen_scans = 99
        st = change(nm, dict(good, ssid="new-name"))
        expect(st["code"] == "not_found" and nm.scans == 1 + SCAN_RETRIES, "never seen: %r %d" % (st, nm.scans))
        # A network the scan showed turning the device away: NetworkManager
        # says "could not be found" - the password or the signal, not a
        # missing network. A hidden one not in the scan: not found.
        nm = FakeNM(paths)
        nm.turned_away = True
        old = nm.active
        st = change(nm)
        expect(st["code"] == "auth_failed" and st["left"] and st["restored"] and nm.active == old, "turned away: %r" % st)
        nm = FakeNM(paths)
        nm.turned_away = True
        nm.networks["hidden-net".encode().hex().upper()] = (60, "WPA2")
        nm.unseen_scans = 99
        st = change(nm, dict(good, ssid="hidden-net", hidden=True))
        expect(st["code"] == "not_found" and st["left"] and st["restored"], "hidden, not seen, turned away: %r" % st)
        # A cable-only device: nothing to go back to.
        nm = FakeNM(paths, wired_only=True)
        st = change(nm, dict(good, psk="wrong-password"))
        expect(st["code"] == "wrong_password" and not st["left"] and not st["restored"], "cable only: %r" % st)

        # Another change, an update, a burst.
        nm = FakeNM(paths)

        def hold_wifi():
            fd = os.open(paths.wifi_lock, os.O_RDWR | os.O_CREAT, 0o600)
            fcntl.flock(fd, fcntl.LOCK_EX)
            held.append(fd)
        held = []
        st = change(nm, setup=hold_wifi)
        expect(st["code"] == "busy" and not nmcli_calls(nm), "busy: %r" % st)
        os.close(held.pop())
        lk = os.open(paths.update_lock, os.O_RDWR | os.O_CREAT, 0o600)
        fcntl.flock(lk, fcntl.LOCK_SH)
        st = change(nm)
        expect(st["code"] == "update_running", "update running: %r" % st)
        os.close(lk)
        # An update asked for (its request not yet taken), or its runner
        # downloading before update.sh takes the lock.
        os.makedirs(os.path.dirname(paths.update_request), exist_ok=True)
        open(paths.update_request, "w").close()
        nm = FakeNM(paths)
        st = change(nm)
        expect(st["code"] == "update_running" and not nmcli_calls(nm), "update asked for: %r" % st)
        os.unlink(paths.update_request)
        nm = FakeNM(paths)
        nm.update_active = True
        st = change(nm)
        expect(st["code"] == "update_running" and not nmcli_calls(nm), "update runner active: %r" % st)

        # Same network as before: its fixed address kept; someone else's
        # profile for it stops autoconnecting.
        nm = FakeNM(paths)
        prev = nm.active
        nm.conns[prev].update({"ipv4.method": "manual", "ipv4.addresses": "192.168.1.50/24", "ipv4.gateway": "192.168.1.1",
                               "ipv4.dns": "192.168.1.1", "ipv6.method": "auto"})
        other = "33333333-3333-4333-8333-333333333333"
        nm.conns[other] = {"name": "preconfigured", "type": "802-11-wireless", "file": "/etc/x", "ssid": "home", "autoconnect": True}
        loaded = []
        real = nm.run

        def spy(argv, timeout):
            if argv[1:3] == ["connection", "load"]:
                loaded.append(open(argv[3]).read())
            return real(argv, timeout)
        nm.run = spy
        st = change(nm)
        expect(st["code"] == "connected" and "address1=192.168.1.50/24,192.168.1.1" in loaded[0], "addressing kept")
        expect(nm.conns[other]["autoconnect"] is False and other in nm.conns, "other profile kept, autoconnect off")

        # Cut short after the commit: --stopped leaves the saved network
        # and finishes the commit (the old profile retired, flushed).
        nm = FakeNM(paths)
        seen_state = []

        def at_delete(argv):
            if argv[1:3] == ["connection", "delete"]:
                seen_state.append(read_json(paths.state))
                return True
            return False
        nm.fail_on = at_delete
        st = change(nm)
        expect(st["code"] == "connected" and len(nm.conns) == 1 and nm.active in nm.conns
               and nm.conns[nm.active]["name"] == OUR_NAME and syncs, "committed change stands: %r" % st)
        expect(seen_state and set(seen_state[0]) == {"id", "iface", "committed"}, "commit marker alone: %r" % seen_state)
        # Cut short after the profile was saved, before the commit was
        # recorded: the same - it is saved, so it stays.
        nm = FakeNM(paths)
        modified = []

        def after_save(argv):
            if argv[1:3] == ["connection", "modify"] and "connection.id" in argv:
                modified.append(True)
                return False
            return bool(modified) and argv[1:4] == ["-t", "-f", "UUID,NAME,TYPE,FILENAME"]
        nm.fail_on = after_save
        st = change(nm)
        expect(st["code"] == "connected" and len(nm.conns) == 1 and nm.conns[nm.active]["name"] == OUR_NAME
               and nm.conns[nm.active]["file"].startswith("/etc/"), "saved, not yet committed: %r %r" % (st, nm.conns))
        # Cut short during the trial: interrupted, trial gone, back on the
        # previous network.
        nm = FakeNM(paths)
        old = nm.active
        nm.fail_on = lambda argv: argv[0] == "curl"
        st = change(nm)
        expect(st["code"] == "interrupted" and st["left"] and st["restored"] and nm.active == old and list(nm.conns) == [old]
               and os.listdir(paths.nm_run_dir) == [], "interrupted: %r" % st)
        # Cut short with its --stopped killed too (systemctl kill takes the
        # whole unit): the next change puts it right first - the trial
        # dropped, the previous network back - then goes on from there.
        nm = FakeNM(paths)
        old = nm.active
        nm.fail_on = lambda argv: argv[0] == "curl"
        for p in (paths.status, paths.state, paths.history, paths.wifi_lock):
            if os.path.exists(p):
                os.unlink(p)
        ctx.run, ctx.which, ctx.sleep = nm.run, nm.which, tick
        clock[0] = 0.0
        put(json.dumps(good))
        try:
            main(ctx)
        except RuntimeError:
            pass
        first_trial = (read_json(paths.state) or {}).get("trial_uuid")
        expect(first_trial and nm.active == first_trial and os.listdir(paths.nm_run_dir), "left on the trial, unsaved")
        nm.fail_on = None
        # (The killed run's lock went with it.)
        os.unlink(paths.wifi_lock)
        second = dict(good, id="fedcba9876543210")
        put(json.dumps(second))
        main(ctx)
        st = json.loads(open(paths.status).read())
        expect(st["id"] == second["id"] and st["code"] == "connected" and st["left"] is False, "the next change: %r" % st)
        expect(first_trial not in nm.conns and old not in nm.conns and len(nm.conns) == 1 and os.listdir(paths.nm_run_dir) == []
               and not os.path.exists(paths.state), "put right, then changed: %r" % nm.conns)
        calls = [" ".join(c) for c in nm.calls]
        dropped = calls.index("nmcli connection delete uuid " + first_trial)
        back = [i for i, c in enumerate(calls) if c.startswith("nmcli --wait 40 connection up uuid " + old)]
        loads = [i for i, c in enumerate(calls) if c.startswith("nmcli connection load")]
        expect(back and dropped < back[0] < loads[-1], "dropped, back on the previous network, then the new trial")
        # ... and while the system shuts down, nothing.
        nm = FakeNM(paths)
        write_status(ctx, good["id"], "joining", "joining", "home")
        write_state(ctx, {"id": good["id"], "iface": "wlan0", "prev_uuid": nm.active, "trial_uuid": None})
        nm.run = lambda argv, timeout: (0, "stopping\n", "") if argv[:2] == ["systemctl", "is-system-running"] else (_ for _ in ()).throw(AssertionError(argv))
        ctx.run = nm.run
        stopped(ctx)
        expect(json.loads(open(paths.status).read())["state"] == "joining", "nothing at shutdown")

        # A stray file in RAM for a saved profile (one NetworkManager left
        # behind): the file goes, the profile never.
        nm = FakeNM(paths)
        saved_uuid = nm.active
        stray = os.path.join(paths.nm_run_dir, "otc-wifi-0badf00d.nmconnection")
        with open(stray, "w") as f:
            f.write("[connection]\nuuid=%s\n" % saved_uuid)
        st = change(nm, dict(good, ssid="elsewhere"))
        expect(st["code"] == "not_found" and saved_uuid in nm.conns and nm.active == saved_uuid and not os.path.exists(stray),
               "a saved profile kept: %r" % st)
        # ... nor one that shows that file and is active.
        nm = FakeNM(paths)
        saved_uuid = nm.active
        with open(stray, "w") as f:
            f.write("[connection]\nuuid=%s\n" % saved_uuid)
        nm.conns[saved_uuid]["file"] = stray
        st = change(nm, dict(good, ssid="elsewhere"))
        expect(saved_uuid in nm.conns and not nm_calls(nm, "connection", "delete"), "the active profile kept: %r" % st)

        # The previous connection still trying (an old password): no
        # network to go back to.
        nm = FakeNM(paths)
        nm.prev_activating = True
        st = change(nm, dict(good, psk="wrong-password"))
        expect(st["code"] == "wrong_password" and not st["left"] and not ups(nm)[1:], "previous one only activating: %r" % st)

        # On a cable after the router's password changed: our inactive
        # profile for that network gives its fixed address.
        nm = FakeNM(paths, wired_only=True)
        ours_uuid = "55555555-5555-4555-8555-555555555555"
        nm.conns[ours_uuid] = {"name": OUR_NAME, "type": "802-11-wireless", "file": "/etc/NetworkManager/system-connections/OTC-WiFi.nmconnection",
                               "ssid": "home", "autoconnect": True, "ipv4.method": "manual", "ipv4.addresses": "192.168.1.60/24",
                               "ipv4.gateway": "192.168.1.1", "ipv4.dns": "", "ipv6.method": "auto", "ipv6.addr-gen-mode": "eui64"}
        st = change(nm)
        expect(st["code"] == "connected" and "address1=192.168.1.60/24,192.168.1.1" in nm.loaded[0]
               and "addr-gen-mode=eui64" in nm.loaded[0] and ours_uuid not in nm.conns, "inactive profile's address: %r %s" % (st, nm.loaded))
        # Otherwise the address made the way NetworkManager makes it, not
        # from the MAC.
        nm = FakeNM(paths)
        nm.conns[nm.active]["ssid"] = "other"
        nm.networks["other".encode().hex().upper()] = (50, "WPA2")
        st = change(nm)
        expect(st["code"] == "connected" and "[ipv6]\nmethod=auto\naddr-gen-mode=stable-privacy\n" in nm.loaded[0], "ipv6 privacy: %s" % nm.loaded)

        # What a killed run left in root's directory goes at the next.
        nm = FakeNM(paths)
        left_behind = os.path.join(paths.state_dir, "req.99999")
        with open(left_behind, "w") as f:
            f.write(json.dumps(good))
        st = change(nm, dict(good, ssid="elsewhere"))
        expect(not os.path.exists(left_behind), "a taken request left behind")

        # A trial a crashed run left in RAM is removed at the next start.
        nm = FakeNM(paths)
        stray = os.path.join(paths.nm_run_dir, "otc-wifi-deadbeef.nmconnection")
        with open(stray, "w") as f:
            f.write("[connection]\nuuid=44444444-4444-4444-8444-444444444444\n")
        nm.conns["44444444-4444-4444-8444-444444444444"] = {"name": TRIAL_NAME, "type": "802-11-wireless", "file": stray,
                                                             "ssid": "x", "autoconnect": False}
        st = change(nm)
        expect(st["code"] == "connected" and not os.path.exists(stray) and "44444444-4444-4444-8444-444444444444" not in nm.conns,
               "stray trial removed")

        # A bad request is answered with its id; none at all is nothing.
        nm = FakeNM(paths)
        ctx.run, ctx.which = nm.run, nm.which
        os.unlink(paths.history)
        put(json.dumps(dict(good, psk="short")))
        main(ctx)
        st = json.loads(open(paths.status).read())
        expect(st["code"] == "bad_request" and st["id"] == good["id"] and not nm.calls, "bad request answered")
        expect(len(open(paths.history).read().split()) == 1, "a bad request counts toward the limit")
        main(ctx)
        expect(json.loads(open(paths.status).read()) == st, "spurious trigger changes nothing")
    print("selftest ok")


def entry(argv):
    if "--selftest" in argv:
        selftest()
        return 0
    try:
        return stopped(Ctx()) if "--stopped" in argv else main(Ctx())
    except Exception as e:
        # The type only: an error's own words may quote what it read.
        log("stopped by an error: %s" % type(e).__name__)
        return 1


if __name__ == "__main__":
    sys.exit(entry(sys.argv[1:]))
