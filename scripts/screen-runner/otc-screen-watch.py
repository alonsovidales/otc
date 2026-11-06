#!/usr/bin/python3 -I
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Off The Cloud on a connected screen: a screen plugged into the device
shows the web app, full-screen, in its screen mode (?screen=1: an
on-screen keyboard for a touch screen, nothing that leads off the device's
own pages - web/src/components/screenMode.ts).

Run as root by otc-screen.service. Every 3 s it reads the Raspberry Pi's
display outputs (/sys/class/drm/card*-HDMI-A-*/status, and a DSI panel's):

  - a screen there: otc-screen-browser.service shows the browser - Chromium
    inside cage, a Wayland compositor that shows one app full-screen and
    nothing else - as the otc-screen user, on tty7
    (otc-screen-browser.sh). The first time, both are installed from the
    distribution (a few minutes; the console says so) and the user made;
  - no screen for 15 s: the browser stops (it takes about 500 MB of
    memory) and the console is back on tty1.

A Pi looks for a screen once, as it starts, and a portable screen still
waking up then is missed: no picture until the next restart (found with a
9.7-inch portable screen). While no screen is seen, this asks the HDMI
outputs again every 10 s (writing "detect" to their status, which makes
the kernel read the screen's EDID), so a screen turned on later is found.

Only on a Raspberry Pi with 4 GB of memory or more (the 2 GB one runs one
library and little else), and never where a desktop already runs
(display-manager.service: a Pi of the owner's own with Raspberry Pi OS's
desktop keeps it). Chromium's policies (policy(), written here with the
device's HTTP port) keep it on the device's own pages: no other site, no
downloads, no file picker, no password manager.

--selftest checks it without root, sysfs, systemd or apt (on a Mac too):
python3 scripts/screen-runner/otc-screen-watch.py --selftest
"""

import grp
import json
import os
import pwd
import re
import subprocess
import sys
import tempfile
import time

POLL = 3.0
GONE_AFTER = 15.0
PROBE_EVERY = 10.0
INSTALL_RETRY = 1800.0
DESKTOP_CHECK_EVERY = 60.0
MIN_MEMORY_MB = 3000
# The packages take about 600 MB, their downloads 200 MB more until cleaned.
MIN_FREE_MB = 1500
USER = "otc-screen"
HOME = "/var/lib/otc-screen"
GROUPS = ("video", "render", "input")
# chromium and cage from the distribution (Raspberry Pi OS's own chromium
# build, kept current by the daily security updates: install.sh's
# Origins-Pattern takes the Raspberry Pi archive's main component); chvt
# from kbd; wlr-randr sets a sharp screen's scale (screen_scale); the fonts
# the web app falls back to, emoji included.
PACKAGES = ("cage", "chromium", "kbd", "wlr-randr", "fonts-dejavu-core", "fonts-noto-color-emoji")
BROWSER_UNIT = "otc-screen-browser.service"
DEFAULT_PORT = 8080
# card1-HDMI-A-1, card1-HDMI-A-2, card2-DSI-1 - not Writeback, not Composite.
OUTPUT_RE = re.compile(r"card[0-9]+-(HDMI-A|DSI)-[0-9]+")
RUNNING = ("active", "activating", "reloading", "deactivating")
# The page's zoom on a small sharp screen: about an iPad's 132 CSS pixels
# per inch, in quarters, from 1 to 3.
CSS_PPI = 132.0
EDID_HEADER = b"\x00\xff\xff\xff\xff\xff\xff\x00"

SUBPROCESS_ENV = {
    "LC_ALL": "C.UTF-8",
    "PATH": "/usr/sbin:/usr/bin:/sbin:/bin",
    "DEBIAN_FRONTEND": "noninteractive",
}
APT = ["apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
       "-o", "DPkg::Lock::Timeout=600"]


class Paths:
    def __init__(self, root=""):
        self.root = root or "/"
        self.drm = root + "/sys/class/drm"
        self.model = root + "/proc/device-tree/model"
        self.meminfo = root + "/proc/meminfo"
        # install.sh's ENVIRONMENT is dev on every device.
        self.ini = root + "/etc/otc_dev.ini"
        self.env = root + "/run/otc-screen/browser.env"
        self.policy = root + "/etc/chromium/policies/managed/otc-screen.json"
        self.console = root + "/dev/tty1"
        self.cage = root + "/usr/bin/cage"
        self.chromium = root + "/usr/bin/chromium"
        self.wlr_randr = root + "/usr/bin/wlr-randr"
        self.home = root + HOME


def log(msg):
    print("[otc-screen] " + msg, flush=True)


def real_run(argv, timeout):
    """(returncode, stdout, stderr) of argv - never a shell. A timeout is
    124, a missing program 127."""
    try:
        p = subprocess.run(argv, stdin=subprocess.DEVNULL, capture_output=True, text=True,
                           env=SUBPROCESS_ENV, timeout=timeout)
        return p.returncode, p.stdout, p.stderr
    except subprocess.TimeoutExpired:
        return 124, "", "timed out"
    except OSError as e:
        return 127, "", type(e).__name__


def real_user_exists(name):
    try:
        pwd.getpwnam(name)
        return True
    except KeyError:
        return False


def real_group_exists(name):
    try:
        grp.getgrnam(name)
        return True
    except KeyError:
        return False


def real_free_mb(path):
    try:
        st = os.statvfs(path)
        return st.f_bavail * st.f_frsize // (1024 * 1024)
    except OSError:
        return 0


class Ctx:
    def __init__(self, paths=None):
        self.paths = paths or Paths()
        self.run = real_run
        self.user_exists = real_user_exists
        self.group_exists = real_group_exists
        self.free_mb = real_free_mb
        self.clock = time.monotonic
        self.sleep = time.sleep


def outputs(paths):
    """[(name, status)] of the outputs a screen plugs into: status is the
    kernel's connected / disconnected / unknown."""
    found = []
    try:
        names = sorted(os.listdir(paths.drm))
    except OSError:
        return found
    for name in names:
        if not OUTPUT_RE.fullmatch(name):
            continue
        try:
            with open(os.path.join(paths.drm, name, "status")) as f:
                found.append((name, f.read().strip()))
        except OSError:
            continue
    return found


def probe(paths, outs):
    """Asks each disconnected HDMI output whether a screen is there now.
    Never one that says connected: "detect" also clears a forced output
    (video=...D on cmdline.txt), and a forced one never says disconnected."""
    for name, status in outs:
        if status != "disconnected" or "-HDMI-A-" not in name:
            continue
        try:
            with open(os.path.join(paths.drm, name, "status"), "w") as f:
                f.write("detect")
        except OSError:
            pass


def screen_scale(edid):
    """The page's zoom for a screen, from its EDID: its native width (the
    first detailed timing) over its width in mm. A 9.7-inch 2048 x 1536
    screen is 264 pixels per inch: shown at 1, the web app would be a
    quarter of its size, so it is shown at 2 (1024 x 768 CSS pixels, as an
    iPad). 1 for any screen at a normal density, and when the EDID says
    nothing usable (a TV or a projector often gives no size)."""
    if len(edid) < 128 or edid[:8] != EDID_HEADER:
        return 1.0
    d = edid[54:72]
    if d[0] == 0 and d[1] == 0:
        return 1.0
    width = d[2] | ((d[4] & 0xF0) << 4)
    mm = d[12] | ((d[14] & 0xF0) << 4)
    if not mm:
        mm = edid[21] * 10
    if width <= 0 or mm < 50:
        return 1.0
    scale = round(width / (mm / 25.4) / CSS_PPI * 4) / 4
    return max(1.0, min(3.0, scale))


def read_edid(paths, name):
    try:
        with open(os.path.join(paths.drm, name, "edid"), "rb") as f:
            return f.read(256)
    except OSError:
        return b""


def is_pi(paths):
    try:
        with open(paths.model, "rb") as f:
            return b"Raspberry Pi" in f.read()
    except OSError:
        return False


def memory_mb(paths):
    try:
        with open(paths.meminfo) as f:
            for line in f:
                if line.startswith("MemTotal:"):
                    return int(line.split()[1]) // 1024
    except (OSError, ValueError, IndexError):
        pass
    return 0


def http_port(paths):
    """[otc-api] port of the device's config: where its web app is."""
    section = ""
    try:
        with open(paths.ini) as f:
            for line in f:
                line = line.strip()
                if line.startswith("[") and line.endswith("]"):
                    section = line[1:-1].strip()
                elif section == "otc-api" and "=" in line:
                    key, value = (s.strip() for s in line.split("=", 1))
                    if key == "port" and value.isdigit() and 0 < int(value) < 65536:
                        return int(value)
    except OSError:
        pass
    return DEFAULT_PORT


def url(port):
    return "http://127.0.0.1:%d/?screen=1" % port


def policy(port):
    """Chromium's managed policies: the device's own pages only (the
    blocklist stops navigations; the map's tiles, fetched by the page,
    still load), nothing saved on or taken from the device's disk, no
    Google account, nothing that asks."""
    return {
        "URLBlocklist": ["*"],
        "URLAllowlist": ["http://127.0.0.1:%d" % port],
        "DownloadRestrictions": 3,
        "AllowFileSelectionDialogs": False,
        "PasswordManagerEnabled": False,
        "AutofillAddressEnabled": False,
        "AutofillCreditCardEnabled": False,
        "TranslateEnabled": False,
        "SpellcheckEnabled": False,
        "BrowserSignin": 0,
        "SyncDisabled": True,
        "MetricsReportingEnabled": False,
        "SearchSuggestEnabled": False,
        "NetworkPredictionOptions": 2,
        "DefaultNotificationsSetting": 2,
        "DefaultGeolocationSetting": 2,
        "DeveloperToolsAvailability": 2,
        "PrintingEnabled": False,
        "BrowserGuestModeEnabled": False,
        "BrowserAddPersonEnabled": False,
        "IncognitoModeAvailability": 1,
        "BackgroundModeEnabled": False,
    }


def write_if_changed(path, data, mode):
    """Writes data to path (atomically, mode) unless it is there already."""
    try:
        with open(path, "rb") as f:
            if f.read() == data:
                return False
    except OSError:
        pass
    d = os.path.dirname(path)
    os.makedirs(d, mode=0o755, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".otc-screen-")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return True


def tell(paths, msg):
    """A line on the console (tty1), which the screen shows until the
    browser is up."""
    try:
        with open(paths.console, "w") as f:
            f.write("\r\n" + msg + "\r\n")
    except OSError:
        pass


def unit_running(ctx, unit):
    _, out, _ = ctx.run(["systemctl", "is-active", unit], 10)
    return out.strip() in RUNNING


OK, LATER, FAILED = "ok", "later", "failed"


def install(ctx):
    """Installs the browser and makes its user, if not there yet: OK, LATER
    (an update runs: its own apt comes first) or FAILED."""
    p = ctx.paths
    programs = (p.cage, p.chromium, p.wlr_randr)
    if not all(os.path.exists(f) for f in programs):
        if unit_running(ctx, "otc-update.service"):
            return LATER
        if ctx.free_mb(p.root) < MIN_FREE_MB:
            log("not enough free space on the system's disk for the browser")
            tell(p, "Off The Cloud: there isn't enough free space on this device's microSD card"
                 " to show it on this screen.")
            return FAILED
        log("a screen: installing the browser")
        tell(p, "Off The Cloud: setting up this screen - installing the browser, a few minutes...")
        ctx.run(APT + ["update"], 900)
        rc, _, err = ctx.run(APT + ["install", "--no-install-recommends"] + list(PACKAGES), 3600)
        if rc != 0 or not all(os.path.exists(f) for f in programs):
            log("could not install the browser (apt-get %d): %s" % (rc, err.strip()[-300:]))
            tell(p, "Off The Cloud: the browser for this screen could not be installed."
                 " It is tried again in 30 minutes.")
            return FAILED
        # The downloaded packages (about 200 MB) aren't needed once installed.
        ctx.run(["apt-get", "clean"], 300)
    if not ctx.user_exists(USER):
        argv = ["useradd", "--system", "--user-group", "--create-home", "--home-dir", HOME,
                "--shell", "/usr/sbin/nologin", "--comment", "Off The Cloud screen"]
        groups = [g for g in GROUPS if ctx.group_exists(g)]
        if groups:
            argv += ["--groups", ",".join(groups)]
        rc, _, err = ctx.run(argv + [USER], 60)
        if rc != 0 or not ctx.user_exists(USER):
            log("could not make the %s user: %s" % (USER, err.strip()[-300:]))
            return FAILED
        try:
            os.chmod(p.home, 0o700)
        except OSError:
            pass
    return OK


class Watcher:
    def __init__(self, ctx):
        self.ctx = ctx
        self.seen_at = None
        self.probed_at = None
        self.retry_at = None
        self.desktop_at = None
        self.desktop = False

    def eligible(self):
        """'' when this device can show itself on a screen, else why not."""
        p = self.ctx.paths
        if not is_pi(p):
            return "not a Raspberry Pi"
        mb = memory_mb(p)
        if mb < MIN_MEMORY_MB:
            return "%d MB of memory (the browser needs a 4 GB Raspberry Pi or more)" % mb
        return ""

    def step(self):
        ctx, p = self.ctx, self.ctx.paths
        now = ctx.clock()
        if self.desktop_at is None or now - self.desktop_at >= DESKTOP_CHECK_EVERY:
            self.desktop_at = now
            desktop = unit_running(ctx, "display-manager.service")
            if desktop != self.desktop:
                log("a desktop runs on this device: it keeps the screen" if desktop
                    else "no desktop on this device")
            self.desktop = desktop
        outs = outputs(p)
        present = any(status == "connected" for _, status in outs)
        if present:
            self.seen_at = now
        running = unit_running(ctx, BROWSER_UNIT)
        if present and not self.desktop:
            if not running and (self.retry_at is None or now >= self.retry_at):
                got = install(ctx)
                if got == OK:
                    self.retry_at = None
                    port = http_port(p)
                    shown = [n for n, s in outs if s == "connected"]
                    scale = screen_scale(read_edid(p, shown[0]))
                    write_if_changed(p.env, ("OTC_SCREEN_URL=%s\nOTC_SCREEN_SCALE=%s\n" % (url(port), scale)).encode(),
                                     0o644)
                    write_if_changed(p.policy, (json.dumps(policy(port), indent=2, sort_keys=True) + "\n").encode(),
                                     0o644)
                    log("a screen: showing the device on it (%s, zoom %s)" % (", ".join(shown), scale))
                    ctx.run(["systemctl", "start", "--no-block", BROWSER_UNIT], 30)
                elif got == FAILED:
                    self.retry_at = now + INSTALL_RETRY
        elif running and (self.desktop or self.seen_at is None or now - self.seen_at >= GONE_AFTER):
            log("a desktop: the browser stops" if self.desktop else "no screen: the browser stops")
            ctx.run(["systemctl", "stop", "--no-block", BROWSER_UNIT], 30)
        if not present and (self.probed_at is None or now - self.probed_at >= PROBE_EVERY):
            self.probed_at = now
            probe(p, outs)


def main(ctx):
    w = Watcher(ctx)
    why = w.eligible()
    if why:
        log("this device doesn't show itself on a screen: " + why)
        return 0
    log("watching for a screen")
    while True:
        try:
            w.step()
        except Exception as e:
            # The type only: an error's own words may quote what it read.
            log("a check failed: %s" % type(e).__name__)
        ctx.sleep(POLL)


def selftest():
    def expect(cond, what):
        if not cond:
            raise AssertionError(what)

    with tempfile.TemporaryDirectory() as tmp:
        paths = Paths(tmp)
        for d in ("sys/class/drm/card1-HDMI-A-1", "sys/class/drm/card1-HDMI-A-2", "sys/class/drm/card1-Writeback-1",
                  "sys/class/drm/card0", "proc/device-tree", "etc", "dev", "usr/bin", "run"):
            os.makedirs(os.path.join(tmp, d))

        def put(rel, text):
            with open(os.path.join(tmp, rel), "w") as f:
                f.write(text)

        def get(rel):
            with open(os.path.join(tmp, rel)) as f:
                return f.read()

        def status(port, value):
            put("sys/class/drm/card1-HDMI-A-%d/status" % port, value + "\n")

        status(1, "disconnected")
        status(2, "disconnected")
        put("sys/class/drm/card1-Writeback-1/status", "unknown\n")
        put("proc/device-tree/model", "Raspberry Pi 5 Model B Rev 1.0\x00")
        put("proc/meminfo", "MemTotal:        8245328 kB\nMemFree:  100 kB\n")
        put("etc/otc_dev.ini", "[otc]\nport=1\n\n[otc-api]\nbase-url=otc/\nport = 8080\nssl-port=443\n")
        put("dev/tty1", "")

        class System:
            def __init__(self):
                self.calls = []
                self.users = set()
                self.active = set()
                self.apt_rc = 0
                self.free = 20000

            def run(self, argv, timeout):
                self.calls.append(argv)
                if argv[:2] == ["systemctl", "is-active"]:
                    return (0, "active\n", "") if argv[2] in self.active else (3, "inactive\n", "")
                if argv[:3] == ["systemctl", "start", "--no-block"]:
                    self.active.add(argv[3])
                elif argv[:3] == ["systemctl", "stop", "--no-block"]:
                    self.active.discard(argv[3])
                elif argv[0] == "apt-get" and "install" in argv:
                    if self.apt_rc == 0:
                        for prog in ("cage", "chromium", "wlr-randr"):
                            put("usr/bin/" + prog, "")
                    return self.apt_rc, "", "E: no network" if self.apt_rc else ""
                elif argv[0] == "useradd":
                    self.users.add(argv[-1])
                return 0, "", ""

            def named(self, word):
                return [c for c in self.calls if word in " ".join(c)]

        sysd = System()
        ctx = Ctx(paths)
        ctx.run = sysd.run
        ctx.user_exists = lambda name: name in sysd.users
        ctx.group_exists = lambda name: name in ("video", "render")
        ctx.free_mb = lambda path: sysd.free
        clock = [100.0]
        ctx.clock = lambda: clock[0]

        # Which outputs count, and the port.
        expect([n for n, _ in outputs(paths)] == ["card1-HDMI-A-1", "card1-HDMI-A-2"], "outputs: HDMI only")
        expect(http_port(paths) == 8080, "port from [otc-api]")
        put("etc/otc_dev.ini", "[otc-api]\nport=8081\n")
        expect(http_port(paths) == 8081, "another port")
        put("etc/otc_dev.ini", "[otc-api]\nport=80a\n")
        expect(http_port(paths) == DEFAULT_PORT, "a bad port is the default")
        os.unlink(paths.ini)
        expect(http_port(paths) == DEFAULT_PORT, "no config is the default")
        put("etc/otc_dev.ini", "[otc-api]\nport=8080\n")
        expect(url(8080) == "http://127.0.0.1:8080/?screen=1", "url")
        expect(policy(8080)["URLAllowlist"] == ["http://127.0.0.1:8080"] and policy(8080)["URLBlocklist"] == ["*"],
               "policy: the device's own pages only")

        w = Watcher(ctx)
        expect(w.eligible() == "", "an 8 GB Pi is eligible")

        # No screen: nothing starts, the HDMI outputs are asked again every
        # 10 s (the Writeback one never).
        w.step()
        expect(get("sys/class/drm/card1-HDMI-A-1/status") == "detect"
               and get("sys/class/drm/card1-HDMI-A-2/status") == "detect", "probed both HDMI outputs")
        expect(get("sys/class/drm/card1-Writeback-1/status") == "unknown\n", "Writeback untouched")
        expect(not sysd.named("apt-get") and not sysd.named("start"), "nothing installed or started")
        status(1, "disconnected")
        status(2, "disconnected")
        clock[0] += 3
        w.step()
        expect(get("sys/class/drm/card1-HDMI-A-1/status") == "disconnected\n", "not probed again within 10 s")
        clock[0] += 7
        w.step()
        expect(get("sys/class/drm/card1-HDMI-A-1/status") == "detect", "probed again after 10 s")
        status(1, "disconnected")

        # A screen: installed (update, install), the user made, the
        # address and the policy written, the browser started.
        status(2, "connected")
        clock[0] += 3
        w.step()
        apts = sysd.named("apt-get")
        expect(len(apts) == 3 and apts[0][-1] == "update" and apts[2] == ["apt-get", "clean"], "apt update, install, clean")
        inst = apts[1]
        expect(inst[-len(PACKAGES):] == list(PACKAGES) and "--no-install-recommends" in inst, "the packages")
        add = sysd.named("useradd")
        expect(len(add) == 1 and add[0][-1] == USER and "video,render" in add[0] and "input" not in " ".join(add[0]),
               "user made with the groups there are")
        expect(get("run/otc-screen/browser.env") == "OTC_SCREEN_URL=http://127.0.0.1:8080/?screen=1\nOTC_SCREEN_SCALE=1.0\n",
               "env written (no EDID: zoom 1)")
        expect(json.loads(get("etc/chromium/policies/managed/otc-screen.json")) == policy(8080), "policy written")
        expect(BROWSER_UNIT in sysd.active, "browser started")
        expect(get("sys/class/drm/card1-HDMI-A-1/status") == "disconnected\n", "no probing with a screen there")
        expect("installing the browser" in get("dev/tty1"), "the console says so")

        # Still there: nothing more. Gone 14 s: kept; 15 s: stopped.
        n = len(sysd.calls)
        clock[0] += 3
        w.step()
        expect(not [c for c in sysd.calls[n:] if c[:2] != ["systemctl", "is-active"]], "nothing more while it's there")
        status(2, "disconnected")
        clock[0] += 14
        w.step()
        expect(BROWSER_UNIT in sysd.active, "kept through a short gap")
        clock[0] += 1
        w.step()
        expect(BROWSER_UNIT not in sysd.active, "stopped 15 s after the screen went")

        # Back: started again, nothing installed again.
        status(2, "connected")
        clock[0] += 3
        w.step()
        expect(BROWSER_UNIT in sysd.active and len(sysd.named("apt-get")) == 3 and len(sysd.named("useradd")) == 1,
               "started again without installing")

        # A desktop takes the screen: stopped, and never started while it runs.
        sysd.active.add("display-manager.service")
        clock[0] += 60
        w.step()
        expect(BROWSER_UNIT not in sysd.active, "stopped for a desktop")
        clock[0] += 3
        w.step()
        expect(BROWSER_UNIT not in sysd.active, "not started with a desktop")
        sysd.active.discard("display-manager.service")
        clock[0] += 60
        w.step()
        expect(BROWSER_UNIT in sysd.active, "started once the desktop is gone")

        # A failed install: told, tried again 30 minutes later, not before.
        os.unlink(paths.cage)
        sysd.active.discard(BROWSER_UNIT)
        sysd.apt_rc = 100
        clock[0] += 3
        w.step()
        expect(BROWSER_UNIT not in sysd.active and "tried again in 30 minutes" in get("dev/tty1"), "install failed")
        apts = len(sysd.named("apt-get"))
        clock[0] += 1700
        w.step()
        expect(len(sysd.named("apt-get")) == apts, "not tried again before 30 minutes")
        sysd.apt_rc = 0
        clock[0] += 101
        w.step()
        expect(BROWSER_UNIT in sysd.active, "installed and started on the retry")

        # An update running: the install waits for it, with no 30-minute wait.
        os.unlink(paths.cage)
        sysd.active = {"otc-update.service"}
        apts = len(sysd.named("apt-get"))
        clock[0] += 3
        w.step()
        expect(len(sysd.named("apt-get")) == apts and BROWSER_UNIT not in sysd.active, "no apt during an update")
        sysd.active = set()
        clock[0] += 3
        w.step()
        expect(BROWSER_UNIT in sysd.active, "installed right after the update")

        # Too little space: told, nothing installed.
        os.unlink(paths.cage)
        sysd.active = set()
        sysd.free = 500
        apts = len(sysd.named("apt-get"))
        w2 = Watcher(ctx)
        clock[0] += 3
        w2.step()
        expect(len(sysd.named("apt-get")) == apts and "enough free space" in get("dev/tty1"), "no space")
        sysd.free = 20000

        # Which devices.
        put("proc/meminfo", "MemTotal:        1999992 kB\n")
        expect("4 GB" in Watcher(ctx).eligible(), "a 2 GB Pi isn't")
        put("proc/meminfo", "MemTotal:        4045704 kB\n")
        expect(Watcher(ctx).eligible() == "", "a 4 GB Pi is")
        put("proc/device-tree/model", "Some Other Board\x00")
        expect(Watcher(ctx).eligible() == "not a Raspberry Pi", "another board isn't")
        os.unlink(paths.model)
        expect(Watcher(ctx).eligible() == "not a Raspberry Pi", "no device tree (a PC, a VPS) isn't")
        expect(main(ctx) == 0, "main returns at once where it can't show anything")

        # The zoom from the EDID: width in pixels over width in mm.
        def edid(width, mm, cm=0):
            e = bytearray(128)
            e[:8] = EDID_HEADER
            e[21] = cm
            e[54], e[55] = 0x3A, 0x02
            e[56], e[58] = width & 0xFF, (width >> 8) << 4
            e[66], e[68] = mm & 0xFF, (mm >> 8) << 4
            return bytes(e)
        for e, want in [(edid(2048, 197), 2.0), (edid(1024, 197), 1.0), (edid(1920, 344), 1.0),
                        (edid(1920, 293), 1.25), (edid(3840, 293), 2.5), (edid(3840, 0, 0), 1.0),
                        (edid(2048, 0, 20), 2.0), (edid(3840, 100), 3.0), (edid(1280, 20), 1.0),
                        (b"", 1.0), (b"\x00" * 128, 1.0)]:
            expect(screen_scale(e) == want, "zoom %r, want %s" % (screen_scale(e), want))
        put("sys/class/drm/card1-HDMI-A-2/edid", "")
        with open(os.path.join(tmp, "sys/class/drm/card1-HDMI-A-2/edid"), "wb") as f:
            f.write(edid(2048, 197))
        os.unlink(paths.env)
        sysd.active = set()
        put("proc/meminfo", "MemTotal:        8245328 kB\n")
        put("proc/device-tree/model", "Raspberry Pi 5 Model B Rev 1.0\x00")
        status(2, "connected")
        w3 = Watcher(ctx)
        clock[0] += 3
        w3.step()
        expect(get("run/otc-screen/browser.env").endswith("OTC_SCREEN_SCALE=2.0\n"), "a 2048-pixel 9.7-inch screen: zoom 2")

        # write_if_changed: rewritten only when different.
        f = os.path.join(tmp, "run", "x")
        expect(write_if_changed(f, b"a", 0o644) and not write_if_changed(f, b"a", 0o644)
               and write_if_changed(f, b"b", 0o644), "write_if_changed")
    print("selftest ok")


def entry(argv):
    if "--selftest" in argv:
        selftest()
        return 0
    return main(Ctx())


if __name__ == "__main__":
    sys.exit(entry(sys.argv[1:]))
