#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
RAID1 watcher for Raspberry Pi + GPIO LEDs.

Features
- Monitors /dev/md0 with mdadm + /proc/mdstat polling.
- LED rules:
  * Both healthy/in-sync  -> both LEDs solid green.
  * Any member failed/missing -> that LED solid red; the other reflects its state.
  * Rebuild/resync:
      - Source (up-to-date) member -> blink green.
      - Target (rebuilding) member -> blink red.
- Auto-repair (optional):
  * When degraded and a new disk appears, partition it as a single Linux RAID member
    and mdadm --add it to /dev/md0 to start rebuild.

Wiring
- Two bi-color LEDs with *separate* Red and Green pins per LED (common cathode to GND).
- Set BCM pin numbers in CONFIG below.

Run
- sudo apt install mdadm parted
- sudo pip3 install RPi.GPIO
- sudo python3 raid_watch.py
- (Optional) install as a systemd service (unit file example at the bottom).

"""

import os
import re
import sys
import time
import json
import stat
import glob
import shlex
import signal
import subprocess
from pathlib import Path

# Running under systemd, stdout is a pipe to journald, not a TTY — Python
# switches to full block buffering in that case (only a TTY gets automatic
# line buffering), so print() output can sit invisible for a long time
# with nothing forcing a flush. Found this the hard way debugging a
# storage-setup request that had actually already been applied correctly.
sys.stdout.reconfigure(line_buffering=True)

# ----------------------------
# CONFIG (edit to your setup)
# ----------------------------
CONFIG = {
    # Your array device
    "raid_dev": "/dev/md0",

    # Issue #38/#39: the otc service (unprivileged, hardened systemd unit —
    # it can't run wipefs/mdadm/mkfs itself) writes the owner's storage
    # choice from the first-run setup wizard here as JSON:
    # {"device_paths": ["/dev/sda", "/dev/sdb"]}  (0, 1, or 2 paths).
    # This script runs as root, so it's the one that actually does the
    # one-time destructive bootstrap (wipe + mdadm --create/mkfs + mount),
    # then deletes the request file. Everything after that first build is
    # this same script's existing job: watch/repair the array.
    "setup_request_file": "/var/lib/otc/storage_setup_request.json",
    "mount_point": "/mnt/storage",
    "unenc_subdir": "unencrypted",

    # GPIO pins (BCM numbering) for each LED (member slot 0 and 1)
    # Example pins; change to your wiring.
    "leds": {
        0: {"red": 17, "green": 27},  # drive in array slot 0 (RaidDevice=0)
        1: {"red": 22, "green": 23},  # drive in array slot 1 (RaidDevice=1)
    },

    # Poll interval (seconds)
    "poll_s": 0.5,

    # Blink period (seconds) – half period ON, half period OFF
    "blink_period_s": 0.1,

    # Auto-repair (DANGEROUS): when degraded and a new disk of similar size appears,
    # wipe and add it automatically to md0 (will DESTROY data on the new disk).
    "auto_repair_enable": True,

    # Minimum capacity ratio the candidate disk must have relative to the existing member
    # e.g. 0.98 means >=98% of size
    "auto_repair_min_size_ratio": 0.98,

    # Preferred block device to add: partition (/dev/sdX1) or whole disk (/dev/sdX)
    # If "partition", we will create a GPT with one partition flagged for RAID and add /dev/sdX1.
    "auto_add_mode": "whole",  # "partition" or "whole"

    # Device selection strategy when multiple candidates are available:
    # "newest" (by /dev/disk/by-id timestamp), or "largest"
    "auto_candidate_strategy": "largest",

    # Paths to tools
    "paths": {
        "mdadm": "/sbin/mdadm",
        "parted": "/usr/sbin/parted",
        "sgdisk": "/usr/sbin/sgdisk",  # optional
        "lsblk": "/bin/lsblk",
        "wipefs": "/sbin/wipefs",
        "blockdev": "/sbin/blockdev",
        "dumpe2fs": "/sbin/dumpe2fs",
        "resize2fs": "/sbin/resize2fs",
    },
}

# ----------------------------
# GPIO setup
# ----------------------------
# The lights are optional: on a machine that isn't a Raspberry Pi the GPIO
# module may be missing, refuse to import ("This module can only be run on
# a Raspberry Pi!" is a RuntimeError, not an ImportError) or fail when a pin
# is set up or driven. Any of those turns the lights off for good and the
# mirror is watched, repaired and grown exactly as on a Pi - an exception
# here used to end the script, and systemd restarted it into the same one.
try:
    import RPi.GPIO as GPIO
except Exception as e:
    GPIO = None
    print(f"WARNING: GPIO not available ({e}). No status lights; the mirror is still watched.")

def gpio_off(why):
    global GPIO
    if GPIO is not None:
        print(f"WARNING: GPIO failed ({why}). No status lights from now on; the mirror is still watched.")
    GPIO = None

def gpio_setup():
    if GPIO is None:
        return
    try:
        GPIO.setmode(GPIO.BCM)
        for slot, pins in CONFIG["leds"].items():
            for color in ("red", "green"):
                GPIO.setup(pins[color], GPIO.OUT)
                GPIO.output(pins[color], GPIO.LOW)
    except Exception as e:
        gpio_off(e)

def gpio_cleanup():
    if GPIO is None:
        return
    try:
        GPIO.cleanup()
    except Exception:
        pass

def led_set(slot: int, red_on: bool, green_on: bool):
    pins = CONFIG["leds"][slot]
    if GPIO is None:
        # Dry-run printout
        return
    try:
        GPIO.output(pins["red"], GPIO.HIGH if red_on else GPIO.LOW)
        GPIO.output(pins["green"], GPIO.HIGH if green_on else GPIO.LOW)
    except Exception as e:
        gpio_off(e)

# ----------------------------
# Helpers
# ----------------------------
def run(cmd, check=False, capture=True, text=True):
    if isinstance(cmd, str):
        cmd = shlex.split(cmd)
    try:
        res = subprocess.run(cmd, check=check, capture_output=capture, text=text)
        return res
    except Exception as e:
        return subprocess.CompletedProcess(cmd, 1, "", str(e))

def mdadm_detail(raid_dev):
    """
    Parse `mdadm --detail` and return:
      {
        "state": "clean, resyncing",  # array state string
        "members": {
           0: {"device": "/dev/sdb1", "state": "spare rebuilding"},
           1: {"device": "/dev/sda1", "state": "active sync"},
        }
      }
    Keys in 'members' are the RAID DEVICE SLOTS (RaidDevice), not mdadm 'Number'.
    """
    out = run([CONFIG["paths"]["mdadm"], "--detail", raid_dev]).stdout
    info = {"state": "", "members": {}, "other_devices": set()}

    m = re.search(r"State\s*:\s*(.+)", out)
    if m:
        info["state"] = m.group(1).strip().lower()

    # Header (for reference):
    # Number  Major  Minor  RaidDevice  State            Device
    #    2       8     16          0    spare rebuilding /dev/sdb
    #    1       8      0          1    active sync      /dev/sda

    # Capture: Number, Major, Minor, RaidDevice, State, Device
    member_re = re.compile(
        r"^\s*(\d+)\s+\d+\s+\d+\s+(-?\d+)\s+(.+?)\s+(/dev/\S+)\s*$",
        re.M
    )
    for number, raiddev, state, dev in member_re.findall(out):
        # Use RaidDevice as the slot index (0,1,...). Sometimes mdadm shows '-' for true spares.
        if raiddev.strip() == '-' or not raiddev.strip().isdigit():
            # Unassigned spare; skip slot mapping (or map later if needed)
            continue
        slot = int(raiddev)
        info["members"][slot] = {"device": dev.strip(), "state": state.strip().lower()}

    # Rows with no slot (RaidDevice "-"): a disk md marked faulty but that
    # is still attached, or a spare. Not members (the LEDs and the
    # degraded check go by slots), but still md's: never a candidate to
    # add, which only ever failed with "busy", every poll.
    other_re = re.compile(r"^\s*(?:\d+|-)\s+\d+\s+\d+\s+-\s+(.+?)\s+(/dev/\S+)\s*$", re.M)
    for _state, dev in other_re.findall(out):
        info["other_devices"].add(dev.strip())

    return info

def mdadm_examine(dev):
    """Return metadata: Events (int), Update Time (str), Array UUID (str)."""
    out = run([CONFIG["paths"]["mdadm"], "--examine", dev]).stdout
    events = None
    update_time = None
    uuid = None
    m = re.search(r"Events :\s*(\d+)", out)
    if m:
        events = int(m.group(1))
    m = re.search(r"Update Time :\s*(.+)", out)
    if m:
        update_time = m.group(1).strip()
    m = re.search(r"Array UUID :\s*([0-9a-fA-F:-]+)", out)
    if m:
        uuid = m.group(1).strip()
    return {"events": events, "update_time": update_time, "uuid": uuid}

def mdstat():
    """Return /proc/mdstat text."""
    try:
        return Path("/proc/mdstat").read_text()
    except Exception:
        return ""

def array_is_resyncing(mdstat_text, raid_name):
    """Detect if array is resyncing/recovering and return bool."""
    # Look for a section starting with md0 : or similar, then lines containing 'resync' or 'recovery'
    sect_re = re.compile(rf"^{re.escape(raid_name)}\s*:\s.*?(?=^\S|\Z)", re.S | re.M)
    m = sect_re.search(mdstat_text)
    if not m:
        return False
    return ("resync" in m.group(0)) or ("recovery" in m.group(0)) or ("rebuild" in m.group(0))

def list_block_disks():
    """Return list of /dev/sdX device names (whole disks, not partitions) and sizes in bytes."""
    out = run([CONFIG["paths"]["lsblk"], "-bndo", "NAME,TYPE,SIZE"]).stdout
    disks = []
    for line in out.splitlines():
        parts = line.split()
        if len(parts) != 3:
            continue
        name, typ, size = parts
        if typ == "disk" and name.startswith("sd"):
            disks.append(("/dev/" + name, int(size)))
    return disks

def size_of(dev):
    """Return size in bytes of device (whole or partition)."""
    out = run([CONFIG["paths"]["lsblk"], "-bndo", "SIZE", dev]).stdout.strip()
    try:
        return int(out)
    except:
        return 0

def device_in_array(dev, detail):
    for member in detail["members"].values():
        if member["device"] == dev:
            return True
    return False

def base_disk(dev_path: str) -> str:
    """Return the base disk for a device or partition.
       /dev/sda1 -> /dev/sda, /dev/sda -> /dev/sda"""
    name = os.path.basename(dev_path)
    # strip trailing partition digits (handles sda1, sda10, etc.)
    m = re.match(r"^(sd[a-z]+)", name)
    if m:
        return "/dev/" + m.group(1)
    return dev_path

def member_base_disks(detail) -> set[str]:
    bases = set()
    for m in detail.get("members", {}).values():
        bases.add(base_disk(m["device"]))
    for d in detail.get("other_devices", ()):
        bases.add(base_disk(d))
    return bases

def root_base_disk() -> str | None:
    # which device backs /
    res = run(["findmnt", "-no", "SOURCE", "/"])
    src = (res.stdout or "").strip()
    if not src:
        return None
    # if it's /dev/mmcblk0p2 or LVM/MD, this will resolve to something non-sd*
    # only exclude if it resolves to an sdX base
    if src.startswith("/dev/"):
        return base_disk(src)
    return None

def pick_candidate_disk(existing_member_size, detail, skip=frozenset(), disks=None, why=None):
    """Pick a disk not in md array, large enough, and not the root disk.
    `skip` holds (disk, size) pairs whose last add failed and are waiting
    out their backoff - filtered here, so one of those can't hide another,
    valid disk. `disks` is list_block_disks() when the caller has it.
    `why`, a dict, gets each disk left out that isn't the mirror's own, with
    the reason in words, for the storage log."""
    if why is None:
        why = {}
    # Disks present
    if disks is None:
        disks = list_block_disks()  # list of ("/dev/sdX", size)
    # Disks to exclude: any base disk already present in array
    in_array_bases = member_base_disks(detail)
    # Also exclude the root disk (if it’s an sdX device)
    root_base = root_base_disk()

    candidates = []
    for d, sz in disks:
        base = base_disk(d)

        # Exclude empty devices (e.g. a card reader with nothing inserted
        # reports as a real disk with size 0) — otherwise these get picked
        # as a "candidate" every single poll and mdadm --add just fails on
        # them forever, spamming the log for no benefit.
        if sz == 0:
            why[d] = "no card in it"
            continue

        # A failed add being backed off (see main).
        if (d, sz) in skip:
            why[d] = "adding it failed; it is tried again in a few minutes"
            continue

        # Exclude any disk that is already a member (whole disk or parent of a partition member)
        if base in in_array_bases:
            continue

        # Exclude root disk if applicable
        if root_base and base == root_base:
            why[d] = "it holds the system"
            continue

        # Exclude mounted disks (anything with a mountpoint)
        mounts = run(["lsblk", "-ndo", "MOUNTPOINT", d]).stdout.strip().splitlines()
        if any(m for m in mounts if m):
            why[d] = "it is mounted (in use)"
            continue

        # Must be large enough relative to existing member
        if existing_member_size and sz < int(existing_member_size * CONFIG["auto_repair_min_size_ratio"]):
            why[d] = f"too small: {human_size(sz)}, and the working disk is {human_size(existing_member_size)}"
            continue

        candidates.append((d, sz))

    if not candidates:
        return None

    if CONFIG["auto_candidate_strategy"] == "largest":
        return sorted(candidates, key=lambda x: x[1], reverse=True)[0][0]

    # default: first acceptable
    return candidates[0][0]

def prepare_disk_for_raid(disk):
    """Make a single GPT partition for Linux RAID and return the new partition path."""
    # Wipe existing partition table
    if Path(CONFIG["paths"]["sgdisk"]).exists():
        run([CONFIG["paths"]["sgdisk"], "--zap-all", disk])
    else:
        run([CONFIG["paths"]["parted"], "-s", disk, "mklabel", "gpt"])
    # Create a single partition
    run([CONFIG["paths"]["parted"], "-s", disk, "mkpart", "primary", "0%", "100%"])
    # Set raid flag (parted’s 'raid' sets GUID to Linux RAID)
    run([CONFIG["paths"]["parted"], "-s", disk, "set", "1", "raid", "on"])
    # Partition appears as /dev/sdX1
    part = disk + "1"
    # Give kernel a moment
    time.sleep(1.0)
    return part

def add_member(raid_dev, new_dev):
    return run([CONFIG["paths"]["mdadm"], "--add", raid_dev, new_dev])

# ----------------------------
# Growing onto bigger cards
# ----------------------------
# More storage: swap one card for a bigger one (it is added and rebuilt
# like any replacement, above), wait for the mirror to be complete, then
# the other. Once every member has room beyond what the array uses, the
# array grows to the smaller of them (mdadm --grow --size=max) and the
# ext4 on it follows (resize2fs, online). Never while degraded or
# rebuilding, never smaller; a failed step is retried with a backoff.
GROW_MARGIN = 256 * 1024 * 1024  # less room than this is not worth a grow
GROW_CHECK_S = 600

def sysfs_int(path):
    try:
        with open(path) as f:
            return int(f.read().strip())
    except (OSError, ValueError):
        return None

def member_room(raid_name, dev):
    """Bytes a member can hold for the array: its size less md's data
    offset (the superblock and bitmap before the data)."""
    size = size_of(dev)
    offset = sysfs_int(f"/sys/block/{raid_name}/md/dev-{os.path.basename(dev)}/offset")
    return size - (offset * 512 if offset is not None else 256 * 1024 * 1024)

def array_room(raid_name):
    """Bytes the array uses of each member now."""
    kib = sysfs_int(f"/sys/block/{raid_name}/md/component_size")
    return kib * 1024 if kib is not None else None

def needs_grow(raid_name, members, raid_disks=2):
    """The array can grow: every slot holds an up-to-date member, and the
    smallest has more room than the array uses."""
    if len(members) != raid_disks or any("active sync" not in m["state"] for m in members.values()):
        return False
    used = array_room(raid_name)
    if not used:
        return False
    room = min(member_room(raid_name, m["device"]) for m in members.values())
    return room - used > GROW_MARGIN

def grow_array(raid_dev):
    """mdadm --grow --size=max; an mdadm that refuses with an internal
    bitmap gets it dropped for the grow and put back."""
    mdadm = CONFIG["paths"]["mdadm"]
    r = run([mdadm, "--grow", raid_dev, "--size=max"])
    if r.returncode != 0 and "bitmap" in ((r.stderr or "") + (r.stdout or "")).lower():
        run([mdadm, "--grow", raid_dev, "--bitmap=none"])
        r = run([mdadm, "--grow", raid_dev, "--size=max"])
        run([mdadm, "--grow", raid_dev, "--bitmap=internal"])
    return r

def fs_needs_resize(raid_dev):
    """The ext4 on the array is smaller than the array."""
    if run([CONFIG["paths"]["lsblk"], "-ndo", "FSTYPE", raid_dev]).stdout.strip() != "ext4":
        return False
    try:
        dev_bytes = int(run([CONFIG["paths"]["blockdev"], "--getsize64", raid_dev]).stdout.strip())
    except ValueError:
        return False
    out = run([CONFIG["paths"]["dumpe2fs"], "-h", raid_dev]).stdout
    count = re.search(r"^Block count:\s*(\d+)", out, re.M)
    bsize = re.search(r"^Block size:\s*(\d+)", out, re.M)
    if not count or not bsize:
        return False
    return dev_bytes - int(count.group(1)) * int(bsize.group(1)) > GROW_MARGIN

def resize_fs(raid_dev):
    return run([CONFIG["paths"]["resize2fs"], raid_dev])

# ----------------------------
# First-time storage bootstrap (issue #38/#39)
# ----------------------------
def _fail_bootstrap(msg):
    print(f"[raid-watch] storage setup request rejected: {msg}")

def _safe_to_wipe(dev):
    """Last line of defense before an irreversible wipe: the otc service's
    device listing already excludes the boot disk and unmounted-but-empty
    devices, but this runs as root off a request that ultimately came from
    a web form, so it re-checks independently rather than trusting that
    filtering held all the way through."""
    # Security advisory (storage setup): the path comes from the otc user,
    # so it must be one of this machine's real whole disks - not a regular
    # file (loop-mounted, and later edited offline to plant a setuid
    # binary) nor anything with odd characters (an injected fstab line).
    if not re.fullmatch(r"/dev/sd[a-z]{1,2}", dev or ""):
        _fail_bootstrap(f"{dev!r} is not a disk this device can use")
        return False
    try:
        if not stat.S_ISBLK(os.stat(dev).st_mode):
            _fail_bootstrap(f"{dev} is not a block device")
            return False
    except OSError as e:
        _fail_bootstrap(f"{dev}: {e}")
        return False
    if dev not in {d for d, _ in list_block_disks()}:
        _fail_bootstrap(f"{dev} is not one of this device's disks")
        return False
    root_base = root_base_disk()
    if root_base and base_disk(dev) == root_base:
        _fail_bootstrap(f"{dev} is the boot disk, refusing to touch it")
        return False
    mounts = run(["lsblk", "-ndo", "MOUNTPOINT", dev]).stdout.strip().splitlines()
    if any(m for m in mounts if m.strip()):
        _fail_bootstrap(f"{dev} (or a partition on it) is already mounted, refusing to wipe it")
        return False
    return True

def _append_fstab(dev, mount_point):
    # nosuid,nodev: nothing on the data disks is ever a program to run as
    # root, nor a device node.
    if "\n" in dev or "\n" in mount_point:
        raise ValueError("bad fstab entry")
    line = f"{dev}   {mount_point}   ext4   defaults,nosuid,nodev   0   0"
    fstab = Path("/etc/fstab").read_text()
    if dev not in fstab:
        with open("/etc/fstab", "a") as f:
            f.write(line + "\n")

def _run_step(cmd):
    """Like run(), but returns True/False and logs on failure — run()
    itself swallows exceptions into a returncode=1 CompletedProcess rather
    than raising, so `check=True` alone won't stop a failed bootstrap from
    barreling into its next (now unsafe) step."""
    res = run(cmd)
    if res.returncode != 0:
        _fail_bootstrap(f"`{' '.join(cmd)}` failed: {res.stderr or res.stdout}")
        return False
    return True

def perform_pending_storage_setup():
    """Reads CONFIG['setup_request_file'] (written by the web setup wizard
    via the otc service) and performs the one-time bootstrap it asks for,
    then removes the request file. Safe to call on every poll: if the file
    isn't there, or the array/mount already exists, this is a no-op. Stops
    (leaving the request file in place for inspection/retry) at the first
    failed step rather than pushing on into further destructive commands
    against a disk that's now in an unknown state. Returns False only then,
    so the caller can back off before retrying."""
    req_path = Path(CONFIG["setup_request_file"])
    if not req_path.exists():
        return True

    mount_point = CONFIG["mount_point"]
    unenc_path = os.path.join(mount_point, CONFIG["unenc_subdir"])
    raid_dev = CONFIG["raid_dev"]

    try:
        request = json.loads(req_path.read_text())
        # `or []` (not just a .get default) because a present-but-null
        # "device_paths" key — e.g. a Go nil slice marshaled to JSON `null`
        # — parses to None, which .get()'s default never catches since the
        # key *was* there.
        device_paths = request.get("device_paths") or []
    except Exception as e:
        _fail_bootstrap(f"could not parse {req_path}: {e}")
        req_path.unlink(missing_ok=True)
        return True

    if os.path.ismount(mount_point):
        print(f"[raid-watch] {mount_point} is already mounted — treating storage setup as already done.")
        req_path.unlink(missing_ok=True)
        return True

    print(f"[raid-watch] Applying storage setup request: {device_paths}")

    if len(device_paths) == 0:
        os.makedirs(unenc_path, exist_ok=True)
        print(f"[raid-watch] No disks selected — using {mount_point} on the boot disk.")

    elif len(device_paths) == 1:
        dev = device_paths[0]
        if not _safe_to_wipe(dev):
            return False
        if not _run_step([CONFIG["paths"].get("wipefs", "wipefs"), "-a", dev]):
            return False
        if not _run_step(["mkfs.ext4", "-F", dev]):
            return False
        os.makedirs(mount_point, exist_ok=True)
        if not _run_step(["mount", "-o", "nosuid,nodev", dev, mount_point]):
            return False
        os.makedirs(unenc_path, exist_ok=True)
        _append_fstab(dev, mount_point)
        print(f"[raid-watch] {dev} formatted and mounted at {mount_point} (no RAID, single disk).")

    elif len(device_paths) == 2:
        d1, d2 = device_paths
        if not _safe_to_wipe(d1) or not _safe_to_wipe(d2):
            return False
        if not _run_step([CONFIG["paths"].get("wipefs", "wipefs"), "-a", d1]):
            return False
        if not _run_step([CONFIG["paths"].get("wipefs", "wipefs"), "-a", d2]):
            return False
        if not _run_step([CONFIG["paths"]["mdadm"], "--create", "--verbose", "--run", raid_dev,
                           "--level=1", "--raid-devices=2", d1, d2]):
            return False
        if not _run_step(["mkfs.ext4", "-F", raid_dev]):
            return False
        os.makedirs(mount_point, exist_ok=True)
        if not _run_step(["mount", "-o", "nosuid,nodev", raid_dev, mount_point]):
            return False
        os.makedirs(unenc_path, exist_ok=True)
        scan = run([CONFIG["paths"]["mdadm"], "--detail", "--scan"])
        with open("/etc/mdadm/mdadm.conf", "a") as f:
            f.write(scan.stdout)
        run(["update-initramfs", "-u"])
        _append_fstab(raid_dev, mount_point)
        print(f"[raid-watch] RAID1 built on {raid_dev} from {d1}+{d2}, mounted at {mount_point}.")

    else:
        _fail_bootstrap(f"expected 0, 1, or 2 device paths, got {len(device_paths)}")
        return False

    req_path.unlink(missing_ok=True)
    return True

def raid_name_from_dev(raid_dev):
    return Path(raid_dev).name  # e.g., md0

def array_in_mdstat(mdstat_text, raid_name):
    """Whether the kernel has the array at all, active or inactive. mdstat
    leaves out an md device with no disks, which opening /dev/md0 (as
    mdadm --detail does) can create - so a sysfs or /dev check would not
    do."""
    return re.search(rf"^{re.escape(raid_name)}\s*:", mdstat_text, re.M) is not None

def request_key(path):
    """What identifies one storage-setup request: a re-submit from the
    wizard rewrites the file, which changes this."""
    try:
        st = os.stat(path)
    except OSError:
        return None
    return (st.st_mtime_ns, st.st_size)

def next_delay(prev, cap):
    """Backoff after a failure: 5 s, doubling, up to `cap`."""
    return min(max(5, 2 * prev), cap) if prev else 5

def now():
    return time.time()

# ----------------------------
# Settings > Logs > Storage
# ----------------------------
# The otc service can't read the journal, so what this script prints also
# goes, each line with the time, to a file it may read: the disks the Pi
# sees, the mirror's state, what was done about it (and why a disk was not
# used), and the kernel's own lines about USB, the disks and the mirror.
# Root writes it in a root-owned directory - never under /var/lib/otc,
# which otc owns - and keeps it small: past STORAGE_LOG_MAX it moves to .1
# and starts again (the Logs screen reads a smaller file from its start).
STORAGE_LOG = "/var/log/otc-storage/storage.log"
STORAGE_LOG_MAX = 512 * 1024

class StorageLog:
    def __init__(self, path=STORAGE_LOG, max_bytes=STORAGE_LOG_MAX, clock=None):
        self.path, self.max_bytes = path, max_bytes
        self.clock = clock or (lambda: time.strftime("%Y-%m-%dT%H:%M:%S%z"))
        self.pending = ""
        self.failed = False

    def write(self, s):
        self.pending += s
        while "\n" in self.pending:
            line, self.pending = self.pending.split("\n", 1)
            self._append(f"{self.clock()} {line}\n")
        return len(s)

    def flush(self):
        pass

    def _append(self, text):
        try:
            os.makedirs(os.path.dirname(self.path), mode=0o755, exist_ok=True)
            try:
                if os.stat(self.path).st_size + len(text) > self.max_bytes:
                    os.replace(self.path, self.path + ".1")
            except FileNotFoundError:
                pass
            fd = os.open(self.path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o644)
            try:
                os.write(fd, text.encode("utf-8", "replace"))
            finally:
                os.close(fd)
            self.failed = False
        except OSError as e:
            if not self.failed:  # once, to the journal
                self.failed = True
                sys.__stderr__.write(f"[raid-watch] storage log not written: {e}\n")

class Tee:
    """sys.stdout for main(): the journal as before, and the storage log."""
    def __init__(self, *outs):
        self.outs = outs

    def write(self, s):
        for o in self.outs:
            try:
                o.write(s)
            except Exception:
                pass
        return len(s)

    def flush(self):
        for o in self.outs:
            try:
                o.flush()
            except Exception:
                pass

def _read(path):
    try:
        return Path(path).read_text().strip()
    except Exception:
        return ""

def human_size(n):
    return f"{n / 1e9:.1f} GB" if n >= 1e9 else f"{n / 1e6:.0f} MB"

_USB_CTRL = re.compile(r"^xhci-hcd\.\d+$")
_USB_BUS = re.compile(r"^usb(\d+)$")
_USB_PORT = re.compile(r"^(\d+)-([0-9.]+)$")

def disks_seen(sys_root="/sys"):
    """The whole disks (sd*) the kernel has now, read from sysfs - no fork,
    so it can be looked at often: name, size (0 = a reader with no card),
    model, and where it is plugged in with the speed it runs at, the way
    raidwatch.locate reads it (e.g. "xhci-hcd.1 usb4 4-1 at 5000 Mb/s")."""
    out = []
    try:
        names = sorted(os.listdir(os.path.join(sys_root, "block")))
    except OSError:
        return out
    for name in names:
        if not name.startswith("sd"):
            continue
        base = os.path.join(sys_root, "block", name)
        try:
            size = int(_read(os.path.join(base, "size")) or 0) * 512
        except ValueError:
            size = 0
        model = " ".join(x for x in (_read(os.path.join(base, "device", "vendor")),
                                     _read(os.path.join(base, "device", "model"))) if x)
        where = ""
        parts = os.path.realpath(base).split("/")
        for i in range(len(parts) - 2):
            if _USB_CTRL.match(parts[i]) and _USB_BUS.match(parts[i + 1]) and _USB_PORT.match(parts[i + 2]):
                speed = _read(os.path.join(sys_root, "bus", "usb", "devices", parts[i + 2], "speed"))
                where = f"{parts[i]} {parts[i + 1]} {parts[i + 2]}" + (f" at {speed} Mb/s" if speed else "")
                break
        out.append((name, size, model, where))
    return out

def describe_disks(disks):
    if not disks:
        return "disks the Pi sees: none (no USB disk or card reader)"
    items = []
    for name, size, model, where in disks:
        what = human_size(size) if size else "no card in it"
        extra = ", ".join(x for x in (model, where) if x)
        items.append(f"{name} {what}" + (f" ({extra})" if extra else ""))
    return "disks the Pi sees: " + "; ".join(items)

def describe_array(state, members, raid_disks=2):
    if not state and not members:
        return "mirror md0: not assembled"
    slots = []
    for slot in range(max([raid_disks] + [k + 1 for k in members])):
        m = members.get(slot)
        slots.append(f"slot {slot} {m['device']} ({m['state']})" if m else f"slot {slot} empty")
    return f"mirror md0: {state or 'unknown'}; " + "; ".join(slots)

# Kernel lines worth keeping: USB devices coming and going, the disks'
# errors, the mirror's and the filesystem's own messages.
KERNEL_DISK_LINE = re.compile(
    r"\b(usb \d+-[\d.]+|usb-storage|uas|xhci|sd[a-z]+\b|sd \d+:\d+|scsi|md\d+|md/raid|raid1|"
    r"I/O error|blk_update_request|Buffer I/O|EXT4-fs)", re.I)
KERNEL_KEEP = 150

# /proc/mdstat's progress: "recovery = 12.3% (...) finish=95.2min ...".
PROGRESS_LINE = re.compile(r"(resync|recovery)\s*=\s*([\d.]+%)(?:[^\n]*?(finish=\S+))?")

def kernel_disk_lines(cursor, output=None):
    """The kernel's lines about USB, the disks and the mirror after cursor
    (with none, this boot's - the last KERNEL_KEEP), and the cursor to go on
    from. `output` stands in for journalctl's in the selftest."""
    if output is None:
        cmd = ["journalctl", "-k", "-b", "-o", "short-iso", "--no-pager", "--show-cursor"]
        if cursor:
            cmd += ["--after-cursor", cursor]
        r = run(cmd)
        if r.returncode != 0:
            return None, cursor
        output = r.stdout or ""
    lines, new = [], cursor
    for line in output.splitlines():
        if line.startswith("-- cursor: "):
            new = line[len("-- cursor: "):].strip()
            continue
        if line.startswith("-- "):
            continue
        # "2026-10-25T10:00:00+0100 host kernel: usb 2-1: USB disconnect..."
        m = re.match(r"^(\S+)\s+\S+\s+kernel:\s?(.*)$", line)
        msg = m.group(2) if m else line.strip()  # a message's further lines are indented
        if KERNEL_DISK_LINE.search(msg):
            lines.append(f"kernel {m.group(1)} {msg}" if m else f"kernel {msg}")
    if len(lines) > KERNEL_KEEP:
        lines = [f"kernel ({len(lines) - KERNEL_KEEP} earlier lines left out)"] + lines[-KERNEL_KEEP:]
    return lines, new

# ----------------------------
# LED state machine
# ----------------------------
class BlinkState:
    def __init__(self, period):
        self.period = max(0.2, float(period))
        self.next_toggle = now()
        self.on = False

    def step(self):
        t = now()
        if t >= self.next_toggle:
            self.on = not self.on
            self.next_toggle = t + self.period / 2.0

        return self.on

class LedController:
    def __init__(self, blink_period):
        self.blink_period = blink_period
        self.blinkers = {0: BlinkState(blink_period), 1: BlinkState(blink_period)}
        # Current desired modes: "solid_green", "solid_red", "blink_green", "blink_red", "off"
        self.modes = {0: "off", 1: "off"}

    def set_mode(self, slot, mode):
        self.modes[slot] = mode

    def apply(self):
        for slot, mode in self.modes.items():
            if mode == "solid_green":
                led_set(slot, red_on=False, green_on=True)
            elif mode == "solid_red":
                led_set(slot, red_on=True, green_on=False)
            elif mode == "blink_green":
                on = self.blinkers[slot].step()
                led_set(slot, red_on=False, green_on=on)
            elif mode == "blink_red":
                on = self.blinkers[slot].step()
                led_set(slot, red_on=on, green_on=False)
            else:
                led_set(slot, red_on=False, green_on=False)

# ----------------------------
# Core logic
# ----------------------------
def main():
    raid_dev = CONFIG["raid_dev"]
    raid_name = raid_name_from_dev(raid_dev)

    # Everything printed also goes to Settings > Logs > Storage.
    sys.stdout = Tee(sys.stdout, StorageLog())

    print(f"[raid-watch] Monitoring {raid_dev} (name: {raid_name})")
    gpio_setup()

    led = LedController(CONFIG["blink_period_s"])

    def handle_sigterm(sig, frame):
        print("[raid-watch] SIGTERM received. Cleaning up GPIO.")
        gpio_cleanup()
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, handle_sigterm)
    signal.signal(signal.SIGINT, handle_sigterm)

    changed = [True]
    last_summary = [None]

    def log(*args):
        if changed[0]:
            print(*args)

    # Failures are retried with a backoff rather than on every poll: a
    # setup step or an `mdadm --add` that fails (a replacement card a
    # little too small, a faulty disk md still holds) used to fork and log
    # twice a second for as long as the state lasted.
    # (disk, size) -> {"next_try": monotonic, "delay": s, "err": stderr}
    failed_adds = {}
    setup_retry = {"key": None, "next_try": 0.0, "delay": 0}
    # Growing onto bigger cards, with its own backoff per step.
    # Looked at every GROW_CHECK_S only: it reads the disks (lsblk,
    # dumpe2fs), and a healthy array's disks are left to rest.
    grow_retry = {"check": 0.0, "array": 0.0, "array_delay": 0, "fs": 0.0, "fs_delay": 0}
    # The storage log: the disks seen (looked at every DISKS_EVERY_S), the
    # kernel's lines about them (every KERNEL_EVERY_S, and at once when the
    # disks or the mirror change), why a disk wasn't added, and how far a
    # rebuild has got (every PROGRESS_EVERY_S).
    DISKS_EVERY_S, KERNEL_EVERY_S, PROGRESS_EVERY_S = 5, 30, 600
    seen = {"disks": None, "disks_at": 0.0, "kernel": None, "kernel_at": 0.0,
            "kernel_failed": False, "why": {}, "progress_at": 0.0}

    def kernel_lines():
        lines, seen["kernel"] = kernel_disk_lines(seen["kernel"])
        seen["kernel_at"] = time.monotonic()
        if lines is None:
            if not seen["kernel_failed"]:
                seen["kernel_failed"] = True
                print("[raid-watch] the kernel's log can't be read (journalctl)")
            return
        for line in lines:
            print(line)

    kernel_lines()

    try:
        while True:
            key = request_key(CONFIG["setup_request_file"])
            if key is not None and not (key == setup_retry["key"] and time.monotonic() < setup_retry["next_try"]):
                if perform_pending_storage_setup():
                    setup_retry.update(key=None, delay=0)
                else:
                    delay = next_delay(setup_retry["delay"] if key == setup_retry["key"] else 0, 3600)
                    setup_retry.update(key=key, next_try=time.monotonic() + delay, delay=delay)

            # Read first: no array at all (OTC_SKIP_RAID, a single disk, the
            # SD card only) means no mdadm fork every poll, and nothing to
            # repair - `mdadm --add` to a missing md0 can only fail. Read
            # after the setup above, so an array it just created counts.
            mdst = mdstat()
            array_present = array_in_mdstat(mdst, raid_name)
            # The fallback is what a failed `mdadm --detail` parses to.
            detail = mdadm_detail(raid_dev) if array_present else {"state": "", "members": {}, "other_devices": set()}
            state = (detail.get("state") or "").lower()
            members = detail.get("members", {})
            # Log only when the picture changes: this runs every poll_s,
            # and printing every time filled the journal on the SD card.
            summary = (state, tuple(sorted((k, v.get("state")) for k, v in members.items())))
            changed[0] = summary != last_summary[0]
            last_summary[0] = summary
            log(describe_array(state, members) if array_present else "mirror md0: not assembled")

            mono = time.monotonic()
            disks_changed = False
            if mono - seen["disks_at"] >= DISKS_EVERY_S or changed[0]:
                seen["disks_at"] = mono
                cur = disks_seen()
                if cur != seen["disks"]:
                    disks_changed = True
                    seen["disks"] = cur
                    print(describe_disks(cur))
            if changed[0] or disks_changed or mono - seen["kernel_at"] >= KERNEL_EVERY_S:
                kernel_lines()

            # Default: turn off until we decide
            led.set_mode(0, "off")
            led.set_mode(1, "off")

            # Determine per-slot status
            # Slots that exist in array definition
            present_slots = sorted(members.keys())

            # Detect resync/recovery via /proc/mdstat (read above)
            rebuilding = array_is_resyncing(mdst, raid_name)

            # Identify source vs target during rebuild:
            # mdadm --detail marks the rebuilding device as "(rebuilding)" or "spare rebuilding"
            rebuilding_slot = None
            for slot, m in members.items():
                if "rebuild" in m["state"]:
                    rebuilding_slot = slot
                    break

            # If we want to also confirm direction: compare Events counters.
            # Only while rebuilding - the only time it's used. `mdadm
            # --examine` reads each drive's superblock straight off the
            # device; doing it every poll kept both USB pen drives busy
            # around the clock, so they never idled and ran hot.
            events_by_slot = {}
            if rebuilding:
                for slot, m in members.items():
                    # Use the underlying member device (/dev/sdXN)
                    ex = mdadm_examine(m["device"])
                    events_by_slot[slot] = ex.get("events", 0)


            # Choose source as the slot with highest Events
            source_slot = None
            if events_by_slot:
                source_slot = max(events_by_slot, key=lambda s: events_by_slot[s])

            # LED logic
            if "degraded" in state or len(members) < 2:
                # Some member missing/failed
                for slot in (0, 1):
                    if slot not in members:
                        # Missing member -> solid red
                        led.set_mode(slot, "solid_red")
                    else:
                        # The surviving member: green unless rebuilding
                        if rebuilding and rebuilding_slot == slot:
                            # If the only member is somehow "rebuilding" (rare), blink red
                            led.set_mode(slot, "blink_red")
                        else:
                            led.set_mode(slot, "solid_green")
            else:
                # Two members present
                if rebuilding and rebuilding_slot is not None:
                    # Target (rebuilding) -> blink red
                    led.set_mode(rebuilding_slot, "blink_red")
                    # Source (newer events) -> blink green
                    src = source_slot if source_slot is not None else (0 if rebuilding_slot == 1 else 1)
                    led.set_mode(src, "blink_green")
                else:
                    # Healthy + in-sync
                    # mdadm reports "clean", "active", etc. Without rebuild markers.
                    led.set_mode(0, "solid_green")
                    led.set_mode(1, "solid_green")

            # Apply LED states (updates blinkers)
            led.apply()

            # Auto-repair path
            if CONFIG["auto_repair_enable"] and array_present:
                # If degraded and exactly one member present, try to find a candidate
                if ("degraded" in state) or (len(members) < 2):
                    # Determine size of existing member to filter candidates
                    sizes = []
                    for m in members.values():
                        sizes.append(size_of(m["device"]))
                    existing_size = max(sizes) if sizes else 0
                    disks = list_block_disks()
                    # A disk that was pulled (gone, or size 0) ends its
                    # backoff: put back, it is tried at once.
                    present = {(d, sz) for d, sz in disks if sz}
                    for k in [k for k in failed_adds if k not in present]:
                        del failed_adds[k]
                    mono = time.monotonic()
                    waiting = {k for k, v in failed_adds.items() if mono < v["next_try"]}
                    why = {}
                    candidate = pick_candidate_disk(existing_size, detail, skip=waiting, disks=disks, why=why)
                    if why != seen["why"]:
                        for d, reason in sorted(why.items()):
                            if seen["why"].get(d) != reason:
                                print(f"[raid-watch] {d} is not added to the mirror: {reason}")
                        seen["why"] = why
                    if candidate:
                        add_key = (candidate, dict(disks).get(candidate, 0))
                        retrying = add_key in failed_adds
                        if not retrying:
                            print(f"[raid-watch] Candidate new disk detected: {candidate}")
                        to_add = candidate
                        if CONFIG["auto_add_mode"] == "partition":
                            to_add = prepare_disk_for_raid(candidate)
                        if not retrying:
                            print(f"[raid-watch] Adding {to_add} to {raid_dev} ...")
                        r = add_member(raid_dev, to_add)
                        if r.returncode != 0:
                            prev = failed_adds.get(add_key)
                            delay = next_delay(prev["delay"] if prev else 0, 600)
                            if prev is None or prev["err"] != r.stderr:
                                print(f"[raid-watch] mdadm --add failed: {(r.stderr or '').strip()} (will retry in {delay}s)")
                            failed_adds[add_key] = {"next_try": time.monotonic() + delay, "delay": delay, "err": r.stderr}
                        else:
                            failed_adds.pop(add_key, None)
                            print("[raid-watch] Member added, rebuild should start automatically.")
                else:
                    failed_adds.clear()
                    seen["why"] = {}
            else:
                failed_adds.clear()

            if rebuilding and mono - seen["progress_at"] >= PROGRESS_EVERY_S:
                seen["progress_at"] = mono
                m = PROGRESS_LINE.search(mdst)
                if m:
                    print(f"[raid-watch] {m.group(1)}: {m.group(2)}" + (f", {m.group(3)}" if m.group(3) else ""))
            elif not rebuilding:
                seen["progress_at"] = 0.0

            # Bigger cards: grow the array, then its filesystem (above
            # GROW_MARGIN). Only once the mirror is complete and settled.
            mono = time.monotonic()
            if array_present and "degraded" not in state and not rebuilding and mono >= grow_retry["check"]:
                grow_retry["check"] = mono + GROW_CHECK_S
                if mono >= grow_retry["array"] and needs_grow(raid_name, members):
                    print(f"[raid-watch] Every disk has room to spare: growing {raid_dev}")
                    r = grow_array(raid_dev)
                    if r.returncode != 0:
                        grow_retry["array_delay"] = next_delay(grow_retry["array_delay"], 3600)
                        grow_retry["array"] = mono + grow_retry["array_delay"]
                        print(f"[raid-watch] mdadm --grow failed: {(r.stderr or '').strip()} (will retry in {grow_retry['array_delay']}s)")
                    else:
                        grow_retry["array_delay"] = 0
                        print(f"[raid-watch] {raid_dev} grown; the new space is being mirrored")
                if mono >= grow_retry["fs"] and fs_needs_resize(raid_dev):
                    print(f"[raid-watch] Growing the filesystem on {raid_dev}")
                    r = resize_fs(raid_dev)
                    if r.returncode != 0:
                        grow_retry["fs_delay"] = next_delay(grow_retry["fs_delay"], 3600)
                        grow_retry["fs"] = mono + grow_retry["fs_delay"]
                        print(f"[raid-watch] resize2fs failed: {(r.stderr or '').strip()} (will retry in {grow_retry['fs_delay']}s)")
                    else:
                        grow_retry["fs_delay"] = 0
                        print(f"[raid-watch] Filesystem on {raid_dev} grown")

            time.sleep(CONFIG["poll_s"])

    finally:
        gpio_cleanup()

def selftest():
    """python3 raid_watch.py --selftest: the storage log's helpers, with no
    root, mdadm or Pi needed."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        # Lines get the time, a partial line waits, the file moves to .1
        # past its size, and a symlink in its place is refused.
        log_path = os.path.join(tmp, "log", "storage.log")
        sl = StorageLog(log_path, max_bytes=60, clock=lambda: "T")
        sl.write("one\ntw")
        assert Path(log_path).read_text() == "T one\n", Path(log_path).read_text()
        sl.write("o\n")
        assert Path(log_path).read_text() == "T one\nT two\n"
        sl.write("x" * 50 + "\n")
        assert Path(log_path + ".1").read_text() == "T one\nT two\n"
        assert Path(log_path).read_text() == "T " + "x" * 50 + "\n"
        os.remove(log_path)
        os.symlink(os.path.join(tmp, "elsewhere"), log_path)
        sys_stderr, sys.__stderr__ = sys.__stderr__, open(os.devnull, "w")
        try:
            sl.write("never\n")
        finally:
            sys.__stderr__.close()
            sys.__stderr__ = sys_stderr
        assert not os.path.exists(os.path.join(tmp, "elsewhere")) and sl.failed

        # The disks a Pi 5 shows: a card in the controller-1 blue port at
        # USB 3, an empty reader at USB 2, and a disk that isn't USB.
        sys_root = os.path.join(tmp, "sys")
        def disk(name, size, path, vendor="Generic", model="Card Reader"):
            dev = os.path.join(sys_root, path, name)
            os.makedirs(os.path.join(dev, "device"))
            Path(dev, "size").write_text(f"{size}\n")
            Path(dev, "device", "vendor").write_text(vendor + "  \n")
            Path(dev, "device", "model").write_text(model + "\n")
            os.makedirs(os.path.join(sys_root, "block"), exist_ok=True)
            os.symlink(dev, os.path.join(sys_root, "block", name))
        usb = "devices/platform/axi/1000120000.pcie/1f00300000.usb/xhci-hcd.1/usb4/4-1/4-1:1.0/host0/target0:0:0/0:0:0:0/block"
        disk("sda", 1000215216, usb)
        os.makedirs(os.path.join(sys_root, "bus/usb/devices/4-1"))
        Path(sys_root, "bus/usb/devices/4-1/speed").write_text("5000\n")
        disk("sdb", 0, usb.replace("xhci-hcd.1/usb4/4-1/4-1:1.0", "xhci-hcd.0/usb1/1-2/1-2:1.0"))
        os.makedirs(os.path.join(sys_root, "bus/usb/devices/1-2"))
        Path(sys_root, "bus/usb/devices/1-2/speed").write_text("480\n")
        disk("sdc", 2048, "devices/platform/sata/host1/block", vendor="", model="Disk")
        os.makedirs(os.path.join(sys_root, "block", "mmcblk0"))
        seen = disks_seen(sys_root)
        assert [d[0] for d in seen] == ["sda", "sdb", "sdc"], seen
        assert seen[0] == ("sda", 1000215216 * 512, "Generic Card Reader", "xhci-hcd.1 usb4 4-1 at 5000 Mb/s"), seen[0]
        assert seen[1][1] == 0 and seen[1][3] == "xhci-hcd.0 usb1 1-2 at 480 Mb/s", seen[1]
        assert seen[2][2:] == ("Disk", ""), seen[2]
        said = describe_disks(seen)
        assert said == ("disks the Pi sees: sda 512.1 GB (Generic Card Reader, xhci-hcd.1 usb4 4-1 at 5000 Mb/s); "
                        "sdb no card in it (Generic Card Reader, xhci-hcd.0 usb1 1-2 at 480 Mb/s); sdc 1 MB (Disk)"), said
        assert describe_disks([]).endswith("none (no USB disk or card reader)")

    # The mirror in one line, a missing slot included.
    said = describe_array("clean, degraded", {0: {"device": "/dev/sda", "state": "active sync"}})
    assert said == "mirror md0: clean, degraded; slot 0 /dev/sda (active sync); slot 1 empty", said

    # The kernel's lines: only the disks', with their time, and the cursor.
    out = "\n".join([
        "2026-10-25T10:00:00+0100 otc kernel: usb 4-1: USB disconnect, device number 2",
        "2026-10-25T10:00:01+0100 otc kernel: md/raid1:md0: Disk failure on sdb, disabling device.",
        "                                         md/raid1:md0: Operation continuing on 1 devices.",
        "2026-10-25T10:00:02+0100 otc kernel: brcmfmac: power management disabled",
        "2026-10-25T10:00:03+0100 otc kernel: I/O error, dev sda, sector 2048 op 0x1:(WRITE)",
        "-- No entries --",
        "-- cursor: s=abc;i=42",
    ])
    lines, cur = kernel_disk_lines(None, output=out)
    assert cur == "s=abc;i=42", cur
    assert lines == [
        "kernel 2026-10-25T10:00:00+0100 usb 4-1: USB disconnect, device number 2",
        "kernel 2026-10-25T10:00:01+0100 md/raid1:md0: Disk failure on sdb, disabling device.",
        "kernel md/raid1:md0: Operation continuing on 1 devices.",
        "kernel 2026-10-25T10:00:03+0100 I/O error, dev sda, sector 2048 op 0x1:(WRITE)",
    ], lines
    many = "\n".join(f"2026-10-25T10:00:00+0100 otc kernel: usb 1-{i}: new device" for i in range(KERNEL_KEEP + 5))
    lines, _ = kernel_disk_lines("s=old", output=many)
    assert len(lines) == KERNEL_KEEP + 1 and lines[0] == "kernel (5 earlier lines left out)", lines[:2]

    # Rebuild progress from /proc/mdstat.
    m = PROGRESS_LINE.search("      [>....]  recovery =  0.0% (9600/125762560) finish=871.8min speed=2400K/sec\n")
    assert (m.group(1), m.group(2), m.group(3)) == ("recovery", "0.0%", "finish=871.8min"), m.groups()
    m = PROGRESS_LINE.search("      resync=DELAYED\n      [=>...]  resync = 7.5% (1/2)\n")
    assert (m.group(2), m.group(3)) == ("7.5%", None), m.groups()

    # Why a disk wasn't taken: no card, too small; the mirror's own isn't listed.
    why = {}
    pick = pick_candidate_disk(512_000_000_000, {"members": {0: {"device": "/dev/sda", "state": "active sync"}}},
                               disks=[("/dev/sda", 512_000_000_000), ("/dev/sdb", 0), ("/dev/sdc", 128_000_000_000)], why=why)
    assert pick is None, pick
    assert why == {"/dev/sdb": "no card in it",
                   "/dev/sdc": "too small: 128.0 GB, and the working disk is 512.0 GB"}, why
    print("selftest ok")

if __name__ == "__main__":
    if "--selftest" in sys.argv:
        selftest()
    else:
        main()
