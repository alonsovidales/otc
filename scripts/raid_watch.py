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
  * When degraded and a disk appears that is blank (no partition table, no
    filesystem or other signature, first and last MiB empty) or was a member
    of this very mirror, mdadm --add it to /dev/md0 to start the rebuild. Any
    other disk is left alone: it is logged once and the device raises an
    Alert (raidwatch, from /run/otc-raid-watch/not-added.json).

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

    # Auto-repair: when degraded and a disk of similar size appears, add it
    # to md0 (which overwrites it) - only a blank disk, or one that was a
    # member of md0 itself (see judge_disk). A plugged-in backup drive is
    # never taken: Raspberry Pi OS Lite mounts nothing by itself, so an
    # unmounted disk says nothing about whether it holds data.
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
        "blkid": "/sbin/blkid",
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

# mdadm --detail is a fork every poll (0.5 s) for the device's whole life.
# What it reports comes from md's sysfs, which costs no fork to read: the
# poll reads that, and runs mdadm only when it changed, or every
# MDADM_EVERY_S whatever happens.
MDADM_EVERY_S = 30
# array_state flips between these with every write ("clean"/"active", as
# mdadm's State does - see array_condition): one running array to the
# fingerprint, or every write burst would fork mdadm again.
_RUNNING_STATES = ("clean", "active", "write-pending", "active-idle")

def md_fingerprint(raid_name, sys_root="/sys"):
    """What of /sys/block/<md>/md mdadm --detail's answer depends on: the
    array's state (running or not), degraded, sync_action, raid_disks, and
    each member's name, slot and state. None when it can't be read (then
    mdadm is asked every poll, as before)."""
    base = os.path.join(sys_root, "block", raid_name, "md")
    try:
        names = sorted(e for e in os.listdir(base) if e.startswith("dev-"))
    except OSError:
        return None
    state = _read(os.path.join(base, "array_state"))
    parts = ["running" if state in _RUNNING_STATES else state]
    parts += [_read(os.path.join(base, f)) for f in ("degraded", "sync_action", "raid_disks")]
    for name in names:
        parts.append(f"{name}:{_read(os.path.join(base, name, 'slot'))}:{_read(os.path.join(base, name, 'state'))}")
    return tuple(parts)

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

def pick_candidate_disk(existing_member_size, detail, skip=frozenset(), disks=None, why=None, judge=None, refused=None):
    """Pick a disk not in md array, large enough, and not the root disk.
    `skip` holds (disk, size) pairs whose last add failed and are waiting
    out their backoff - filtered here, so one of those can't hide another,
    valid disk. `disks` is list_block_disks() when the caller has it.
    `why`, a dict, gets each disk left out that isn't the mirror's own, with
    the reason in words, for the storage log. `judge(disk, size)` says
    whether a disk that passed every other check may be added (judge_disk's
    ("add"|"refuse", why)); without one, none is. `refused`, a list, gets
    the disks the judge refused."""
    if why is None:
        why = {}
    if judge is None:
        judge = lambda d, sz: ("refuse", "it wasn't checked")
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

        # Never a disk that may hold something: only a blank one, or one
        # that was this mirror's (judge_disk).
        verdict, reason = judge(d, sz)
        if verdict != "add":
            why[d] = (f"{reason} - it was left alone. To use it for the mirror, erase it on a computer "
                      "(its partitions too) and plug it in again, or plug in a blank disk")
            if refused is not None:
                refused.append(d)
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
# Which disk may be added (and wiped)
# ----------------------------
# A disk is added to the degraded mirror by itself only when it is blank -
# no partition table, no filesystem or other signature (blkid -p, wipefs),
# and its first and last MiB read back empty - or when it was a member of
# this very mirror (its md superblock carries md0's UUID) and hasn't run
# without the disk the mirror runs on now (a split copy holds changes the
# mirror lacks: it is left for the owner). The same rule as docker/mirror's
# otc-mirror, which also never takes a disk that can't be read; "empty" is
# all zero bytes, or all 0xFF (an erased flash card can read either way).
# Anything else - a plugged-in backup drive, a card fresh from the shop with
# its filesystem, another array's disk - is left alone, logged once, and
# reported in NOT_ADDED_FILE for the device's Alert.
ENDS_SPAN = 1 << 20

def tool(name):
    """The command `name`: CONFIG's path when it is there, else from PATH."""
    path = CONFIG["paths"].get(name)
    if path and os.path.exists(path):
        return path
    import shutil
    return shutil.which(name) or path or name

def kv_lines(text):
    """KEY=VALUE lines (blkid -o export, mdadm --export) as a dict."""
    out = {}
    for line in (text or "").splitlines():
        k, sep, v = line.strip().partition("=")
        if sep:
            out[k] = v
    return out

def has_partitions(dev, sys_root="/sys"):
    """The kernel found a partition table on the disk (sdb1... in sysfs)."""
    name = os.path.basename(dev)
    try:
        return any(e.startswith(name) and e != name for e in os.listdir(os.path.join(sys_root, "block", name)))
    except OSError:
        return False

def signatures(dev):
    """What blkid -p and wipefs find on dev: "" for nothing, else a few
    words; None when they can't read it."""
    r = run([tool("blkid"), "-p", "-o", "export", dev])
    if r.returncode == 0:
        found = kv_lines(r.stdout)
        found.pop("DEVNAME", None)
        parts = []
        if found.get("PTTYPE"):
            parts.append(f"a {found['PTTYPE']} partition table")
        if found.get("TYPE"):
            parts.append(found["TYPE"] + (f" labelled {found['LABEL']!r}" if found.get("LABEL") else ""))
        return " and ".join(parts) or "signatures blkid can't tell apart"
    if r.returncode == 8:  # several signatures
        return "several signatures"
    if r.returncode != 2:  # 2: nothing found
        return None
    r = run([tool("wipefs"), "--no-act", "--noheadings", dev])
    if r.returncode != 0:
        return None
    first = (r.stdout or "").strip().splitlines()
    return f"signatures wipefs lists ({' '.join(first[0].split())})" if first else ""

def read_ends(dev):
    """dev's first and last MiB, read straight from the disk (O_DIRECT, so
    a read error isn't hidden by the page cache) and only when nothing holds
    it (O_EXCL: no mount, no array, no volume). Returns (first, last) or
    raises OSError."""
    import mmap
    flags = os.O_RDONLY | os.O_EXCL | getattr(os, "O_DIRECT", 0) | getattr(os, "O_CLOEXEC", 0)
    fd = os.open(dev, flags)
    try:
        size = os.lseek(fd, 0, os.SEEK_END)
        ends = []
        for at in (0, max(0, (size - ENDS_SPAN) & ~4095)):
            buf = mmap.mmap(-1, ENDS_SPAN)  # page-aligned, as O_DIRECT wants
            try:
                n = os.preadv(fd, [buf], at)
                ends.append(bytes(buf[:n]))
            finally:
                buf.close()
        return ends[0], ends[1]
    finally:
        os.close(fd)

def uniform(data):
    """No data in it: all zero bytes, or all 0xFF."""
    return bool(data) and (data.count(0) == len(data) or data.count(0xFF) == len(data))

ARRAY_STATE = re.compile(r"^\s*Array State\s*:\s*([A-Za-z.]+)", re.M)

def diverged(dev, members):
    """The disk's own superblock says it last ran without a disk the mirror
    runs on now (docker/mirror's diverged): each copy then holds changes the
    other lacks. True/False, or None when the superblock can't be read. A
    disk that only dropped out while the other went on still shows both
    slots active: it is behind, and a re-add catches it up."""
    r = run([CONFIG["paths"]["mdadm"], "--examine", dev])
    m = ARRAY_STATE.search(r.stdout or "") if r.returncode == 0 else None
    if not m:
        return None
    state = m.group(1)
    for slot, member in members.items():
        if "active sync" not in member.get("state", "") and "in_sync" not in member.get("state", ""):
            continue
        if slot >= len(state) or state[slot] == ".":
            return True
    return False

def judge_disk(dev, array_uuid, members):
    """Whether dev may be added to the degraded mirror: ("add", why) for a
    blank disk or a former member of this mirror, ("refuse", why) for any
    other, why being words for the storage log."""
    r = run([CONFIG["paths"]["mdadm"], "--examine", "--export", dev])
    sb = kv_lines(r.stdout) if r.returncode == 0 else {}
    if sb.get("MD_UUID"):
        if array_uuid and sb["MD_UUID"].lower() == array_uuid.lower():
            split = diverged(dev, members)
            if split is None:
                return "refuse", "it was a member of this mirror, but its superblock can't be read"
            if split:
                return "refuse", ("it was a member of this mirror but ran on its own since, so it holds changes "
                                  "the mirror lacks (copy what you need off it first)")
            return "add", "it was a member of this mirror"
        return "refuse", "it holds part of another disk array"
    if r.returncode != 0 and "No md superblock" not in (r.stderr or "") + (r.stdout or ""):
        return "refuse", "it can't be read (mdadm --examine failed)"
    if has_partitions(dev):
        return "refuse", "it has partitions"
    found = signatures(dev)
    if found is None:
        return "refuse", "it can't be read (blkid/wipefs failed)"
    if found:
        return "refuse", f"it holds {found}"
    try:
        first, last = read_ends(dev)
    except OSError as e:
        return "refuse", f"it can't be read ({e.strerror or e})"
    if not (uniform(first) and uniform(last)):
        return "refuse", "it holds data (no filesystem this device knows)"
    return "add", "it is blank"

def array_uuid_of(raid_dev):
    """md0's UUID (mdadm --detail --export), or "" when it can't be read."""
    r = run([CONFIG["paths"]["mdadm"], "--detail", "--export", raid_dev])
    return kv_lines(r.stdout).get("MD_UUID", "") if r.returncode == 0 else ""

# The disks left alone, for the device (raidwatch reads it every minute
# and raises one Alert per disk plugged in): root's directory on the /run
# tmpfs, which the service may read and nothing but root may write.
NOT_ADDED_FILE = "/run/otc-raid-watch/not-added.json"
_not_added_written = {}

def write_not_added(entries, path=None):
    """Writes the disks left alone ([{"id", "disk"}], [] for none) when that
    changed: a temp file beside it, renamed over it."""
    path = path or NOT_ADDED_FILE
    data = json.dumps({"disks": entries}, sort_keys=True)
    if _not_added_written.get(path) == data:
        return
    try:
        with open(path, "rb") as f:
            if f.read() == data.encode():
                _not_added_written[path] = data
                return
    except OSError:
        if not entries:  # nothing to say, and nothing said before
            _not_added_written[path] = data
            return
    try:
        os.makedirs(os.path.dirname(path), mode=0o755, exist_ok=True)
        tmp = f"{path}.tmp.{os.getpid()}"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
        try:
            os.write(fd, data.encode())
        finally:
            os.close(fd)
        os.replace(tmp, path)
        _not_added_written[path] = data
    except OSError as e:
        # Once per content: the next change tries again.
        if _not_added_written.get(path) != ("failed", data):
            print(f"[raid-watch] could not write {path}: {e}")
        _not_added_written[path] = ("failed", data)

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
    # Only a disk with nothing on it: the setup's storage step never said
    # the disks it lists are erased, so one holding a filesystem, a
    # partition table or part of an array (a backup drive, a card fresh from
    # the shop) is refused rather than wiped. The same checks as a disk
    # added to a degraded mirror (judge_disk), less the empty ends.
    r = run([CONFIG["paths"]["mdadm"], "--examine", "--export", dev])
    if r.returncode == 0 and kv_lines(r.stdout).get("MD_UUID"):
        _fail_bootstrap(f"{dev} holds part of a disk array, refusing to wipe it")
        return False
    if has_partitions(dev):
        _fail_bootstrap(f"{dev} has partitions, refusing to wipe it: erase it on a computer first, or choose a blank disk")
        return False
    found = signatures(dev)
    if found is None:
        _fail_bootstrap(f"{dev} can't be read (blkid/wipefs failed)")
        return False
    if found:
        _fail_bootstrap(f"{dev} holds {found}, refusing to wipe it: erase it on a computer first, or choose a blank disk")
        return False
    return True

def _storage_holds_data(mount_point):
    """What the storage directory already holds that a new disk mounted
    over it would hide (its first such entry), or "". Hidden names are the
    service's own bookkeeping, made at its first start; an empty
    unencrypted/ and lost+found hold nothing."""
    try:
        entries = sorted(os.listdir(mount_point))
    except FileNotFoundError:
        return ""
    except OSError as e:
        return f"{mount_point} (it can't be listed: {e.strerror or e})"
    unenc = CONFIG["unenc_subdir"]
    for name in entries:
        if name.startswith(".") or name == "lost+found":
            continue
        path = os.path.join(mount_point, name)
        if name == unenc and os.path.isdir(path) and not os.path.islink(path):
            try:
                if not os.listdir(path):
                    continue
            except OSError:
                pass
        return path
    return ""

def _fstab_has(mount_point, fstab="/etc/fstab"):
    """A line of fstab (not a comment) mounts something on mount_point."""
    try:
        lines = Path(fstab).read_text().splitlines()
    except OSError:
        return False
    for line in lines:
        fields = line.split()
        if fields and not fields[0].startswith("#") and len(fields) >= 2 and fields[1] == mount_point:
            return True
    return False

def _fs_uuid(dev):
    r = run([tool("blkid"), "-s", "UUID", "-o", "value", dev])
    uuid = (r.stdout or "").strip()
    return uuid if r.returncode == 0 and re.fullmatch(r"[0-9A-Fa-f-]{8,64}", uuid) else ""

# What install.sh writes: nofail, so a disk missing or slow at boot no
# longer stops the boot in emergency mode, and the drop-ins that keep the
# service and MariaDB from starting on the empty directory under the mount
# point (nofail no longer orders the mount before local-fs.target).
STORAGE_MOUNT_OPTS = "defaults,nosuid,nodev,nofail,x-systemd.device-timeout=3min"
STORAGE_DROPINS = ("otc", "mariadb")

def _add_fstab(dev, mount_point, fstab="/etc/fstab"):
    """Adds the storage's line - by its filesystem's UUID, which stays with
    the disk whatever sdX it gets - written beside fstab, flushed and
    renamed over it. False when the line can't be made or written."""
    uuid = _fs_uuid(dev)
    if not uuid or "\n" in mount_point:
        _fail_bootstrap(f"could not read the new filesystem's UUID on {dev}")
        return False
    try:
        text = Path(fstab).read_text()
        if text and not text.endswith("\n"):
            text += "\n"
        text += f"UUID={uuid}   {mount_point}   ext4   {STORAGE_MOUNT_OPTS}   0   0\n"
        tmp = f"{fstab}.otc.{os.getpid()}"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
        try:
            os.write(fd, text.encode())
            os.fsync(fd)
        finally:
            os.close(fd)
        os.replace(tmp, fstab)
    except OSError as e:
        _fail_bootstrap(f"could not write {fstab}: {e}")
        return False
    for unit in STORAGE_DROPINS:
        d = f"/etc/systemd/system/{unit}.service.d"
        try:
            os.makedirs(d, mode=0o755, exist_ok=True)
            with open(os.path.join(d, "otc-storage.conf"), "w") as f:
                f.write("# Off The Cloud: start only with the storage mounted (fstab has nofail).\n"
                        f"[Unit]\nRequiresMountsFor={mount_point}\n")
        except OSError as e:
            print(f"[raid-watch] could not write the {unit} storage drop-in: {e}")
    run(["systemctl", "daemon-reload"])
    return True

def _own_storage(mount_point, unenc_path):
    """The new filesystem's root and unencrypted/ as install.sh leaves
    them: the service's (a new filesystem's root is root's, and uploads
    failed), 0750, the root also o+x (MariaDB passes through it when its
    datadir is on the storage)."""
    import pwd
    try:
        otc = pwd.getpwnam("otc")
    except KeyError:
        print("[raid-watch] no otc user: the new storage keeps root's ownership")
        return
    os.makedirs(unenc_path, exist_ok=True)
    for path, mode in ((mount_point, 0o751), (unenc_path, 0o750)):
        os.lchown(path, otc.pw_uid, otc.pw_gid)
        os.chmod(path, mode)

# The largest request the service writes (storage.RequestSetup).
MAX_REQUEST_BYTES = 4096

def read_setup_request(path):
    """The storage setup request at path as a dict, or None. Opened without
    following a symlink and without blocking on a FIFO, and only a regular
    file of at most MAX_REQUEST_BYTES is read: /var/lib/otc is the otc
    user's directory, and this runs as root."""
    try:
        fd = os.open(str(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    except OSError:
        return None
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > MAX_REQUEST_BYTES:
            return None
        data = os.read(fd, MAX_REQUEST_BYTES + 1)
        if len(data) > MAX_REQUEST_BYTES:
            return None
        request = json.loads(data.decode("utf-8"))
        return request if isinstance(request, dict) else None
    except (OSError, ValueError):
        return None
    finally:
        os.close(fd)

def drop_setup_request(path):
    """Removes the request, whatever was put there: a directory too
    (removed without following links), so it can't stop this loop."""
    import shutil
    try:
        if os.path.isdir(str(path)) and not os.path.islink(str(path)):
            shutil.rmtree(str(path), ignore_errors=True)
        else:
            os.unlink(str(path))
    except FileNotFoundError:
        pass
    except OSError as e:
        print(f"[raid-watch] could not remove the storage setup request: {type(e).__name__}")

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
    so the caller can back off before retrying. A request refused before
    anything was touched (the storage already holds files, a disk holds
    something) is removed: asking again changes nothing."""
    req_path = Path(CONFIG["setup_request_file"])
    if not os.path.lexists(str(req_path)):
        return True

    mount_point = CONFIG["mount_point"]
    unenc_path = os.path.join(mount_point, CONFIG["unenc_subdir"])
    raid_dev = CONFIG["raid_dev"]

    request = read_setup_request(req_path)
    if request is None:
        _fail_bootstrap(f"could not read {req_path} (not a small regular file of JSON)")
        drop_setup_request(req_path)
        return True
    # `or []` (not just a .get default) because a present-but-null
    # "device_paths" key — e.g. a Go nil slice marshaled to JSON `null`
    # — parses to None, which .get()'s default never catches since the
    # key *was* there.
    device_paths = request.get("device_paths") or []
    if not isinstance(device_paths, list) or not all(isinstance(d, str) for d in device_paths):
        _fail_bootstrap(f"{req_path} does not list device paths")
        drop_setup_request(req_path)
        return True

    if os.path.ismount(mount_point):
        print(f"[raid-watch] {mount_point} is already mounted — treating storage setup as already done.")
        drop_setup_request(req_path)
        return True

    if device_paths:
        # A disk mounted over the storage directory hides what is in it:
        # only a device that has stored nothing there yet (the setup's
        # first sign-in) gets new storage. And never over storage fstab
        # already names but that isn't mounted now (a disk missing): that
        # is this device's storage, not a place for new disks.
        held = _storage_holds_data(mount_point)
        if held:
            _fail_bootstrap(f"{mount_point} already holds files ({held}): new disks mounted over it would hide them")
            drop_setup_request(req_path)
            return True
        if _fstab_has(mount_point):
            _fail_bootstrap(f"/etc/fstab already mounts storage on {mount_point}, which isn't mounted now - "
                            "reconnect that disk instead")
            drop_setup_request(req_path)
            return True

    print(f"[raid-watch] Applying storage setup request: {device_paths}")

    if len(device_paths) == 0:
        os.makedirs(unenc_path, exist_ok=True)
        print(f"[raid-watch] No disks selected — using {mount_point} on the boot disk.")

    elif len(device_paths) == 1:
        dev = device_paths[0]
        if not _safe_to_wipe(dev):
            drop_setup_request(req_path)
            return True
        if not _run_step([CONFIG["paths"].get("wipefs", "wipefs"), "-a", dev]):
            return False
        if not _run_step(["mkfs.ext4", "-F", dev]):
            return False
        os.makedirs(mount_point, exist_ok=True)
        if not _run_step(["mount", "-o", "nosuid,nodev", dev, mount_point]):
            return False
        _own_storage(mount_point, unenc_path)
        if not _add_fstab(dev, mount_point):
            return False
        print(f"[raid-watch] {dev} formatted and mounted at {mount_point} (no RAID, single disk); "
              "MariaDB's database stays where it was, on the system disk.")

    elif len(device_paths) == 2:
        d1, d2 = device_paths
        if d1 == d2 or not _safe_to_wipe(d1) or not _safe_to_wipe(d2):
            if d1 == d2:
                _fail_bootstrap(f"{d1} was chosen twice: the mirror needs two different disks")
            drop_setup_request(req_path)
            return True
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
        _own_storage(mount_point, unenc_path)
        scan = run([CONFIG["paths"]["mdadm"], "--detail", "--scan"])
        with open("/etc/mdadm/mdadm.conf", "a") as f:
            f.write(scan.stdout)
        run(["update-initramfs", "-u"])
        if not _add_fstab(raid_dev, mount_point):
            return False
        print(f"[raid-watch] RAID1 built on {raid_dev} from {d1}+{d2}, mounted at {mount_point}; "
              "MariaDB's database stays where it was, on the system disk.")

    else:
        _fail_bootstrap(f"expected 0, 1, or 2 device paths, got {len(device_paths)}")
        drop_setup_request(req_path)
        return True

    drop_setup_request(req_path)
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
        st = os.lstat(path)  # a symlink is a request too: read_setup_request drops it
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

def array_condition(state):
    """mdadm's State without "clean"/"active", which only says whether a
    write is under way and flips with every one ("clean, degraded" <->
    "active, degraded"): what is left is what changed - "degraded",
    "degraded, recovering", "resyncing", or "" for a whole, settled mirror."""
    return ", ".join(w for w in (x.strip() for x in state.split(",")) if w and w not in ("clean", "active"))

def describe_array(state, members, raid_disks=2):
    if not state and not members:
        return "mirror md0: not assembled"
    slots = []
    for slot in range(max([raid_disks] + [k + 1 for k in members])):
        m = members.get(slot)
        slots.append(f"slot {slot} {m['device']} ({m['state']})" if m else f"slot {slot} empty")
    return f"mirror md0: {array_condition(state) or ('in sync' if state else 'unknown')}; " + "; ".join(slots)

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
            "kernel_failed": False, "why": {}, "progress_at": 0.0, "repair_at": -DISKS_EVERY_S}
    # (disk, size, model, port) -> judge_disk's verdict, while it is plugged in.
    verdicts = {}
    # The last mdadm --detail, the sysfs it was read with, and when.
    md_cache = {"fp": None, "at": 0.0, "detail": None}

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
            # mdadm only when md's sysfs changed, or every MDADM_EVERY_S.
            if array_present:
                fp, mono_md = md_fingerprint(raid_name), time.monotonic()
                if fp is None or fp != md_cache["fp"] or md_cache["detail"] is None \
                        or mono_md - md_cache["at"] >= MDADM_EVERY_S:
                    md_cache.update(fp=fp, at=mono_md, detail=mdadm_detail(raid_dev))
                detail = md_cache["detail"]
            else:
                md_cache.update(fp=None, at=0.0, detail=None)
                detail = {"state": "", "members": {}, "other_devices": set()}
            state = (detail.get("state") or "").lower()
            members = detail.get("members", {})
            # Log only when the picture changes: this runs every poll_s,
            # and printing every time filled the journal on the SD card.
            # Not the raw state: "clean"/"active" flip with every write.
            summary = (array_condition(state), tuple(sorted((k, v.get("state")) for k, v in members.items())))
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

            # Auto-repair path. Looked at every DISKS_EVERY_S, and at once
            # when the disks or the mirror change: it forks lsblk per disk,
            # and a disk's verdict (judge_disk) is kept while it stays.
            repair_due = changed[0] or disks_changed or mono - seen["repair_at"] >= DISKS_EVERY_S
            if CONFIG["auto_repair_enable"] and array_present:
                # If degraded and exactly one member present, try to find a candidate
                if (("degraded" in state) or (len(members) < 2)) and repair_due:
                    seen["repair_at"] = mono
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
                    # ...and its verdict: what is plugged in next is judged anew.
                    for k in [k for k in verdicts if (k[0], k[1]) not in present]:
                        del verdicts[k]
                    seen_by_name = {"/dev/" + name: (model, where) for name, _sz, model, where in (seen["disks"] or disks_seen())}
                    uuid = []

                    def judge(d, sz):
                        key = (d, sz) + seen_by_name.get(d, ("", ""))
                        if key not in verdicts:
                            if not uuid:
                                uuid.append(array_uuid_of(raid_dev))
                            verdicts[key] = judge_disk(d, uuid[0], members)
                        return verdicts[key]

                    mono = time.monotonic()
                    waiting = {k for k, v in failed_adds.items() if mono < v["next_try"]}
                    why = {}
                    refused = []
                    candidate = pick_candidate_disk(existing_size, detail, skip=waiting, disks=disks, why=why,
                                                    judge=judge, refused=refused)
                    if why != seen["why"]:
                        for d, reason in sorted(why.items()):
                            if seen["why"].get(d) != reason:
                                print(f"[raid-watch] {d} is not added to the mirror: {reason}")
                        seen["why"] = why
                    # For the device's Alert: one per disk plugged in.
                    sizes_now = dict(disks)
                    not_added = []
                    for d in refused:
                        model, where = seen_by_name.get(d, ("", ""))
                        sz = sizes_now.get(d, 0)
                        extra = ", ".join(x for x in (model, where) if x)
                        not_added.append({"id": f"{os.path.basename(d)}|{sz}|{model}|{where}",
                                          "disk": f"{os.path.basename(d)} {human_size(sz)}" + (f" ({extra})" if extra else "")})
                    write_not_added(not_added)
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
                elif not (("degraded" in state) or (len(members) < 2)):
                    failed_adds.clear()
                    verdicts.clear()
                    seen["why"] = {}
                    write_not_added([])
            else:
                failed_adds.clear()
                verdicts.clear()
                if seen["why"]:
                    seen["why"] = {}
                write_not_added([])

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
    assert said == "mirror md0: degraded; slot 0 /dev/sda (active sync); slot 1 empty", said
    # A write under way changes nothing worth a line.
    assert array_condition("active, degraded") == array_condition("clean, degraded") == "degraded"
    assert array_condition("active, degraded, recovering") == "degraded, recovering"
    assert array_condition("clean") == array_condition("active") == ""
    both = {0: {"device": "/dev/sda", "state": "active sync"}, 1: {"device": "/dev/sdb", "state": "active sync"}}
    assert describe_array("active", both) == "mirror md0: in sync; slot 0 /dev/sda (active sync); slot 1 /dev/sdb (active sync)"

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
    selftest_disks()
    print("selftest ok")

def selftest_disks():
    """Which disk may join the mirror (judge_disk, pick_candidate_disk), the
    storage setup's own checks, and the file the device's Alert reads."""
    import tempfile
    global run, read_ends, has_partitions
    real = (run, read_ends, has_partitions)
    uuid = "1111aaaa:2222bbbb:3333cccc:4444dddd"
    members = {0: {"device": "/dev/sda", "state": "active sync"}}
    # What each disk shows: mdadm --examine --export, --examine, blkid -p,
    # wipefs, its partitions and its two ends (or the error reading them).
    disks = {
        "/dev/sdb": {"export": f"MD_LEVEL=raid1\nMD_UUID={uuid}\n", "examine": "   Array State : AA ('A' == active)\n"},
        "/dev/sdc": {"export": f"MD_LEVEL=raid1\nMD_UUID={uuid}\n", "examine": "   Array State : .A ('A' == active)\n"},
        "/dev/sdd": {"export": "MD_LEVEL=raid1\nMD_UUID=9999eeee:0000ffff:1212abab:3434cdcd\n"},
        "/dev/sde": {"parts": True},
        "/dev/sdf": {"blkid": (0, "DEVNAME=/dev/sdf\nTYPE=exfat\nLABEL=Untitled\n")},
        "/dev/sdg": {},
        "/dev/sdh": {"ends": (b"\xff" * 64, b"\xff" * 64)},
        "/dev/sdi": {"ends": (b"\0" * 63 + b"x", b"\0" * 64)},
        "/dev/sdj": {"ends": OSError(5, "Input/output error")},
        "/dev/sdk": {"wipefs": "0x438 ext4 53ef\n"},
        "/dev/sdl": {"blkid": (8, "")},
    }

    def fake_run(cmd, **_kw):
        name, dev = os.path.basename(cmd[0]), cmd[-1]
        d = disks.get(dev, {})
        if name == "mdadm" and "--export" in cmd:
            out = d.get("export", "")
            return subprocess.CompletedProcess(cmd, 0 if out else 1, out, "" if out else f"mdadm: No md superblock detected on {dev}.")
        if name == "mdadm":
            return subprocess.CompletedProcess(cmd, 0 if "examine" in d else 1, d.get("examine", ""), "")
        if name == "blkid":
            code, out = d.get("blkid", (2, ""))
            return subprocess.CompletedProcess(cmd, code, out, "")
        if name == "wipefs":
            return subprocess.CompletedProcess(cmd, 0, d.get("wipefs", ""), "")
        raise AssertionError(cmd)

    def fake_ends(dev):
        e = disks[dev].get("ends", (b"\0" * 64, b"\0" * 64))
        if isinstance(e, Exception):
            raise e
        return e

    run, read_ends, has_partitions = fake_run, fake_ends, lambda dev, sys_root="/sys": disks[dev].get("parts", False)
    try:
        got = {d: judge_disk(d, uuid, members) for d in disks}
    finally:
        run, read_ends, has_partitions = real
    want = {
        "/dev/sdb": ("add", "it was a member of this mirror"),
        "/dev/sdc": ("refuse", "it was a member of this mirror but ran on its own since, so it holds changes the mirror lacks (copy what you need off it first)"),
        "/dev/sdd": ("refuse", "it holds part of another disk array"),
        "/dev/sde": ("refuse", "it has partitions"),
        "/dev/sdf": ("refuse", "it holds exfat labelled 'Untitled'"),
        "/dev/sdg": ("add", "it is blank"),
        "/dev/sdh": ("add", "it is blank"),
        "/dev/sdi": ("refuse", "it holds data (no filesystem this device knows)"),
        "/dev/sdj": ("refuse", "it can't be read (Input/output error)"),
        "/dev/sdk": ("refuse", "it holds signatures wipefs lists (0x438 ext4 53ef)"),
        "/dev/sdl": ("refuse", "it holds several signatures"),
    }
    for d in disks:
        assert got[d] == want[d], (d, got[d])
    assert uniform(b"\0" * 8) and uniform(b"\xff" * 8) and not uniform(b"\0\xff") and not uniform(b"")

    # The judge decides once the other checks pass: the blank disk is
    # taken, the one with data is left alone, said why and listed.
    why, refused = {}, []
    verdicts = {"/dev/sdb": ("refuse", "it holds exfat"), "/dev/sdc": ("add", "it is blank")}
    pick = pick_candidate_disk(500, {"members": members}, disks=[("/dev/sda", 500), ("/dev/sdb", 900), ("/dev/sdc", 600)],
                               why=why, judge=lambda d, sz: verdicts[d], refused=refused)
    assert pick == "/dev/sdc" and refused == ["/dev/sdb"], (pick, refused)
    assert why["/dev/sdb"].startswith("it holds exfat - it was left alone. To use it for the mirror, erase it"), why
    assert pick_candidate_disk(500, {"members": members}, disks=[("/dev/sdb", 900)], why={}) is None

    with tempfile.TemporaryDirectory() as tmp:
        # The storage directory: bookkeeping and an empty unencrypted/ hold
        # nothing; a blob, or a file in unencrypted/, is the owner's.
        st = os.path.join(tmp, "storage")
        os.makedirs(os.path.join(st, "unencrypted"))
        os.makedirs(os.path.join(st, ".lan-tls"))
        Path(st, ".storage-log-read").write_text("{}")
        assert _storage_holds_data(st) == "", _storage_holds_data(st)
        assert _storage_holds_data(os.path.join(tmp, "none")) == ""
        Path(st, "unencrypted", "a.txt").write_text("x")
        assert _storage_holds_data(st) == os.path.join(st, "unencrypted")
        os.remove(os.path.join(st, "unencrypted", "a.txt"))
        Path(st, "ab" * 32).write_text("blob")
        assert _storage_holds_data(st) == os.path.join(st, "ab" * 32)

        fstab = os.path.join(tmp, "fstab")
        Path(fstab).write_text("# /dev/md0 /mnt/storage ext4 defaults 0 0\nLABEL=root / ext4 defaults 0 1\n")
        assert not _fstab_has("/mnt/storage", fstab)
        Path(fstab).write_text("UUID=abc   /mnt/storage   ext4   defaults,nofail   0   0\n")
        assert _fstab_has("/mnt/storage", fstab)

        # The request: a small regular file only, never through a symlink
        # or from a FIFO (which would block a plain open).
        req = os.path.join(tmp, "req.json")
        Path(req).write_text('{"device_paths": ["/dev/sda"]}')
        assert read_setup_request(req) == {"device_paths": ["/dev/sda"]}
        link = os.path.join(tmp, "link.json")
        os.symlink(req, link)
        assert read_setup_request(link) is None
        fifo = os.path.join(tmp, "fifo.json")
        os.mkfifo(fifo)
        assert read_setup_request(fifo) is None
        drop_setup_request(fifo)
        drop_setup_request(link)
        assert not os.path.lexists(fifo) and not os.path.lexists(link) and os.path.exists(req)
        Path(req).write_text(" " * (MAX_REQUEST_BYTES + 1))
        assert read_setup_request(req) is None
        os.makedirs(os.path.join(tmp, "dir.json", "inside"))
        drop_setup_request(os.path.join(tmp, "dir.json"))
        assert not os.path.lexists(os.path.join(tmp, "dir.json"))

        # md's sysfs as the poll reads it: a write (clean/active) changes
        # nothing, a member that fails or a rebuild that ends does.
        md = os.path.join(tmp, "sys", "block", "md0", "md")
        def put(rel, value):
            os.makedirs(os.path.dirname(os.path.join(md, rel)), exist_ok=True)
            Path(md, rel).write_text(value + "\n")
        for rel, value in (("array_state", "clean"), ("degraded", "0"), ("sync_action", "idle"), ("raid_disks", "2"),
                           ("dev-sda/slot", "0"), ("dev-sda/state", "in_sync"), ("dev-sdb/slot", "1"), ("dev-sdb/state", "in_sync")):
            put(rel, value)
        sys_root = os.path.join(tmp, "sys")
        first = md_fingerprint("md0", sys_root)
        put("array_state", "active")
        assert md_fingerprint("md0", sys_root) == first
        put("dev-sdb/state", "faulty")
        put("degraded", "1")
        failed = md_fingerprint("md0", sys_root)
        assert failed != first
        put("dev-sdb/slot", "none")
        assert md_fingerprint("md0", sys_root) != failed
        put("array_state", "inactive")
        assert md_fingerprint("md0", sys_root)[0] == "inactive"
        assert md_fingerprint("md1", sys_root) is None

        # The Alert's file: written when it changes, an empty list when
        # nothing is left alone.
        na = os.path.join(tmp, "otc-raid-watch", "not-added.json")
        write_not_added([], na)
        assert not os.path.exists(na)
        write_not_added([{"id": "sdb|9|M|P", "disk": "sdb 9 MB (M, P)"}], na)
        assert json.loads(Path(na).read_text()) == {"disks": [{"disk": "sdb 9 MB (M, P)", "id": "sdb|9|M|P"}]}
        write_not_added([], na)
        assert json.loads(Path(na).read_text()) == {"disks": []}

if __name__ == "__main__":
    if "--selftest" in sys.argv:
        selftest()
    else:
        main()
