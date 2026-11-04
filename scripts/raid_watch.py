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
  * Storage missing (issue #207: /mnt/storage not mounted for a minute -
    the disks unplugged, the mirror not assembled, a mount that failed) ->
    the two LEDs red in turn, a second each, whatever the disks say.
- The storage back (StorageGuard): fstab's storage line not mounted while
  its device is there (disks plugged in late, a mirror assembled by hand)
  is mounted; after a minute without it otc-degraded.service tells the
  apps why (`otc <env> degraded`); once it is mounted again MariaDB and
  otc.service are started, which stops otc-degraded.service.
- The storage gone while mounted (StorageGuard, "The storage gone while
  mounted" below): both disks of the mirror pulled while the device runs,
  or the single storage disk pulled - MariaDB and otc.service are stopped,
  the mount and the dead array taken down, and otc-degraded.service
  started at once; the disks plugged in again are assembled, mounted and
  started as above. A mirror down to one disk runs on.
- Auto-repair (optional):
  * When degraded and a disk appears that is blank (no partition table, no
    filesystem or other signature, first and last MiB empty) or was a member
    of this very mirror, mdadm --add it to /dev/md0 to start the rebuild. Any
    other disk is left alone: it is logged once and the device raises an
    Alert (raidwatch, from /run/otc-raid-watch/not-added.json).
  * Settings offers such a disk to its owner, who may have it erased and
    added (/run/otc/mirror-disk.request): checked again here, as root,
    before anything is erased (see "Settings > Storage" below).

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
import errno
import signal
import hashlib
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
if sys.argv[1:2] == ["--ends-blank"]:
    GPIO = None  # ends_blank's child reads a disk, and touches no light
else:
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
# How long a command that reads a disk may take before it is given up
# (run's timeout, run_bounded): mdadm reading a member's superblock,
# blkid and wipefs looking for signatures, dumpe2fs, a disk's two ends. A
# disk that stays attached but stops answering used to hold the whole loop
# - the LEDs, StorageGuard, the repair - for minutes, in exactly the case
# StorageGuard bounds its own commands for.
SUPERBLOCK_READ_S = 10
DISK_READ_S = 20
DISK_WRITE_S = 60

def run(cmd, check=False, capture=True, text=True, timeout=None):
    """The command's CompletedProcess; never raises. With a timeout it is
    run_bounded: given up (exit 124) after that many seconds."""
    if isinstance(cmd, str):
        cmd = shlex.split(cmd)
    if timeout is not None:
        return run_bounded(cmd, timeout)
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
    None when mdadm is given up (it reads a member's superblock, which a
    disk that stopped answering never returns); any other failure parses
    to no state and no members, as it always did.
    """
    r = run([CONFIG["paths"]["mdadm"], "--detail", raid_dev], timeout=SUPERBLOCK_READ_S)
    if r.returncode == 124:
        return None
    out = r.stdout or ""
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

def rebuild_source_slot(members, rebuilding_slot):
    """During a rebuild, the slot being copied from: a member md counts in
    sync, not the one being rebuilt, the lowest slot first; None when there
    is none. md's own state, from mdadm --detail: each member's superblock
    used to be read (mdadm --examine) every poll while rebuilding, to
    compare event counts - a disk read every half second, a crash when one
    of them couldn't be read, and the wrong LED when the counts were
    equal."""
    for slot in sorted(members):
        if slot != rebuilding_slot and in_sync(members[slot]):
            return slot
    return None

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
            why[d] = (f"{reason} - it was left alone. To use it for the mirror, choose it in Settings, "
                      "which has it erased, or plug in a blank disk")
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
    return run([CONFIG["paths"]["mdadm"], "--add", raid_dev, new_dev], timeout=DISK_WRITE_S)

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
    r = run([tool("blkid"), "-p", "-o", "export", dev], timeout=DISK_READ_S)
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
    r = run([tool("wipefs"), "--no-act", "--noheadings", dev], timeout=DISK_READ_S)
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

def ends_blank(dev, timeout=DISK_READ_S):
    """Whether dev's first and last MiB hold nothing (uniform), read by
    read_ends in a child process given up after timeout seconds: a read
    from a disk that stopped answering waits in the kernel for minutes,
    and the loop goes on meanwhile. Raises OSError when they can't be
    read."""
    r = run([sys.executable, "-I", os.path.abspath(__file__), "--ends-blank", dev], timeout=timeout)
    if r.returncode in (0, 1):
        return r.returncode == 0
    if r.returncode == 124:
        raise OSError(errno.ETIMEDOUT, f"no answer in {timeout} s")
    said = (r.stderr or "").strip().splitlines()
    raise OSError(errno.EIO, said[-1] if said else f"exit {r.returncode}")

def ends_blank_child(dev):
    """--ends-blank's exit status: 0 blank, 1 holds data, 2 can't be read
    (why on stderr)."""
    try:
        first, last = read_ends(dev)
    except OSError as e:
        sys.stderr.write(f"{e.strerror or e}\n")
        return 2
    return 0 if uniform(first) and uniform(last) else 1

ARRAY_STATE = re.compile(r"^\s*Array State\s*:\s*([A-Za-z.]+)", re.M)

def diverged(dev, members):
    """The disk's own superblock says it last ran without a disk the mirror
    runs on now (docker/mirror's diverged): each copy then holds changes the
    other lacks. True/False, or None when the superblock can't be read. A
    disk that only dropped out while the other went on still shows both
    slots active: it is behind, and a re-add catches it up."""
    r = run([CONFIG["paths"]["mdadm"], "--examine", dev], timeout=SUPERBLOCK_READ_S)
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
    other, why being words for the storage log. Every read of the disk is
    given up after a while (a disk that doesn't answer is refused)."""
    r = run([CONFIG["paths"]["mdadm"], "--examine", "--export", dev], timeout=SUPERBLOCK_READ_S)
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
        blank = ends_blank(dev)
    except OSError as e:
        return "refuse", f"it can't be read ({e.strerror or e})"
    if not blank:
        return "refuse", "it holds data (no filesystem this device knows)"
    return "add", "it is blank"

def array_uuid_of(raid_dev):
    """md0's UUID (mdadm --detail --export), or "" when it can't be read."""
    r = run([CONFIG["paths"]["mdadm"], "--detail", "--export", raid_dev], timeout=SUPERBLOCK_READ_S)
    return kv_lines(r.stdout).get("MD_UUID", "") if r.returncode == 0 else ""

# The disks left alone, for the device (raidwatch reads it every minute
# and raises one Alert per disk plugged in): root's directory on the /run
# tmpfs, which the service may read and nothing but root may write.
NOT_ADDED_FILE = "/run/otc-raid-watch/not-added.json"
_not_added_written = {}

def write_not_added(entries, path=None):
    """Writes the disks left alone ([] for none) when that changed: a temp
    file beside it, renamed over it. Each is {"id", "disk"} - what the
    Alert goes by, once per id, and its words for a technician - and, for
    Settings (offer_fields), "model", "size", "held" and, when Settings may
    offer it, "offer"."""
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

def mirror_needs_disk(members, raid_disks=2):
    """The mirror lacks a disk: a slot with no disk in it, or only a faulty
    one. Not while a replacement is being copied onto - md still says
    degraded then, but there is nothing to add (it was a spare)."""
    filled = {s for s, m in members.items() if 0 <= s < raid_disks and "faulty" not in m.get("state", "")}
    return len(filled) < raid_disks

def in_sync(member):
    """md counts the member an up-to-date copy."""
    state = member.get("state", "")
    return "active sync" in state or "in_sync" in state

# ----------------------------
# Settings > Storage: a disk left alone, erased and added at the owner's word
# ----------------------------
# Most disks and cards come formatted, so a real replacement is usually
# left alone above. Settings offers each such disk to the owner ("New disk
# found: ... Use it for the mirror?"), and the otc service hands their
# answer to this script in USE_REQUEST - on the service's own /run tmpfs
# directory, which only the otc user writes - as {"v": 1, "id": <16 hex>,
# "disk": <offer id>}. Nothing in it is trusted: the disk must be one of
# those left alone that this process lists now (not-added.json's "offer"),
# and it is checked again before anything is erased - the same disk (the
# kernel's sequence number for this attachment, and its size), nothing
# holding it (no mount, swap, array or volume, and an exclusive open), not
# the system's disk nor one of the mirror's, big enough, and the mirror
# still lacking a disk. Then its signatures and partition table go (wipefs
# -a on each partition, then on the disk) and it is added as a blank disk
# is. The outcome goes to USE_OUTCOME, which the device reads (an Alert
# when the disk wasn't added), and the request is gone once taken.
USE_REQUEST = "/run/otc/mirror-disk.request"
USE_OUTCOME = "/run/otc-raid-watch/mirror-disk.json"
USE_MAX_REQUEST = 512
USE_KEYS = frozenset({"v", "id", "disk"})
USE_REQ_ID = re.compile(r"[0-9a-f]{16}")
OFFER_ID = re.compile(r"[0-9a-f]{20}")
# Every outcome, which the device words (raidwatch's useCodes, useAlert).
USE_CODES = frozenset({"added", "gone", "not_degraded", "in_use", "failed", "bad_request"})
WHOLE_DISK = re.compile(r"sd[a-z]{1,2}")
# BLKGETDISKSEQ, _IOR(0x12, 128, __u64): the sequence number of the disk
# an open file is on (Linux 5.15).
BLKGETDISKSEQ = 0x80081280

class UseRefused(Exception):
    """The request ends with code (USE_CODES); why is for the log."""
    def __init__(self, code, why):
        super().__init__(why)
        self.code, self.why = code, why

def disk_seq(name, sys_root="/sys"):
    """The kernel's sequence number for this attachment of the disk
    (/sys/block/<name>/diskseq, Linux 5.15): never given to another disk
    until a reboot, so a disk unplugged and another plugged in under the
    same name has another. None when the kernel doesn't say."""
    v = _read(os.path.join(sys_root, "block", name, "diskseq"))
    return int(v) if v.isdigit() else None

def disk_bytes(name, sys_root="/sys"):
    v = _read(os.path.join(sys_root, "block", name, "size"))
    return int(v) * 512 if v.isdigit() else 0

def partitions_of(name, sys_root="/sys"):
    """The disk's partitions the kernel has (sdb1, sdb2...)."""
    try:
        found = [e for e in os.listdir(os.path.join(sys_root, "block", name)) if e.startswith(name) and e != name]
    except OSError:
        return []
    return sorted(found, key=lambda e: (len(e), e))

def disk_held(name, sys_root="/sys", proc_root="/proc"):
    """What holds the disk or one of its partitions, in words for the log:
    an array or volume built on it (holders/), a mount, or swap. "" for
    nothing."""
    numbers = {}
    for dev in [name] + partitions_of(name, sys_root):
        base = os.path.join(sys_root, "block", name, *([] if dev == name else [dev]))
        try:
            if os.listdir(os.path.join(base, "holders")):
                return f"{dev} is part of an array or volume that is running"
        except OSError:
            pass
        number = _read(os.path.join(base, "dev"))
        if number:
            numbers[number] = dev
    try:
        for line in Path(proc_root, "self", "mountinfo").read_text().splitlines():
            f = line.split()
            if len(f) > 4 and f[2] in numbers:
                return f"{numbers[f[2]]} is mounted on {f[4]}"
    except OSError:
        return "the mounts can't be read"
    try:
        for line in Path(proc_root, "swaps").read_text().splitlines()[1:]:
            f = line.split()
            if f and os.path.basename(f[0]) in numbers.values():
                return f"{os.path.basename(f[0])} is used as swap"
    except OSError:
        pass  # the exclusive open still says
    return ""

_BOOT_SALT = []

def boot_salt():
    """What makes an offer's id this boot's: the kernel's boot id, or a
    random value for this process when it can't be read."""
    if not _BOOT_SALT:
        v = _read("/proc/sys/kernel/random/boot_id")
        _BOOT_SALT.append(v if re.fullmatch(r"[0-9a-f-]{36}", v) else os.urandom(16).hex())
    return _BOOT_SALT[0]

def offer_id(name, seq, size):
    """What Settings names a disk by: opaque, the same while it stays
    plugged in, and another for any other disk, for this one plugged in
    again, and after a reboot."""
    return hashlib.sha256(f"{boot_salt()}|{name}|{seq}|{size}".encode()).hexdigest()[:20]

def offer_fields(name, size, model, sys_root="/sys", proc_root="/proc", held=None):
    """What not-added.json says of a disk left alone, for Settings: its
    model and size, whether something holds it (held: disk_held's words,
    looked up when not given) and - when nothing does and the kernel numbers
    this attachment - the id Settings may offer it by. Returns (fields, what
    the loop keeps of it to check a request against, or None when it isn't
    offered)."""
    if held is None:
        held = disk_held(name, sys_root, proc_root)
    fields = {"model": model, "size": size, "held": bool(held)}
    seq = disk_seq(name, sys_root)
    if held or seq is None or not size or not WHOLE_DISK.fullmatch(name):
        return fields, None
    fields["offer"] = offer_id(name, seq, size)
    return fields, {"dev": "/dev/" + name, "seq": seq, "size": size}

def _no_duplicates(pairs):
    out = {}
    for k, v in pairs:
        if k in out:
            raise ValueError("duplicate key")
        out[k] = v
    return out

def _no_constant(_name):
    raise ValueError("not JSON")

def parse_use_request(data):
    """(id, disk) of a request's bytes; UseRefused("bad_request") for
    anything but exactly {"v": 1, "id": <16 hex>, "disk": <20 hex>}."""
    bad = UseRefused("bad_request", "the request can't be read")
    try:
        obj = json.loads(data.decode("utf-8"), object_pairs_hook=_no_duplicates, parse_constant=_no_constant)
    except (UnicodeDecodeError, ValueError, RecursionError):
        raise bad
    if not isinstance(obj, dict) or set(obj) != USE_KEYS or type(obj["v"]) is not int or obj["v"] != 1:
        raise bad
    rid, disk = obj["id"], obj["disk"]
    if not (isinstance(rid, str) and USE_REQ_ID.fullmatch(rid) and isinstance(disk, str) and OFFER_ID.fullmatch(disk)):
        raise bad
    return rid, disk

def otc_uid():
    import pwd
    try:
        return pwd.getpwnam("otc").pw_uid
    except KeyError:
        return None

def discard(path):
    """Removes what was taken - a file, or a directory renamed in (without
    following links)."""
    import shutil
    try:
        st = os.lstat(path)
    except OSError:
        return
    try:
        if stat.S_ISDIR(st.st_mode):
            shutil.rmtree(path, ignore_errors=True)
        else:
            os.unlink(path)
    except OSError:
        pass

def read_taken_use(path, uid, sleep=time.sleep):
    """The request taken to path: a regular file owned by uid with one link
    and at most USE_MAX_REQUEST bytes, read without following a link and
    without blocking on a FIFO."""
    bad = UseRefused("bad_request", "the request is not a small regular file of the otc user's")
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    except OSError:
        raise bad
    try:
        st = os.fstat(fd)
        # The device links its temporary file into place and removes the
        # temporary name right after: a moment's second link is that.
        for _ in range(10):
            if st.st_nlink <= 1:
                break
            sleep(0.1)
            st = os.fstat(fd)
        if (not stat.S_ISREG(st.st_mode) or uid is None or st.st_uid != uid or st.st_nlink != 1
                or st.st_size > USE_MAX_REQUEST):
            raise bad
        data = os.read(fd, USE_MAX_REQUEST + 1)
        if len(data) > USE_MAX_REQUEST:
            raise bad
    finally:
        os.close(fd)
    return parse_use_request(data)

def take_use_request(path=None, state_dir=None, uid=None):
    """The owner's request, taken: renamed into root's own directory first
    (both on the /run tmpfs: atomic, and a symlink is moved, never
    followed), read, and removed. (id, disk); None when there is none;
    UseRefused("bad_request") for anything else."""
    path = path or USE_REQUEST
    state_dir = state_dir or os.path.dirname(USE_OUTCOME)
    try:
        os.makedirs(state_dir, mode=0o755, exist_ok=True)
        st = os.lstat(state_dir)
    except OSError:
        raise UseRefused("bad_request", f"{state_dir} can't be made")
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid():
        raise UseRefused("bad_request", f"{state_dir} is not this script's own directory")
    taken = os.path.join(state_dir, f"use.{os.getpid()}")
    discard(taken)
    try:
        os.rename(path, taken)
    except FileNotFoundError:
        return None
    except OSError:
        discard(path)  # never left there to be tried again every poll
        raise UseRefused("bad_request", "the request can't be taken")
    try:
        return read_taken_use(taken, otc_uid() if uid is None else uid)
    finally:
        discard(taken)

def is_block_device(dev):
    try:
        return stat.S_ISBLK(os.stat(dev).st_mode)
    except OSError:
        return False

def exclusive_seq(dev):
    """Opens dev exclusively - which nothing can while a mount, swap, array
    or volume holds it or one of its partitions - and reads the sequence
    number of the disk it opened (None where the kernel can't say). Raises
    OSError, EBUSY when something holds it."""
    import fcntl
    import struct
    fd = os.open(dev, os.O_RDONLY | os.O_EXCL | os.O_NONBLOCK | os.O_CLOEXEC)
    try:
        if not stat.S_ISBLK(os.fstat(fd).st_mode):
            raise OSError(errno.ENOTBLK, "not a block device")
        buf = bytearray(8)
        try:
            fcntl.ioctl(fd, BLKGETDISKSEQ, buf, True)
        except OSError:
            return None
        return struct.unpack("=Q", bytes(buf))[0]
    finally:
        os.close(fd)

def use_disk(offer, offered, raid_dev, sys_root="/sys", proc_root="/proc"):
    """Erases the disk offered as offer and adds it to raid_dev, once
    everything is checked again; UseRefused (with its code) when it isn't,
    or when erasing or adding fails."""
    # The mirror still lacks a disk: read now, never the poll's cached view.
    if not array_in_mdstat(mdstat(), raid_name_from_dev(raid_dev)):
        raise UseRefused("not_degraded", f"{raid_dev} is not assembled")
    detail = mdadm_detail(raid_dev)
    if detail is None:
        raise UseRefused("failed", f"{raid_dev}'s state can't be read (mdadm --detail gave no answer)")
    members = detail.get("members", {})
    if not any(in_sync(m) for m in members.values()):
        raise UseRefused("not_degraded", f"{raid_dev} has no working disk to copy from")
    if not mirror_needs_disk(members):
        raise UseRefused("not_degraded", f"{raid_dev} has both its disks (or one is being copied onto)")
    entry = offered.get(offer)
    if entry is None:
        raise UseRefused("gone", "it is not one of the disks left alone now (unplugged, or already added)")
    dev = entry["dev"]
    name = os.path.basename(dev)
    if not WHOLE_DISK.fullmatch(name) or dev != "/dev/" + name:
        raise UseRefused("gone", f"{dev!r} is not a disk this device can use")
    # The same disk: this attachment's sequence number, and its size.
    if disk_seq(name, sys_root) != entry["seq"] or disk_bytes(name, sys_root) != entry["size"]:
        raise UseRefused("gone", f"{name} is no longer the disk offered (unplugged, or another in its place)")
    if not is_block_device(dev):
        raise UseRefused("gone", f"{dev} is not a block device")
    if name in {os.path.basename(b) for b in member_base_disks(detail)}:
        raise UseRefused("in_use", f"{name} is the mirror's own")
    root = root_base_disk()
    if root and base_disk(dev) == root:
        raise UseRefused("in_use", f"{name} holds the system")
    held = disk_held(name, sys_root, proc_root)
    if held:
        raise UseRefused("in_use", held)
    sizes = [size_of(m["device"]) for m in members.values() if in_sync(m)]
    if sizes and entry["size"] < int(max(sizes) * CONFIG["auto_repair_min_size_ratio"]):
        raise UseRefused("in_use", f"too small: {human_size(entry['size'])}, and the working disk is {human_size(max(sizes))}")
    try:
        opened = exclusive_seq(dev)
    except OSError as e:
        if e.errno == errno.EBUSY:
            raise UseRefused("in_use", f"{name} is in use: it can't be opened exclusively")
        raise UseRefused("gone", f"{name} can't be opened ({e.strerror or e})")
    if opened is not None and opened != entry["seq"]:
        raise UseRefused("gone", f"{name} is no longer the disk offered")
    # Its partitions' signatures first: once the partition table is gone
    # nothing finds them any more. wipefs opens each exclusively too.
    for part in partitions_of(name, sys_root) + [name]:
        r = run([tool("wipefs"), "-a", "/dev/" + part], timeout=DISK_WRITE_S)
        if r.returncode != 0:
            raise UseRefused("failed", f"wipefs -a /dev/{part} failed: {(r.stderr or r.stdout or '').strip()}")
    if disk_seq(name, sys_root) != entry["seq"]:
        raise UseRefused("failed", f"{name} changed while it was being erased")
    r = add_member(raid_dev, dev)
    if r.returncode != 0:
        raise UseRefused("failed", f"mdadm --add failed: {(r.stderr or '').strip()}")

def write_use_outcome(rid, offer, code, what, path=None):
    """The request's outcome, for the device: {"v": 1, "id", "disk",
    "code", "what"} in root's directory, a temp file renamed over the last
    one's."""
    path = path or USE_OUTCOME
    data = json.dumps({"v": 1, "id": rid, "disk": offer, "code": code if code in USE_CODES else "failed",
                       "what": what}, sort_keys=True) + "\n"
    try:
        os.makedirs(os.path.dirname(path), mode=0o755, exist_ok=True)
        tmp = f"{path}.tmp.{os.getpid()}"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
        try:
            os.fchmod(fd, 0o644)
            os.write(fd, data.encode())
        finally:
            os.close(fd)
        os.replace(tmp, path)
    except OSError as e:
        print(f"[raid-watch] could not write {path}: {e}")

# SIGTERM waits while a request is answered (handle_use_request): a
# watcher restarted between erasing a disk and adding it would leave no
# outcome behind.
_TERM = {"busy": False, "pending": False}

def handle_use_request(offered, raid_dev, **paths):
    """Takes and answers the owner's request, when there is one; offered is
    what the last look at the disks left alone kept of each one Settings
    may offer (offer_fields). True when a disk was erased and added."""
    _TERM["busy"] = True
    try:
        try:
            taken = take_use_request(paths.get("request"), paths.get("state_dir"), paths.get("uid"))
        except UseRefused as r:
            print(f"[raid-watch] a request to use a disk for the mirror was refused: {r.why}")
            write_use_outcome("", "", r.code, "", paths.get("outcome"))
            return False
        if taken is None:
            return False
        rid, offer = taken
        what = (offered.get(offer) or {}).get("what", "")
        print(f"[raid-watch] Settings asks for {what or 'a disk'} to be erased and added to the mirror")
        try:
            use_disk(offer, offered, raid_dev, paths.get("sys_root", "/sys"), paths.get("proc_root", "/proc"))
        except UseRefused as r:
            print(f"[raid-watch] {what or 'the disk asked for'} was not used for the mirror: {r.why}")
            write_use_outcome(rid, offer, r.code, what, paths.get("outcome"))
            return False
        except Exception as e:  # answered all the same, never left waiting
            print(f"[raid-watch] {what or 'the disk asked for'} was not used for the mirror: {type(e).__name__}: {e}")
            write_use_outcome(rid, offer, "failed", what, paths.get("outcome"))
            return False
        print(f"[raid-watch] {what} was erased and added to {raid_dev}: the mirror copies everything onto it")
        write_use_outcome(rid, offer, "added", what, paths.get("outcome"))
        return True
    finally:
        _TERM["busy"] = False

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
    r = run([mdadm, "--grow", raid_dev, "--size=max"], timeout=DISK_WRITE_S)
    if r.returncode != 0 and "bitmap" in ((r.stderr or "") + (r.stdout or "")).lower():
        run([mdadm, "--grow", raid_dev, "--bitmap=none"], timeout=DISK_WRITE_S)
        r = run([mdadm, "--grow", raid_dev, "--size=max"], timeout=DISK_WRITE_S)
        run([mdadm, "--grow", raid_dev, "--bitmap=internal"], timeout=DISK_WRITE_S)
    return r

def fs_needs_resize(raid_dev):
    """The ext4 on the array is smaller than the array."""
    if run([CONFIG["paths"]["lsblk"], "-ndo", "FSTYPE", raid_dev]).stdout.strip() != "ext4":
        return False
    try:
        dev_bytes = int(run([CONFIG["paths"]["blockdev"], "--getsize64", raid_dev], timeout=SUPERBLOCK_READ_S).stdout.strip())
    except ValueError:
        return False
    out = run([CONFIG["paths"]["dumpe2fs"], "-h", raid_dev], timeout=DISK_READ_S).stdout
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

def _add_fstab(dev, mount_point, fstab="/etc/fstab", unit_dir="/etc/systemd/system"):
    """Adds the storage's line - by its filesystem's UUID, which stays with
    the disk whatever sdX it gets - written beside fstab, flushed and
    renamed over it, and the drop-ins. False when the line can't be made or
    written."""
    uuid = _fs_uuid(dev)
    if not uuid or "\n" in mount_point:
        _fail_bootstrap(f"could not read the new filesystem's UUID on {dev}")
        return False
    tmp = f"{fstab}.otc.{os.getpid()}"
    try:
        text = Path(fstab).read_text()
        if text and not text.endswith("\n"):
            text += "\n"
        text += f"UUID={uuid}   {mount_point}   ext4   {STORAGE_MOUNT_OPTS}   0   0\n"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
        try:
            os.write(fd, text.encode())
            os.fsync(fd)
        finally:
            os.close(fd)
        os.replace(tmp, fstab)
    except OSError as e:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        _fail_bootstrap(f"could not write {fstab}: {e}")
        return False
    for unit in STORAGE_DROPINS:
        d = os.path.join(unit_dir, f"{unit}.service.d")
        try:
            os.makedirs(d, mode=0o755, exist_ok=True)
            with open(os.path.join(d, "otc-storage.conf"), "w") as f:
                f.write("# Off The Cloud: start only with the storage mounted (fstab has nofail).\n"
                        f"[Unit]\nRequiresMountsFor={mount_point}\n")
        except OSError as e:
            print(f"[raid-watch] could not write the {unit} storage drop-in: {e}")
    run(["systemctl", "daemon-reload"])
    return True

def _record_array(raid_dev, conf="/etc/mdadm/mdadm.conf"):
    """The new mirror in mdadm.conf (its ARRAY line, from mdadm --detail
    --brief) and the initramfs, so it comes up as raid_dev at the next boot
    and not as md127 - unless mdadm.conf names it already. False, said why,
    when that can't be done (the setup is tried again)."""
    name = raid_name_from_dev(raid_dev)
    try:
        text = Path(conf).read_text()
    except FileNotFoundError:
        text = ""
    except OSError as e:
        _fail_bootstrap(f"could not read {conf}: {e}")
        return False
    if conf_array_uuid(text, name):
        return True
    r = run([CONFIG["paths"]["mdadm"], "--detail", "--brief", raid_dev], timeout=SUPERBLOCK_READ_S)
    lines = [l for l in (r.stdout or "").splitlines() if l.startswith("ARRAY ")]
    if r.returncode != 0 or not lines:
        _fail_bootstrap(f"could not read {raid_dev}'s line for {conf}: {(r.stderr or '').strip()}")
        return False
    try:
        os.makedirs(os.path.dirname(conf), exist_ok=True)
        with open(conf, "a") as f:
            if text and not text.endswith("\n"):
                f.write("\n")
            f.write("\n".join(lines) + "\n")
            f.flush()
            os.fsync(f.fileno())
    except OSError as e:
        _fail_bootstrap(f"could not write {conf}: {e}")
        return False
    run(["update-initramfs", "-u"])
    return True

def _finish_setup(device_paths, mount_point, raid_dev, mountinfo="/proc/self/mountinfo",
                  fstab="/etc/fstab", conf="/etc/mdadm/mdadm.conf", unit_dir="/etc/systemd/system"):
    """The storage mounted already: a try before this one formatted and
    mounted it, then stopped before its last steps (blkid had no UUID yet,
    the card's root filesystem was full) - the retry used to take the mount
    for a finished setup, so after the next reboot otc and MariaDB started
    on the empty directory under the mount point. Those steps are done now:
    the storage given to otc, the array in mdadm.conf, fstab's line and the
    drop-ins. Only for what
    this setup mounts - the disk the request named, or its mirror - and only
    while fstab has no line for the mount point: a mount of the owner's own
    is left as it is. False when a step fails (tried again)."""
    try:
        top = mount_on_top(mount_point, Path(mountinfo).read_text())
    except OSError:
        top = None
    if top is None or _fstab_has(mount_point, fstab):
        return True
    source = top[1]
    real = os.path.realpath(source) if source.startswith("/dev/") else source
    if len(device_paths) == 1 and real == os.path.realpath(device_paths[0]):
        dev = device_paths[0]
    elif len(device_paths) == 2 and real == os.path.realpath(raid_dev):
        dev = raid_dev
        if not _record_array(raid_dev, conf):
            return False
    else:
        return True
    print(f"[raid-watch] {mount_point} is mounted from {dev} but /etc/fstab has no line for it: finishing the storage setup")
    if not _own_storage(mount_point, os.path.join(mount_point, CONFIG["unenc_subdir"])):
        return False
    return _add_fstab(dev, mount_point, fstab, unit_dir)

def _own_storage(mount_point, unenc_path):
    """The new filesystem's root and unencrypted/ as install.sh leaves
    them: the service's (a new filesystem's root is root's, and uploads
    failed), 0750, the root also o+x (MariaDB passes through it when its
    datadir is on the storage). Each set on the directory opened without
    following a link: when a later try finishes the setup, the service may
    have written in the new storage already. False, said why, on failure."""
    import pwd
    try:
        otc = pwd.getpwnam("otc")
    except KeyError:
        print("[raid-watch] no otc user: the new storage keeps root's ownership")
        return True
    try:
        os.makedirs(unenc_path, exist_ok=True)
        for path, mode in ((mount_point, 0o751), (unenc_path, 0o750)):
            fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
            try:
                os.fchown(fd, otc.pw_uid, otc.pw_gid)
                os.fchmod(fd, mode)
            finally:
                os.close(fd)
    except OSError as e:
        _fail_bootstrap(f"could not give the new storage to the otc user: {e}")
        return False
    return True

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
        # An earlier try may have mounted it and stopped before fstab.
        if not _finish_setup(device_paths, mount_point, raid_dev):
            return False
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
        if not _own_storage(mount_point, unenc_path):
            return False
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
        if not _own_storage(mount_point, unenc_path):
            return False
        if not _record_array(raid_dev):
            return False
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

# How long md0 must have run before a disk is added to it or it is grown:
# disks plugged back in are gathered by udev (mdadm --incremental) one at a
# time, and the array starts once the newest of them is in.
REPAIR_SETTLE_S = 10

def running_settled(seen, mdstat_text, raid_name, mono):
    """Whether the array has been running (active in /proc/mdstat) for
    REPAIR_SETTLE_S; seen ({"since"}) remembers since when, between calls.
    Inactive (being gathered), broken or gone, it starts again."""
    if re.search(rf"^{re.escape(raid_name)}\s*:\s*active\b", mdstat_text, re.M) is None:
        seen["since"] = None
        return False
    if seen["since"] is None:
        seen["since"] = mono
    return mono - seen["since"] >= REPAIR_SETTLE_S

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
        r = run(cmd, timeout=DISK_READ_S)
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
# The storage missing (issue #207)
# ----------------------------
# otc.service and MariaDB need the storage mounted (RequiresMountsFor, the
# otc-storage.conf drop-ins), and its fstab line has nofail: with the disks
# missing at boot the device comes up without them, and nothing ever
# tried again - systemd doesn't mount a nofail line whose device shows up
# after its timeout, and a service that didn't start for want of a mount
# stays down. This does both: it mounts the storage once its device is
# there, and starts MariaDB and the service once it is mounted; and after
# STORAGE_GRACE_S without it, starts otc-degraded.service (the same binary
# without a database), which answers the apps storage_unavailable and
# which starting otc.service stops (Conflicts=).
STORAGE_GRACE_S = 60
STORAGE_LOOK_S = 2            # how often the guard looks
STORAGE_DEGRADED_S = 10       # how often it checks the degraded unit runs
STORAGE_STATE = "/run/otc-raid-watch/storage.json"
DEGRADED_UNIT = "otc-degraded.service"
FULL_UNITS = ("mariadb.service", "otc.service")

def _fstab_unescape(s):
    return re.sub(r"\\([0-7]{3})", lambda m: chr(int(m.group(1), 8)), s)

def storage_entry(fstab_text, mount_point):
    """fstab's line for mount_point as (source, fstype, options), or None:
    a commented line, swap and a noauto line don't count."""
    want = os.path.normpath(mount_point)
    found = None
    for line in fstab_text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        f = line.split()
        if len(f) < 3 or f[2] == "swap":
            continue
        opts = f[3] if len(f) > 3 else "defaults"
        if "noauto" in opts.split(","):
            continue
        if os.path.normpath(_fstab_unescape(f[1])) == want:
            found = (_fstab_unescape(f[0]), f[2], opts)
    return found

def mount_on_top(mount_point, mountinfo_text):
    """The whole filesystem on top at mount_point - its root, not a
    directory bound there - as (major:minor, source, superblock options,
    mount id), or None."""
    want = os.path.normpath(mount_point)
    found = None
    for line in mountinfo_text.splitlines():
        # id parent major:minor root mountpoint options [optional...] - fstype source superoptions
        f = line.split()
        if len(f) < 6 or os.path.normpath(_fstab_unescape(f[4])) != want:
            continue
        found = None   # the last line for a mount point is what is on top
        if _fstab_unescape(f[3]) != "/":
            continue
        tail = f[f.index("-", 6) + 1:] if "-" in f[6:] else []
        found = (f[2], _fstab_unescape(tail[1]) if len(tail) > 1 else "", tail[2] if len(tail) > 2 else "", f[0])
    return found

def whole_fs_mounted(mount_point, mountinfo_text):
    """A whole filesystem (its root, not a directory bound there) is on top
    at mount_point, as the Go side's check (nostorage.mounted)."""
    return mount_on_top(mount_point, mountinfo_text) is not None

_SOURCE_LINKS = {"UUID": "by-uuid", "LABEL": "by-label", "PARTUUID": "by-partuuid", "PARTLABEL": "by-partlabel"}

def source_names_device(source):
    """fstab's source names a block device this script can find again -
    UUID=, LABEL=, PARTUUID=, PARTLABEL= or a /dev path - so what it takes
    down it can mount back once the device is there. Not a network share
    (server:/export, //server/share) nor a FUSE or ZFS source."""
    kind, eq, _ = source.partition("=")
    return bool(eq and kind in _SOURCE_LINKS) or source.startswith("/dev/")

def source_device(source, dev_root="/dev", is_block=None):
    """The block device fstab's source names now (its real path), or None."""
    kind, eq, value = source.partition("=")
    if eq and kind in _SOURCE_LINKS:
        path = os.path.join(dev_root, "disk", _SOURCE_LINKS[kind], value)
    elif source.startswith("/dev/"):
        path = os.path.join(dev_root, source[len("/dev/"):])
    else:
        return None
    real = os.path.realpath(path)
    return real if (is_block or is_block_device)(real) else None

def source_present(source, mdstat_text, dev_root="/dev", is_block=None, sys_root="/sys"):
    """Whether the storage's device is there to mount: an md array only
    once active in /proc/mdstat (an inactive one, half assembled, has its
    node too) and with a disk that still holds its data (md_dead: never
    the dead array both disks were pulled from), anything else as a block
    device under its name."""
    real = source_device(source, dev_root, is_block)
    if real is None:
        return False
    name = os.path.basename(real)
    if re.fullmatch(r"md\d+", name):
        if re.search(rf"^{name}\s*:\s*active\b", mdstat_text, re.M) is None:
            return False
        return not md_dead(os.path.join(sys_root, "block", name, "md"))
    return True

# ----------------------------
# The storage gone while mounted (issue #207)
# ----------------------------
# Both disks of the mirror pulled while the device runs: md never fails
# the last disk of a raid1 - it calls the array broken (/proc/mdstat and
# array_state say so; every write fails from then on) and keeps the gone
# disk as an in-sync member - so md0 stayed, and its mount, with MariaDB
# aborting and the service answering I/O errors, until a restart. The
# single storage disk pulled: systemd unmounts it by itself and stops what
# needs it (the drop-ins), unless something still holds it.
#
# StorageGuard looks every STORAGE_LOOK_S from sysfs and mountinfo alone
# (no fork, nothing read from the disks): the mounted filesystem's device
# gone (the disk pulled), a mirror with no member left that holds its data
# (each one gone, offline, faulty or not in sync - a disk still being
# copied onto holds only part) or that md calls broken, the disk offline
# (the SCSI layer gave up on it); and a filesystem that turned itself
# read-only, or whose ext4 error count moved since it was mounted (from
# Linux 6.9 ext4's "emergency read-only" after an I/O error doesn't show
# in mountinfo), is read once (O_DIRECT, in a child that may hang on a
# dead disk) to see whether its device still answers. A mirror down to
# one working disk is not gone: it runs on, as before. Only a filesystem
# on a block device, whose fstab source names a device this can mount
# back, is judged at all: btrfs, ZFS, NFS, SMB and FUSE mounts show an
# anonymous 0:NN with nothing in sysfs behind it, and a share can't be
# mounted back from here.
#
# Then it takes the storage down: MariaDB and the service stopped
# (SIGTERM, killed after STORAGE_STOP_S - nothing they write reaches a
# disk any more, and InnoDB recovers from its log as after a power cut),
# the mount unmounted (lazily when something still holds it: the mount
# point is then the empty directory again, which the companion binds
# read-only), and the dead array stopped, so the disks are assembled
# afresh from their superblocks when they come back - udev's mdadm
# --incremental picks the newest, leaves out a disk that dropped out
# first (its events are older), and raid_watch re-adds that one as a
# former member, copied from the newer. Nothing is ever written under the
# mount point, and no disk is added to a dead array. The companion then
# starts at once (it is no slow disk at boot: the grace is for that).
STORAGE_STOP_S = 30            # SIGTERM, then SIGKILL
STORAGE_UMOUNT_S = 20          # a umount or mdadm --stop given up after this
STORAGE_PROBE_EVERY_S = 60     # a suspect filesystem's device read again at most this often
# md's array_state while the array runs (mdadm's "readonly" too).
_MD_RUNNING = ("clean", "active", "write-pending", "active-idle", "readonly", "read-auto")
_DISK_OFFLINE = ("offline", "transport-offline")

def disk_there(block):
    """A block device's sysfs directory (or a link to it) is still there and
    its disk isn't offline."""
    real = os.path.realpath(block)
    if not os.path.isdir(real):
        return False
    disk = os.path.dirname(real) if os.path.exists(os.path.join(real, "partition")) else real
    return _read(os.path.join(disk, "device", "state")) not in _DISK_OFFLINE

def md_members(md_dir):
    """The members of the md array whose sysfs md/ directory is md_dir, as
    {name: (state flags, disk there)}."""
    members = {}
    try:
        names = sorted(e for e in os.listdir(md_dir) if e.startswith("dev-"))
    except OSError:
        return members
    for name in names:
        flags = set(_read(os.path.join(md_dir, name, "state")).split(","))
        members[name[len("dev-"):]] = (flags, disk_there(os.path.join(md_dir, name, "block")))
    return members

def md_dead(md_dir):
    """Why the md array at md_dir (its sysfs md/ directory) holds no data
    any more, or "": md calls it broken (its last disk failed: every write
    fails), or it runs and no member is left in sync, not faulty, with its
    disk there. An array not running (inactive: still being gathered, or
    nothing at md_dir) is not dead, nor one with a disk left."""
    if not os.path.isdir(md_dir):
        return ""
    state = _read(os.path.join(md_dir, "array_state"))
    if state == "broken":
        return "md calls it broken (its last disk failed)"
    if state not in _MD_RUNNING:
        return ""
    members = md_members(md_dir)
    if any("in_sync" in flags and "faulty" not in flags and there for flags, there in members.values()):
        return ""
    if not members:
        return "it has no disks left"
    said = []
    for name, (flags, there) in sorted(members.items()):
        said.append(f"{name} " + ("gone" if not there else ("faulty" if "faulty" in flags else "not in sync")))
    return "no disk of it is left (" + ", ".join(said) + ")"

def probe_read(majmin, timeout=10):
    """One 4 KiB read from the start of the block device major:minor,
    straight from the disk (O_DIRECT): "" when it answers, else why not.
    In a child given up after timeout seconds: a read from a dead disk may
    hang for minutes in the kernel."""
    if not re.fullmatch(r"\d+:\d+", majmin):
        return "no such device"
    r = run_bounded(["dd", f"if=/dev/block/{majmin}", "of=/dev/null", "bs=4096", "count=1",
                     "iflag=direct", "status=none"], timeout)
    if r.returncode == 0:
        return ""
    err = (r.stderr or "").strip()
    return err.splitlines()[-1] if err else f"dd failed ({r.returncode})"

def ext4_errors(sys_root, devname):
    """ext4's own count of errors on the filesystem of devname and when it
    last saw one (/sys/fs/ext4/<dev>/), or None for another filesystem."""
    base = os.path.join(sys_root, "fs", "ext4", devname)
    if not os.path.isdir(base):
        return None
    return (_read(os.path.join(base, "errors_count")), _read(os.path.join(base, "last_error_time")))

def storage_gone(mount_point, mountinfo_text, fstab_options="", sys_root="/sys", probe=None, fs_errors=None):
    """Why the filesystem mounted at mount_point can't be used any more, or
    "" (see "The storage gone while mounted" above). probe is called with
    the device's major:minor only when the filesystem turned itself
    read-only (not when fstab mounts it so) or, fs_errors(mount id,
    device name) being true, has reported errors since it was mounted:
    since Linux 6.9 ext4 goes read-only on an I/O error without saying so
    in mountinfo (its "emergency read-only").

    Only a filesystem on a block device is judged: btrfs, ZFS, NFS, SMB
    and FUSE show an anonymous device (major 0) in mountinfo, with nothing
    under /sys/dev/block to judge it by - taken for gone, a healthy btrfs
    storage was stopped and mounted again every few seconds, and an NFS one
    never came back."""
    top = mount_on_top(mount_point, mountinfo_text)
    if top is None:
        return ""
    majmin, source, superopts, mount_id = top
    if majmin.split(":")[0] == "0":
        return ""
    dev_dir = os.path.join(sys_root, "dev", "block", majmin)
    if not os.path.exists(dev_dir):
        return f"its device {source or majmin} is gone"
    real = os.path.realpath(dev_dir)
    md_dir = os.path.join(real, "md")
    if os.path.isdir(md_dir):
        why = md_dead(md_dir)
        if why:
            return f"the mirror {os.path.basename(real)}: {why}"
    elif not disk_there(real):
        return f"its disk {source or majmin} is offline"
    if probe is not None:
        ro = "ro" in superopts.split(",") and "ro" not in fstab_options.split(",")
        if ro or (fs_errors is not None and fs_errors(mount_id, os.path.basename(real))):
            err = probe(majmin)
            if err:
                what = "it turned read-only" if ro else "its filesystem reported errors"
                return f"{what} and {source or majmin} can't be read ({err})"
    return ""

# ----------------------------
# A stopped array's disks, assembled again (issue #207)
# ----------------------------
# The dead array is stopped so that its disks, plugged in again, are
# assembled afresh by udev's mdadm --incremental. Disks back before the
# stop send no further event: a USB brownout drops both for a second, they
# come back while md still holds the broken array (their incremental add
# fails against it), and once it is stopped nothing assembled them - the
# device stayed without its storage with both disks plugged in, until a
# reboot or a replug. So while fstab's array isn't in /proc/mdstat at all,
# the disks nothing holds that carry its superblock (its UUID, from
# mdadm.conf) are handed to udev again as if plugged in (udevadm trigger
# --action=add): the same mdadm --incremental - the newest disk wins, one
# that dropped out first is re-added and copied from it - and mdadm's
# last-resort start for a mirror that comes back with one disk. An add
# event, not a change: udev's md rule in trixie's mdadm (4.4, the image's)
# skips a bare disk's change event before the incremental assembly ("Bare
# disks are ready when add event happens"); bookworm's 4.2 ran it for
# both, so add works on every release's OS. At once after the stop;
# otherwise (at boot, a disk plugged in) only once it has been apart 30 s,
# which udev's own assembly takes far less than; then 30 s doubling to 10
# min while it stays apart.
#
# Which array that is goes by fstab's source (StorageGuard._md_name):
# /dev/mdN as install.sh writes it, or the array a UUID= line (what this
# script's own setup writes), a LABEL= line or one of udev's links names
# while it is there. A stopped array takes its node and udev's links with
# it, so the name is remembered from the last time the source named it,
# and a guard that never saw it (started while the array was apart) takes
# CONFIG's raid_dev when mdadm.conf has its ARRAY line.
ASSEMBLE_FIRST_S, ASSEMBLE_MAX_S = 30, 600

def source_may_name_array(source):
    """fstab's source may be an md array's while nothing answers to it: its
    filesystem's UUID= or LABEL=, or a udev link (/dev/md/<name>,
    /dev/disk/by-uuid, by-label, by-id/md-...). Not a disk's own name
    (/dev/sdb1) nor a partition's PARTUUID=/PARTLABEL=."""
    kind, eq, _ = source.partition("=")
    if eq and kind in ("UUID", "LABEL"):
        return True
    return source.startswith(("/dev/md/", "/dev/disk/by-uuid/", "/dev/disk/by-label/", "/dev/disk/by-id/md-"))

def md_uuid_hex(text):
    """A UUID as 32 lowercase hex digits (mdadm's 1111aaaa:2222bbbb:...,
    blkid's dashed form), or "" when it isn't one."""
    h = re.sub(r"[^0-9a-fA-F]", "", text or "").lower()
    return h if len(h) == 32 else ""

def conf_array_uuid(conf_text, name):
    """The UUID mdadm.conf's ARRAY line gives /dev/<name> (or /dev/md/<N>),
    as md_uuid_hex; "" when it names none."""
    want = {"/dev/" + name}
    if name.startswith("md") and name[2:].isdigit():
        want.add("/dev/md/" + name[2:])
    for line in conf_text.splitlines():
        f = line.split()
        if len(f) < 2 or f[0] != "ARRAY" or f[1] not in want:
            continue
        for word in f[2:]:
            k, eq, v = word.partition("=")
            if eq and k.upper() == "UUID":
                return md_uuid_hex(v)
    return ""

def unheld_disks(sys_root="/sys", proc_root="/proc"):
    """The whole disks (sd*) with a card or disk in them that nothing holds
    (no array or volume, mount or swap on them: never the system's), each
    followed by its partitions."""
    out = []
    try:
        names = sorted(e for e in os.listdir(os.path.join(sys_root, "block")) if WHOLE_DISK.fullmatch(e))
    except OSError:
        return out
    for name in names:
        if not disk_bytes(name, sys_root) or disk_held(name, sys_root, proc_root):
            continue
        out.append(name)
        out.extend(partitions_of(name, sys_root))
    return out

def run_bounded(cmd, timeout):
    """run(), given up after timeout seconds: the command is killed and left
    behind if it doesn't end (stuck in the kernel on a dead disk), so the
    loop - and the LEDs - go on. Exit code 124 when given up."""
    try:
        p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    except Exception as e:
        return subprocess.CompletedProcess(cmd, 1, "", str(e))
    try:
        out, err = p.communicate(timeout=timeout)
        return subprocess.CompletedProcess(cmd, p.returncode, out, err)
    except subprocess.TimeoutExpired:
        p.kill()
        try:
            out, err = p.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            out, err = "", ""
        return subprocess.CompletedProcess(cmd, 124, out or "", f"{(err or '').strip()} (given up after {timeout} s)".strip())

def unit_stopped(unit):
    """Nothing of the unit runs: inactive or failed - not while it is still
    stopping (deactivating)."""
    r = run(["systemctl", "is-active", unit])
    return (r.stdout or "").strip() in ("inactive", "failed", "unknown", "")

def mount_unit_name(mount_point):
    """systemd's unit for the mount point: /mnt/storage -> mnt-storage.mount."""
    r = run(["systemd-escape", "--path", "--suffix=mount", mount_point])
    name = (r.stdout or "").strip() if r.returncode == 0 else ""
    if name.endswith(".mount"):
        return name
    return os.path.normpath(mount_point).strip("/").replace("-", "\\x2d").replace("/", "-") + ".mount"

def unit_active(unit):
    return run(["systemctl", "is-active", "--quiet", unit]).returncode == 0

def write_storage_state(since, path=None):
    """What the device's degraded mode reads for when the outage began
    (nostorage.MissingSince); None removes it."""
    path = path or STORAGE_STATE
    if since is None:
        try:
            os.remove(path)
        except FileNotFoundError:
            pass
        except OSError as e:
            print(f"[raid-watch] could not remove {path}: {e}")
        return
    try:
        os.makedirs(os.path.dirname(path), mode=0o755, exist_ok=True)
        tmp = f"{path}.tmp.{os.getpid()}"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
        try:
            os.write(fd, json.dumps({"v": 1, "missing_since": int(since)}).encode())
        finally:
            os.close(fd)
        os.replace(tmp, path)
    except OSError as e:
        print(f"[raid-watch] could not write {path}: {e}")

def system_uptime():
    try:
        return float(Path("/proc/uptime").read_text().split()[0])
    except (OSError, ValueError, IndexError):
        return 1e9

class StorageGuard:
    """See "The storage missing" and "The storage gone while mounted"
    above. step() is called every poll and looks every STORAGE_LOOK_S;
    state is "none" (no storage line in fstab: a plain directory, nothing
    to watch), "ok", "waiting" (missing, for less than its grace) or
    "missing" (the LEDs say so; also while a storage that went is taken
    down). dead_array is the md array the storage is on (_md_name) while
    it holds no data (md_dead): nothing is added to it, and it is stopped
    once nothing is mounted from it; while that array is then apart, its
    disks are handed to udev again (_assemble_again)."""

    def __init__(self, mount_point, fstab="/etc/fstab", mountinfo="/proc/self/mountinfo",
                 dev_root="/dev", sys_root="/sys", state_path=None, systemctl=None, active=None,
                 stopped=None, bounded=None, probe=None, read_mdstat=None,
                 mono=time.monotonic, wall=time.time, uptime=system_uptime, is_block=None,
                 mdadm_conf="/etc/mdadm/mdadm.conf", proc_root="/proc"):
        self.mount_point = mount_point
        self.fstab, self.mountinfo, self.dev_root, self.sys_root = fstab, mountinfo, dev_root, sys_root
        self.mdadm_conf, self.proc_root = mdadm_conf, proc_root
        self.state_path = state_path or STORAGE_STATE
        self.systemctl = systemctl or (lambda *args: run(["systemctl", *args]))
        self.active = active or unit_active
        self.stopped = stopped or unit_stopped
        self.bounded = bounded or run_bounded
        self.probe = probe or probe_read
        self.read_mdstat = read_mdstat
        self.mono, self.wall, self.uptime = mono, wall, uptime
        self.is_block = is_block
        self.state = "none"
        self.unit = None
        self.seen_mounted = False  # once this boot, as far as this process knows
        self.down = None           # the storage being taken down (_begin_down)
        self.dead_array = None
        # (fstab's source, the md array it named - None for a disk) when
        # the source last named a device (_md_name).
        self.md_seen = None
        self.md_stop_at, self.md_stop_delay = 0.0, 0
        self.probed = {}           # major:minor -> when a suspect filesystem's device was last read
        self.fs_seen = {}          # {"id": mount id, "errors": ext4_errors when first seen}
        self._reset()
        self.next_look = 0.0

    def _reset(self):
        self.since = None          # wall time the storage went missing
        self.since_mono = None
        self.grace = STORAGE_GRACE_S
        self.present = False
        self.mount_at, self.mount_delay = 0.0, 0
        self.degraded_at, self.degraded_delay = 0.0, 0
        self.told = False
        # When fstab's array, apart, is next handed to udev (None: not seen apart).
        self.assemble_at, self.assemble_delay, self.assemble_said = None, 0, None

    def _read(self, path):
        try:
            return Path(path).read_text()
        except OSError:
            return ""

    def _probe(self, majmin):
        """probe, at most every STORAGE_PROBE_EVERY_S for a device: a
        filesystem read-only for another reason (errors on a disk that
        still answers) is not read at every look."""
        mono = self.mono()
        last = self.probed.get(majmin)
        if last is not None and mono - last < STORAGE_PROBE_EVERY_S:
            return ""
        self.probed[majmin] = mono
        return self.probe(majmin)

    def _fs_errors(self, mount_id, devname):
        """The mounted filesystem has reported errors since this mount was
        first seen (ext4's count or time moved; an old count left in its
        superblock is not taken for a new one)."""
        now = ext4_errors(self.sys_root, devname)
        if now is None:
            return False
        if self.fs_seen.get("id") != mount_id:
            self.fs_seen = {"id": mount_id, "errors": now}
        return now != self.fs_seen["errors"]

    def _array_of(self, source, mdstat_text):
        """The running md array the storage is on (_md_name), while it is
        dead; else None. /proc/mdstat says "broken" for an array whose last
        disk failed (both pulled), "active" for one that runs. By the
        name: udev may drop a by-uuid link once blkid can't read the array
        any more."""
        name = self._md_name(source)
        if not name or not re.search(rf"^{name}\s*:\s*(active|broken)\b", mdstat_text, re.M):
            return None
        return name if md_dead(os.path.join(self.sys_root, "block", name, "md")) else None

    def step(self, mdstat_text):
        mono = self.mono()
        if mono < self.next_look:
            return self.state
        self.next_look = mono + STORAGE_LOOK_S
        entry = storage_entry(self._read(self.fstab), self.mount_point)
        if entry is None:
            if self.since is not None:
                write_storage_state(None, self.state_path)
            self._reset()
            self.down = self.dead_array = None
            self.state = "none"
            return self.state
        source = entry[0]
        self.dead_array = self._array_of(source, mdstat_text)
        info = self._read(self.mountinfo)
        # Taken down only what can be mounted back (source_names_device):
        # a share or a FUSE source would stay unmounted for good.
        if self.down is None and source_names_device(source) and whole_fs_mounted(self.mount_point, info):
            why = storage_gone(self.mount_point, info, entry[2], self.sys_root, self._probe, self._fs_errors)
            if why:
                self._begin_down(why, mono)
        if self.down is not None:
            if not self._down_step(mono):
                self.state = "missing"
                return self.state
            info = self._read(self.mountinfo)
        if whole_fs_mounted(self.mount_point, info):
            # Back (or mounted for the first time this boot): MariaDB and
            # the service are started - their start at boot may have been
            # given up, or cancelled by the companion's. A start of what
            # is already starting or running changes nothing. And the
            # companion never runs over a mounted storage, whoever
            # mounted it (this script restarted meanwhile, a hand).
            start = False
            if self.since is not None:
                print(f"[raid-watch] {self.mount_point} is mounted, after {int(mono - self.since_mono)} s without it: starting MariaDB and the service")
                write_storage_state(None, self.state_path)
                start = True
            elif mono >= self.degraded_at:
                self.degraded_at = mono + STORAGE_DEGRADED_S
                if self.active(DEGRADED_UNIT):
                    print(f"[raid-watch] {self.mount_point} is mounted and {DEGRADED_UNIT} runs: starting MariaDB and the service")
                    start = True
            if start:
                r = self.systemctl("start", "--no-block", *FULL_UNITS)
                if r.returncode != 0:
                    print(f"[raid-watch] could not start {' and '.join(FULL_UNITS)}: {(r.stderr or '').strip()}")
            degraded_at = self.degraded_at
            self.seen_mounted = True
            self._reset()
            self.degraded_at = degraded_at if not start else mono + STORAGE_DEGRADED_S
            self.state = "ok"
            return self.state

        # A dead array under the storage is stopped, now that nothing is
        # mounted from it: what the kernel holds of it is of no use, and
        # its disks, plugged in again, are assembled afresh from their own
        # superblocks - by udev for disks plugged in from now on, and here
        # for those already back (_assemble_again).
        if self.dead_array:
            self._stop_array(mono)
            mdstat_text = (self.read_mdstat or mdstat)()
        self._assemble_again(mono, mdstat_text, source)
        present = source_present(source, mdstat_text, self.dev_root, self.is_block, self.sys_root)
        if self.since is None:
            # Missing since the boot when the device has only just started
            # and it hasn't been seen mounted since. Seen mounted and gone
            # with its device (the single disk pulled, which systemd
            # unmounts by itself), the apps are told at once.
            up = self.uptime()
            if up < 5 * STORAGE_GRACE_S and not self.seen_mounted:
                self.since, self.since_mono = self.wall() - up, mono - up
            else:
                self.since, self.since_mono = self.wall(), mono
            if self.seen_mounted and not present:
                self.grace = 0
            self.degraded_at, self.degraded_delay = 0.0, 0
            print(f"[raid-watch] {self.mount_point} is not mounted (from {source})"
                  + (": its device is gone" if self.seen_mounted and not present else ""))
        if present and not self.present:
            self.mount_at, self.mount_delay = 0.0, 0   # just plugged in: at once
        self.present = present
        if present and mono >= self.mount_at:
            if self.unit is None:
                self.unit = mount_unit_name(self.mount_point)
            print(f"[raid-watch] {source} is there: mounting {self.mount_point} ({self.unit})")
            r = self.systemctl("start", "--no-block", self.unit)
            self.mount_delay = min(max(30, 2 * self.mount_delay), 600)
            self.mount_at = mono + self.mount_delay
            if r.returncode != 0:
                print(f"[raid-watch] could not mount it: {(r.stderr or '').strip()} (again in {self.mount_delay} s)")

        if mono - self.since_mono < self.grace:
            self.state = "waiting"
            return self.state
        self.state = "missing"
        if not self.told:
            self.told = True
            print(f"[raid-watch] no storage for {int(mono - self.since_mono)} s: the device tells the apps ({DEGRADED_UNIT})")
            write_storage_state(self.since, self.state_path)
        if mono >= self.degraded_at:
            self.degraded_at = mono + STORAGE_DEGRADED_S
            # Never over a service that runs: with its storage gone systemd
            # stops it, and one still running is answering by itself.
            if not self.active("otc.service") and not self.active(DEGRADED_UNIT):
                r = self.systemctl("start", "--no-block", DEGRADED_UNIT)
                if r.returncode == 0:
                    self.degraded_delay = 0
                else:
                    self.degraded_delay = min(max(STORAGE_DEGRADED_S, 2 * self.degraded_delay), 600)
                    self.degraded_at = mono + self.degraded_delay
                    print(f"[raid-watch] could not start {DEGRADED_UNIT}: {(r.stderr or '').strip()} (again in {self.degraded_delay} s)")
        return self.state

    def _begin_down(self, why, mono):
        """The mounted storage can't be used: MariaDB and the service are
        asked to stop, and _down_step takes it from there. The outage
        starts now, and the apps are told as soon as it is unmounted."""
        print(f"[raid-watch] {self.mount_point} can't be used any more: {why}. "
              "Stopping the service and MariaDB, then unmounting it")
        self.since, self.since_mono = self.wall(), mono
        self.grace = 0
        self.degraded_at, self.degraded_delay = 0.0, 0
        self.down = {"phase": "stop", "until": mono + STORAGE_STOP_S, "killed": False, "said": False}
        r = self.systemctl("stop", "--no-block", *FULL_UNITS)
        if r.returncode != 0:
            print(f"[raid-watch] could not stop {' and '.join(FULL_UNITS)}: {(r.stderr or '').strip()}")

    def _down_step(self, mono):
        """One look at the storage being taken down; True once it is no
        longer mounted. MariaDB and the service first (killed after
        STORAGE_STOP_S; 10 s after that the mount goes all the same), then
        the mount (lazily when it is busy)."""
        d = self.down
        if d["phase"] == "stop":
            running = [u for u in FULL_UNITS if not self.stopped(u)]
            if running and mono < d["until"]:
                return False
            if running and not d["killed"]:
                print(f"[raid-watch] {' and '.join(running)} still running after {STORAGE_STOP_S} s: killing it")
                r = self.systemctl("kill", "--signal=SIGKILL", *running)
                if r.returncode != 0:
                    print(f"[raid-watch] could not kill it: {(r.stderr or '').strip()}")
                d["killed"], d["until"] = True, mono + 10
                return False
            if running:
                print(f"[raid-watch] {' and '.join(running)} still not stopped (stuck on the disk): unmounting all the same")
            else:
                print("[raid-watch] the service and MariaDB are stopped")
            d["phase"] = "unmount"
        if whole_fs_mounted(self.mount_point, self._read(self.mountinfo)):
            r = self.bounded(["umount", self.mount_point], STORAGE_UMOUNT_S)
            if r.returncode != 0:
                print(f"[raid-watch] could not unmount {self.mount_point} ({(r.stderr or '').strip()}): detaching it (umount --lazy)")
                r = self.bounded(["umount", "--lazy", self.mount_point], STORAGE_UMOUNT_S)
            if r.returncode != 0 and whole_fs_mounted(self.mount_point, self._read(self.mountinfo)):
                if not d["said"]:
                    d["said"] = True
                    print(f"[raid-watch] could not detach {self.mount_point}: {(r.stderr or '').strip()} (tried again every {STORAGE_LOOK_S} s)")
                return False
            print(f"[raid-watch] {self.mount_point} unmounted")
        self.down = None
        return True

    def _md_name(self, source):
        """The md array the storage is on, by fstab's source: /dev/mdN (as
        install.sh writes it), or the array the source names now. Nothing
        answering to it (a stopped array takes its node and udev's links
        with it): the array it named when last seen, or - for a guard that
        never saw it, and a source that may be an array's
        (source_may_name_array) - CONFIG's raid_dev when mdadm.conf has its
        ARRAY line. None for a storage on a disk."""
        m = re.fullmatch(r"/dev/(md\d+)", source)
        if m:
            return m.group(1)
        real = source_device(source, self.dev_root, self.is_block)
        if real is not None:
            name = os.path.basename(real)
            self.md_seen = (source, name if re.fullmatch(r"md\d+", name) else None)
            return self.md_seen[1]
        if self.md_seen is not None and self.md_seen[0] == source:
            return self.md_seen[1]
        if source_may_name_array(source):
            name = raid_name_from_dev(CONFIG["raid_dev"])
            if re.fullmatch(r"md\d+", name) and conf_array_uuid(self._read(self.mdadm_conf), name):
                return name
        return None

    def _assemble_again(self, mono, mdstat_text, source):
        """fstab's array not in /proc/mdstat at all: the disks nothing holds
        that carry its superblock are handed to udev again (see "A stopped
        array's disks, assembled again" above) - at once after this guard
        stopped it, else once it has been apart ASSEMBLE_FIRST_S - then 30 s
        doubling to 10 min while it stays apart."""
        name = self._md_name(source)
        if not name or array_in_mdstat(mdstat_text, name):
            self.assemble_at, self.assemble_delay, self.assemble_said = None, 0, None
            return
        if self.assemble_at is None:
            # Seen apart (at boot, a disk just plugged in): udev is given
            # ASSEMBLE_FIRST_S to assemble it itself. After this guard's own
            # mdadm --stop it is at once.
            self.assemble_at = mono + ASSEMBLE_FIRST_S
        if mono < self.assemble_at:
            return
        self.assemble_delay = min(max(ASSEMBLE_FIRST_S, 2 * self.assemble_delay), ASSEMBLE_MAX_S)
        self.assemble_at = mono + self.assemble_delay
        uuid = conf_array_uuid(self._read(self.mdadm_conf), name)
        if not uuid:
            return
        members = []
        for dev in unheld_disks(self.sys_root, self.proc_root):
            r = self.bounded([CONFIG["paths"]["mdadm"], "--examine", "--export", "/dev/" + dev], 10)
            if r.returncode == 0 and md_uuid_hex(kv_lines(r.stdout).get("MD_UUID", "")) == uuid:
                members.append("/dev/" + dev)
        if not members:
            return
        if members != self.assemble_said:
            self.assemble_said = members
            print(f"[raid-watch] /dev/{name} isn't assembled, and {', '.join(members)} carry its superblock: "
                  "asking udev to assemble it again, as when they are plugged in (udevadm trigger --action=add)")
        # add, not change: trixie's md rule skips a bare disk's change event.
        r = self.bounded(["udevadm", "trigger", "--action=add", *members], 20)
        if r.returncode != 0:
            print(f"[raid-watch] udevadm trigger failed: {(r.stderr or '').strip()} (again in {self.assemble_delay} s)")

    def _stop_array(self, mono):
        """mdadm --stop for the dead array, again 10 s doubling to 5 min
        while it fails (something still holds the filesystem a lazy
        unmount detached)."""
        if mono < self.md_stop_at:
            return
        name = self.dead_array
        if not self.md_stop_delay:
            why = md_dead(os.path.join(self.sys_root, "block", name, "md"))
            print(f"[raid-watch] stopping /dev/{name}, which holds no data any more ({why}): "
                  "its disks are assembled again from their own superblocks once they are back")
        r = self.bounded([CONFIG["paths"]["mdadm"], "--stop", f"/dev/{name}"], STORAGE_UMOUNT_S)
        if r.returncode == 0:
            print(f"[raid-watch] /dev/{name} stopped")
            self.dead_array = None
            self.md_stop_at, self.md_stop_delay = 0.0, 0
            # Its disks already back are handed to udev at once.
            self.assemble_at, self.assemble_delay, self.assemble_said = 0.0, 0, None
            return
        self.md_stop_delay = min(max(10, 2 * self.md_stop_delay), 300)
        self.md_stop_at = mono + self.md_stop_delay
        try:
            holders = sorted(os.listdir(os.path.join(self.sys_root, "block", name, "holders")))
        except OSError:
            holders = []
        print(f"[raid-watch] could not stop /dev/{name}: {(r.stderr or '').strip()}"
              + (f" (held by {', '.join(holders)})" if holders else "")
              + f" (again in {self.md_stop_delay} s)")

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
        # The storage missing: both red in turn, a second each - slower than
        # a rebuild's blink, and never both lit as with two failed disks.
        self.turns = BlinkState(2.0)
        # Current desired modes: "solid_green", "solid_red", "blink_green",
        # "blink_red", "turns_red" (the storage missing), "off"
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
            elif mode == "turns_red":
                first = self.turns.step() if slot == 0 else self.turns.on
                led_set(slot, red_on=first if slot == 0 else not first, green_on=False)
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
    # Issue #207: the storage not mounted - mounted once its device is
    # there, the apps told after a minute, the service started once back.
    guard = StorageGuard(CONFIG["mount_point"])

    def handle_sigterm(sig, frame):
        if _TERM["busy"]:
            # Answered first (handle_use_request), then this ends.
            _TERM["pending"] = True
            return
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
    # "offered": the disks left alone that Settings may offer, by their
    # offer id, as the last look at them found them (offer_fields).
    seen = {"disks": None, "disks_at": 0.0, "kernel": None, "kernel_at": 0.0,
            "kernel_failed": False, "why": {}, "progress_at": 0.0, "repair_at": -DISKS_EVERY_S,
            "offered": {}}
    # (disk, size, model, port, attachment) -> judge_disk's verdict, while it
    # is plugged in.
    verdicts = {}
    # The last mdadm --detail, the sysfs it was read with, and when; after
    # one given up (a member that doesn't answer), not before "retry_at".
    md_cache = {"fp": None, "at": 0.0, "detail": None, "retry_at": 0.0}
    # Since when md0 has been running (active in /proc/mdstat), as this
    # loop saw it: the repair and the grow wait REPAIR_SETTLE_S after it
    # started (see running_settled).
    md_running = {"since": None}

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

    # The whole poll, every poll_s, for the device's life (keep_running
    # runs it again after anything it raises).
    def loop():
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
            settled = running_settled(md_running, mdst, raid_name, time.monotonic())
            storage_state = guard.step(mdst)
            # Settings: a disk left alone, to be erased and added at the
            # owner's word - everything checked again first (use_disk).
            if os.path.lexists(USE_REQUEST):
                if handle_use_request(seen["offered"], raid_dev):
                    seen["offered"] = {}
                    md_cache.update(fp=None, at=0.0, detail=None, retry_at=0.0)
                    mdst = mdstat()
                if _TERM["pending"]:
                    print("[raid-watch] SIGTERM received. Cleaning up GPIO.")
                    gpio_cleanup()
                    raise SystemExit(0)
            # The fallback is what a failed `mdadm --detail` parses to.
            # mdadm only when md's sysfs changed, or every MDADM_EVERY_S.
            # One given up (it reads a member's superblock, which a disk that
            # stopped answering never returns) leaves the last answer, and
            # mdadm isn't asked again for MDADM_EVERY_S, whatever changes.
            if array_present:
                fp, mono_md = md_fingerprint(raid_name), time.monotonic()
                if (fp is None or fp != md_cache["fp"] or md_cache["detail"] is None
                        or mono_md - md_cache["at"] >= MDADM_EVERY_S) and mono_md >= md_cache["retry_at"]:
                    got = mdadm_detail(raid_dev)
                    if got is None:
                        print(f"[raid-watch] mdadm --detail {raid_dev} gave no answer in {SUPERBLOCK_READ_S} s "
                              f"(a disk that doesn't answer?): the last answer stands, asked again in {MDADM_EVERY_S} s")
                        got = md_cache["detail"] or {"state": "", "members": {}, "other_devices": set()}
                        md_cache["retry_at"] = mono_md + MDADM_EVERY_S
                    else:
                        md_cache["retry_at"] = 0.0
                    md_cache.update(fp=fp, at=mono_md, detail=got)
                detail = md_cache["detail"] or {"state": "", "members": {}, "other_devices": set()}
            else:
                md_cache.update(fp=None, at=0.0, detail=None, retry_at=0.0)
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

            # The copy's source: a member md counts in sync (no disk read).
            source_slot = rebuild_source_slot(members, rebuilding_slot) if rebuilding else None

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
                    # Source (in sync) -> blink green
                    src = source_slot if source_slot is not None else (0 if rebuilding_slot == 1 else 1)
                    led.set_mode(src, "blink_green")
                else:
                    # Healthy + in-sync
                    # mdadm reports "clean", "active", etc. Without rebuild markers.
                    led.set_mode(0, "solid_green")
                    led.set_mode(1, "solid_green")

            # The storage missing says so whatever the disks say: the mirror
            # may be whole and still not mounted.
            if storage_state == "missing":
                led.set_mode(0, "turns_red")
                led.set_mode(1, "turns_red")

            # Apply LED states (updates blinkers)
            led.apply()

            # Auto-repair path. Looked at every DISKS_EVERY_S, and at once
            # when the disks or the mirror change: it forks lsblk per disk,
            # and a disk's verdict (judge_disk) is kept while it stays.
            repair_due = changed[0] or disks_changed or mono - seen["repair_at"] >= DISKS_EVERY_S
            # A slot with no working disk: never while a replacement is
            # being copied onto (md says degraded until that ends), when a
            # disk plugged in would only be a spare.
            needs_disk = mirror_needs_disk(members)
            # Only into a mirror that runs, and has for REPAIR_SETTLE_S:
            # never while udev is still gathering its disks (inactive: the
            # disk that came back first may be the newer one, and adding it
            # then would copy the older one over it), and never into one
            # with no disk left that holds its data (both pulled: nothing to
            # copy from; its disks are assembled afresh once it is stopped).
            if CONFIG["auto_repair_enable"] and settled and guard.dead_array != raid_name:
                # If degraded and exactly one member present, try to find a candidate
                if needs_disk and repair_due:
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
                    # ...and its verdict: what is plugged in next is judged
                    # anew, even under the same name and size (the kernel
                    # numbers each attachment).
                    for k in [k for k in verdicts if (k[0], k[1]) not in present
                              or k[4] != disk_seq(os.path.basename(k[0]))]:
                        del verdicts[k]
                    seen_by_name = {"/dev/" + name: (model, where) for name, _sz, model, where in (seen["disks"] or disks_seen())}
                    uuid = []

                    def judge(d, sz):
                        key = (d, sz) + seen_by_name.get(d, ("", "")) + (disk_seq(os.path.basename(d)),)
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
                    # For the device's Alert, one per disk plugged in, and
                    # for Settings, which may offer it to be erased and added
                    # - not while something holds it.
                    sizes_now = dict(disks)
                    not_added = []
                    offered = {}
                    for d in refused:
                        model, where = seen_by_name.get(d, ("", ""))
                        sz = sizes_now.get(d, 0)
                        extra = ", ".join(x for x in (model, where) if x)
                        what = f"{os.path.basename(d)} {human_size(sz)}" + (f" ({extra})" if extra else "")
                        held = disk_held(os.path.basename(d))
                        fields, offer = offer_fields(os.path.basename(d), sz, model, held=held)
                        not_added.append({"id": f"{os.path.basename(d)}|{sz}|{model}|{where}", "disk": what, **fields})
                        if offer:
                            offered[fields["offer"]] = dict(offer, what=what)
                        if held and d in why:
                            why[d] = why[d].split(" - it was left alone.")[0] + (
                                f", and {held} - it was left alone, and Settings doesn't offer it while it is in use")
                    if why != seen["why"]:
                        for d, reason in sorted(why.items()):
                            if seen["why"].get(d) != reason:
                                print(f"[raid-watch] {d} is not added to the mirror: {reason}")
                        seen["why"] = why
                    write_not_added(not_added)
                    seen["offered"] = offered
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
                elif not needs_disk:
                    failed_adds.clear()
                    verdicts.clear()
                    seen["why"] = {}
                    seen["offered"] = {}
                    write_not_added([])
            else:
                failed_adds.clear()
                verdicts.clear()
                if seen["why"]:
                    seen["why"] = {}
                seen["offered"] = {}
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
            if settled and "degraded" not in state and not rebuilding and mono >= grow_retry["check"]:
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

    try:
        keep_running(loop)
    finally:
        gpio_cleanup()

def keep_running(loop, sleep=time.sleep, mono=time.monotonic):
    """Runs loop() - the watcher's poll, forever - and runs it again after
    anything it raises: one unexpected answer used to end the process, and
    systemd restarting it into the same one could leave the unit failed by
    its start limit - no LEDs, no StorageGuard, no repair - until a reboot.
    Each new failure is printed with its traceback (later ones of the same
    kind are counted); the pause before running again doubles from 1 s to
    30 s while failures keep coming, back to 1 s after a minute without
    one. A stop (SystemExit, KeyboardInterrupt) ends it, as does loop()
    returning."""
    import traceback
    delay, last_at, seen = 0, None, {}
    while True:
        try:
            loop()
            return
        except Exception as e:
            at = mono()
            if last_at is not None and at - last_at > 60:
                delay = 0
            last_at = at
            what = f"{type(e).__name__}: {e}"
            if what not in seen and len(seen) >= 100:
                seen.clear()
            seen[what] = seen.get(what, 0) + 1
            n = seen[what]
            if n == 1:
                print("[raid-watch] the watcher's poll failed and starts again: " + traceback.format_exc().strip())
            elif n in (10, 100, 1000) or n % 10000 == 0:
                print(f"[raid-watch] the watcher's poll failed again ({n} times): {what}")
            delay = min(max(1, 2 * delay), 30)
            sleep(delay)

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
    selftest_storage()
    selftest_storage_gone()
    selftest_assemble()
    selftest_bounded()
    selftest_setup_finish()
    print("selftest ok")

def selftest_disks():
    """Which disk may join the mirror (judge_disk, pick_candidate_disk), the
    storage setup's own checks, and the file the device's Alert reads."""
    import tempfile
    global run, ends_blank, has_partitions
    real = (run, ends_blank, has_partitions)
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
        return uniform(e[0]) and uniform(e[1])

    run, ends_blank, has_partitions = fake_run, fake_ends, lambda dev, sys_root="/sys": disks[dev].get("parts", False)
    try:
        got = {d: judge_disk(d, uuid, members) for d in disks}
    finally:
        run, ends_blank, has_partitions = real
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
    assert why["/dev/sdb"].startswith("it holds exfat - it was left alone. To use it for the mirror, choose it in Settings"), why
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
    selftest_use()

def selftest_storage():
    """Issue #207: the storage missing - fstab's line, what counts as
    mounted and as its device being there, StorageGuard's steps on a fake
    clock and systemctl, and the LEDs' pattern for it."""
    import tempfile
    import io
    import contextlib
    fstab = ("# a comment\nPARTUUID=1-02 / ext4 defaults 0 1\n"
             "/dev/md0   /mnt/storage   ext4   defaults,nofail,x-systemd.device-timeout=3min   0   0\n"
             "/dev/sdz1 /mnt/backup ext4 noauto 0 0\n/swapfile none swap sw 0 0\n")
    assert storage_entry(fstab, "/mnt/storage") == ("/dev/md0", "ext4", "defaults,nofail,x-systemd.device-timeout=3min")
    assert storage_entry(fstab, "/mnt/storage/") is not None
    assert storage_entry(fstab, "/mnt/backup") is None          # noauto
    assert storage_entry("# /dev/md0 /mnt/storage ext4 defaults 0 0\n", "/mnt/storage") is None
    assert storage_entry("UUID=ab-cd /mnt/my\\040disk ext4 nofail 0 2\n", "/mnt/my disk") == ("UUID=ab-cd", "ext4", "nofail")

    root = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"
    md = "31 22 9:0 / /mnt/storage rw,relatime shared:12 - ext4 /dev/md0 rw\n"
    bound = "40 22 179:2 /mnt/storage /mnt/storage ro,relatime - ext4 /dev/mmcblk0p2 rw\n"
    assert not whole_fs_mounted("/mnt/storage", root)
    assert whole_fs_mounted("/mnt/storage", root + md)
    assert not whole_fs_mounted("/mnt/storage", root + bound)   # otc-degraded's read-only bind
    assert whole_fs_mounted("/mnt/storage", root + bound + md.replace("31 22", "41 40"))

    active = "Personalities : [raid1]\nmd0 : active raid1 sdb[1] sda[0]\n      976630464 blocks [2/2] [UU]\n"
    inactive = "Personalities : [raid1]\nmd0 : inactive sda[0](S)\n      976630464 blocks\n"
    with tempfile.TemporaryDirectory() as tmp:
        dev = os.path.join(tmp, "dev")
        os.makedirs(os.path.join(dev, "disk", "by-uuid"))
        for name in ("md0", "sdb1"):
            Path(dev, name).write_text("")
        os.symlink("../../sdb1", os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        blocks = {os.path.realpath(os.path.join(dev, "md0")), os.path.realpath(os.path.join(dev, "sdb1"))}
        is_block = lambda path: path in blocks
        sys_root = os.path.join(tmp, "sys")
        _fake_md(sys_root)                                                    # md0 on sda + sdb, in sync
        assert source_present("/dev/md0", active, dev, is_block, sys_root)
        assert not source_present("/dev/md0", inactive, dev, is_block, sys_root)       # half assembled
        assert not source_present("/dev/md0", "Personalities : [raid1]\n", dev, is_block, sys_root)
        assert source_present("UUID=ab-cd", "", dev, is_block, sys_root)
        assert not source_present("UUID=ef-01", "", dev, is_block, sys_root)
        assert not source_present("/dev/sdc1", "", dev, is_block, sys_root)
        assert not source_present("server:/export", "", dev, is_block, sys_root)

        fstab_path = os.path.join(tmp, "fstab")
        info_path = os.path.join(tmp, "mountinfo")
        state = os.path.join(tmp, "run", "storage.json")
        Path(fstab_path).write_text(fstab)
        Path(info_path).write_text(root)
        clock = {"mono": 1000.0, "wall": 1_800_000_000.0}
        calls = []
        units = {"otc.service": False, DEGRADED_UNIT: False}

        class R:
            def __init__(self, rc=0, err=""):
                self.returncode, self.stdout, self.stderr = rc, "", err

        def systemctl(*args):
            calls.append(args)
            return R()

        def guard_with(uptime):
            g = StorageGuard("/mnt/storage", fstab=fstab_path, mountinfo=info_path, dev_root=dev,
                             sys_root=sys_root, state_path=state, systemctl=systemctl, active=lambda u: units[u],
                             mono=lambda: clock["mono"], wall=lambda: clock["wall"], uptime=lambda: uptime,
                             is_block=is_block, mdadm_conf=os.path.join(tmp, "mdadm.conf"))
            g.unit = "mnt-storage.mount"
            return g

        def step(g, mdstat, seconds=0):
            clock["mono"] += seconds
            clock["wall"] += seconds
            with contextlib.redirect_stdout(io.StringIO()) as out:
                st = g.step(mdstat)
            return st, out.getvalue()

        # Missing from boot (up 10 s): not before a minute since the boot,
        # nothing to mount while md0 isn't there.
        g = guard_with(uptime=10)
        st, said = step(g, inactive)
        assert st == "waiting" and "/mnt/storage is not mounted" in said and calls == [], (st, said, calls)
        assert step(g, inactive, 1)[0] == "waiting"       # looked at most every STORAGE_LOOK_S
        st, said = step(g, inactive, 49)                   # 10 + 50 = 60 s since the boot
        assert st == "missing" and calls == [("start", "--no-block", DEGRADED_UNIT)], (st, calls)
        assert json.loads(Path(state).read_text()) == {"v": 1, "missing_since": int(1_800_000_000 - 10)}
        # Running: not started again; never over a service that runs.
        units[DEGRADED_UNIT] = True
        del calls[:]
        step(g, inactive, STORAGE_DEGRADED_S)
        units[DEGRADED_UNIT], units["otc.service"] = False, True
        step(g, inactive, STORAGE_DEGRADED_S)
        assert calls == [], calls
        units["otc.service"] = False
        # The mirror assembled: mounted at once, and again only after 30 s.
        st, said = step(g, active, STORAGE_DEGRADED_S)
        assert ("start", "--no-block", "mnt-storage.mount") in calls and "is there: mounting" in said, (calls, said)
        n = len([c for c in calls if c[-1] == "mnt-storage.mount"])
        step(g, active, 10)
        assert len([c for c in calls if c[-1] == "mnt-storage.mount"]) == n
        step(g, active, 25)
        assert len([c for c in calls if c[-1] == "mnt-storage.mount"]) == n + 1
        # Mounted: MariaDB and the service started, the state removed.
        Path(info_path).write_text(root + md)
        del calls[:]
        st, said = step(g, active, STORAGE_LOOK_S)
        assert st == "ok" and calls == [("start", "--no-block") + FULL_UNITS], (st, calls)
        assert "is mounted, after" in said and not os.path.exists(state), said
        step(g, active, STORAGE_LOOK_S)
        assert len(calls) == 1
        # The companion still running over the mounted storage (this
        # script restarted meanwhile): the service is started, which stops it.
        units[DEGRADED_UNIT] = True
        g = guard_with(uptime=86400)
        del calls[:]
        st, said = step(g, active, 1)
        assert st == "ok" and calls == [("start", "--no-block") + FULL_UNITS], calls
        step(g, active, STORAGE_LOOK_S)
        assert len(calls) == 1                              # again only every STORAGE_DEGRADED_S
        units[DEGRADED_UNIT] = False
        step(g, active, STORAGE_DEGRADED_S)
        assert len(calls) == 1

        # A slow mount at boot (20 s): the units the boot is starting are
        # asked for again, which changes nothing.
        Path(info_path).write_text(root)
        g = guard_with(uptime=5)
        del calls[:]
        step(g, inactive)
        Path(info_path).write_text(root + md)
        st, said = step(g, active, 20)
        assert st == "ok" and calls == [("start", "--no-block") + FULL_UNITS], calls
        # Unmounted later in that boot: a minute from then, not from the boot.
        Path(info_path).write_text(root)
        assert step(g, active, 30)[0] == "waiting"
        assert step(g, active, 58)[0] == "waiting"
        assert step(g, active, 2)[0] == "missing"
        Path(info_path).write_text(root + md)

        # Missing long after the boot (unmounted by hand): a minute from
        # then; the device there all along is mounted again.
        Path(info_path).write_text(root)
        g = guard_with(uptime=86400)
        del calls[:]
        st, _ = step(g, active, 1)
        assert st == "waiting" and calls == [("start", "--no-block", "mnt-storage.mount")], calls
        st, _ = step(g, active, 60)
        assert st == "missing" and calls[-1] == ("start", "--no-block", DEGRADED_UNIT), calls
        assert json.loads(Path(state).read_text())["missing_since"] == int(clock["wall"] - 60)

        # A companion that can't start (no unit yet): backoff, said once a try.
        def failing(*args):
            calls.append(args)
            return R(5, "Unit otc-degraded.service not found.")
        g = guard_with(uptime=86400)
        g.systemctl = failing
        del calls[:]
        step(g, inactive, 1)
        st, said = step(g, inactive, 60)
        assert st == "missing" and "could not start" in said and "again in 10 s" in said, said
        step(g, inactive, 9)
        assert len([c for c in calls if c[-1] == DEGRADED_UNIT]) == 1
        step(g, inactive, 2)
        assert len([c for c in calls if c[-1] == DEGRADED_UNIT]) == 2

        # No storage line (OTC_SKIP_RAID): nothing to watch.
        Path(fstab_path).write_text("PARTUUID=1-02 / ext4 defaults 0 1\n")
        g = guard_with(uptime=10)
        del calls[:]
        assert step(g, inactive, 100)[0] == "none" and calls == []

    # The LEDs: red in turn, never both at once, never green.
    seen = []
    real = (led_set, globals()["now"])
    t = [0.0]
    globals()["led_set"] = lambda slot, red_on, green_on: seen.append((slot, red_on, green_on))
    globals()["now"] = lambda: t[0]
    try:
        led = LedController(0.1)
        led.set_mode(0, "turns_red")
        led.set_mode(1, "turns_red")
        frames = []
        for i in range(8):
            del seen[:]
            led.apply()
            frames.append(tuple(seen))
            t[0] += 0.5
        for f in frames:
            (s0, r0, g0), (s1, r1, g1) = f
            assert (s0, s1) == (0, 1) and r0 != r1 and not g0 and not g1, f
        lit = [f[0][1] for f in frames]
        assert lit == [True, True, False, False, True, True, False, False], lit   # a second each
    finally:
        globals()["led_set"], globals()["now"] = real

def _fake_md(sys_root, name="md0", majmin="9:0", members=("sda", "sdb"), state="clean"):
    """A sysfs tree with the md array `name` (its /sys/dev/block link too)
    on the USB disks `members`, each in sync, for the selftests. Pulling a
    disk is removing devices/usb/<disk>: the member's block link then
    leads nowhere, as in the kernel."""
    md_dev = os.path.join(sys_root, "devices", "virtual", "block", name)
    os.makedirs(os.path.join(md_dev, "md"), exist_ok=True)
    Path(md_dev, "md", "array_state").write_text(state + "\n")
    for m in members:
        disk = os.path.join(sys_root, "devices", "usb", m)
        os.makedirs(os.path.join(disk, "device"), exist_ok=True)
        Path(disk, "device", "state").write_text("running\n")
        d = os.path.join(md_dev, "md", f"dev-{m}")
        os.makedirs(d, exist_ok=True)
        Path(d, "state").write_text("in_sync\n")
        if not os.path.lexists(os.path.join(d, "block")):
            os.symlink(disk, os.path.join(d, "block"))
    for where in (os.path.join(sys_root, "block", name), os.path.join(sys_root, "dev", "block", majmin)):
        os.makedirs(os.path.dirname(where), exist_ok=True)
        if not os.path.lexists(where):
            os.symlink(md_dev, where)
    return os.path.join(md_dev, "md")

def selftest_storage_gone():
    """Issue #207, the rest: the storage gone while mounted - both disks of
    the mirror pulled, the single disk pulled, a filesystem that turned
    read-only on a disk that doesn't answer - on a fake sysfs, and
    StorageGuard taking it down and bringing it back on a fake clock,
    systemctl, umount and mdadm. A mirror down to one disk runs on."""
    import tempfile
    import io
    import shutil
    import contextlib

    # Disks are added only to a mirror that has run for REPAIR_SETTLE_S:
    # never while udev gathers the disks plugged back in (seen in the VM:
    # the newer disk was picked while md0 was still inactive).
    seen = {"since": None}
    act = "md0 : active raid1 sdb[1] sda[0]\n"
    assert not running_settled(seen, "md0 : inactive sda[0](S)\n", "md0", 100) and seen["since"] is None
    assert not running_settled(seen, act, "md0", 100) and seen["since"] == 100
    assert not running_settled(seen, act, "md0", 100 + REPAIR_SETTLE_S - 1)
    assert running_settled(seen, act, "md0", 100 + REPAIR_SETTLE_S)
    assert not running_settled(seen, "md0 : broken raid1 sdb[1]\n", "md0", 200) and seen["since"] is None
    assert not running_settled(seen, "unused devices: <none>\n", "md0", 201)
    assert not running_settled(seen, act, "md0", 202)

    # mountinfo: optional fields or none, and the superblock's options.
    line = "31 22 9:0 / /mnt/storage rw,relatime shared:12 master:3 - ext4 /dev/md0 rw,errors=remount-ro\n"
    assert mount_on_top("/mnt/storage", line) == ("9:0", "/dev/md0", "rw,errors=remount-ro", "31")
    assert mount_on_top("/mnt/storage", line.replace(" shared:12 master:3", "")) == ("9:0", "/dev/md0", "rw,errors=remount-ro", "31")
    bound = "40 22 179:2 /mnt/storage /mnt/storage ro,relatime - ext4 /dev/mmcblk0p2 rw\n"
    assert mount_on_top("/mnt/storage", line + bound) is None       # a bind on top is not the storage
    assert mount_on_top("/mnt/storage", "") is None

    root = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"
    md_line = "31 22 9:0 / /mnt/storage rw,relatime shared:12 - ext4 /dev/md0 rw\n"
    one_line = "31 22 8:17 / /mnt/storage rw,nosuid,nodev,relatime shared:12 - ext4 /dev/sdb1 rw\n"
    with tempfile.TemporaryDirectory() as tmp:
        sys_root = os.path.join(tmp, "sys")
        md = _fake_md(sys_root)
        usb = os.path.join(sys_root, "devices", "usb")

        def pull(disk):
            shutil.rmtree(os.path.join(usb, disk))

        def plug(disk):
            os.makedirs(os.path.join(usb, disk, "device"), exist_ok=True)
            Path(usb, disk, "device", "state").write_text("running\n")

        def member(disk, state):
            Path(md, f"dev-{disk}", "state").write_text(state + "\n")

        # md_dead: both in sync; one out (faulty, or gone from the array) runs on.
        assert md_dead(md) == ""
        member("sda", "faulty")
        assert md_dead(md) == ""
        pull("sda")
        shutil.rmtree(os.path.join(md, "dev-sda"))                  # mdadm -If took it out
        assert md_dead(md) == ""
        assert storage_gone("/mnt/storage", root + md_line, "defaults", sys_root) == ""
        # ...and the last one pulled too: md keeps it, in sync, with no disk.
        pull("sdb")
        why = md_dead(md)
        assert why == "no disk of it is left (sdb gone)", why
        assert storage_gone("/mnt/storage", root + md_line, "defaults", sys_root) == \
            "the mirror md0: no disk of it is left (sdb gone)"
        plug("sdb")
        assert md_dead(md) == ""
        Path(usb, "sdb", "device", "state").write_text("offline\n")  # the SCSI layer gave up on it
        assert md_dead(md) != ""
        Path(usb, "sdb", "device", "state").write_text("running\n")
        Path(md, "array_state").write_text("broken\n")               # its last disk failed
        assert md_dead(md) == "md calls it broken (its last disk failed)"
        Path(md, "array_state").write_text("inactive\n")             # being gathered: not dead
        assert md_dead(md) == ""
        Path(md, "array_state").write_text("active\n")
        # A replacement being copied onto holds only part: with the source gone, nothing is left.
        plug("sdc")
        os.makedirs(os.path.join(md, "dev-sdc"))
        Path(md, "dev-sdc", "state").write_text("\n")
        os.symlink(os.path.join(usb, "sdc"), os.path.join(md, "dev-sdc", "block"))
        assert md_dead(md) == ""
        pull("sdb")
        assert md_dead(md) == "no disk of it is left (sdb gone, sdc not in sync)", md_dead(md)
        assert md_dead(os.path.join(tmp, "nothing")) == ""
        shutil.rmtree(os.path.join(md, "dev-sdc"))
        plug("sda")
        plug("sdb")
        _fake_md(sys_root)                                          # both back in md0, in sync
        assert md_dead(md) == ""

        # The single disk: its partition's device gone, or its disk offline.
        part = os.path.join(usb, "sdd", "sdd1")
        os.makedirs(os.path.join(usb, "sdd", "device"))
        Path(usb, "sdd", "device", "state").write_text("running\n")
        os.makedirs(part)
        Path(part, "partition").write_text("1\n")
        os.symlink(part, os.path.join(sys_root, "dev", "block", "8:17"))
        assert storage_gone("/mnt/storage", root + one_line, "defaults", sys_root) == ""
        Path(usb, "sdd", "device", "state").write_text("offline\n")
        assert storage_gone("/mnt/storage", root + one_line, "defaults", sys_root) == "its disk /dev/sdb1 is offline"
        shutil.rmtree(os.path.join(usb, "sdd"))
        assert storage_gone("/mnt/storage", root + one_line, "defaults", sys_root) == "its device /dev/sdb1 is gone"
        assert storage_gone("/mnt/storage", root, "defaults", sys_root) == ""      # not mounted: not this check
        # No block device behind it (an anonymous 0:NN in mountinfo, nothing
        # in /sys/dev/block): btrfs, NFS, FUSE are never judged - not even
        # read when they turn read-only.
        btrfs = "31 22 0:45 / /mnt/storage rw,relatime shared:12 - btrfs /dev/sdb1 rw,space_cache=v2,subvolid=5,subvol=/\n"
        nfs = "31 22 0:52 / /mnt/storage rw,relatime shared:12 - nfs4 server:/export rw,vers=4.2,addr=192.168.1.2\n"
        fuse = "31 22 0:60 / /mnt/storage rw,nosuid,nodev,relatime - fuse.mergerfs pool rw,user_id=0,group_id=0\n"
        read = []
        for line in (btrfs, nfs, fuse, btrfs.replace(" rw,space_cache", " ro,space_cache")):
            assert storage_gone("/mnt/storage", root + line, "defaults", sys_root, lambda m: read.append(m) or "I/O error",
                                lambda mount_id, dev: True) == "", line
        assert read == [], read
        assert source_names_device("UUID=ab-cd") and source_names_device("PARTLABEL=data") and source_names_device("/dev/md0")
        assert not source_names_device("server:/export") and not source_names_device("//nas/share")
        assert not source_names_device("pool/data") and not source_names_device("sshfs#me@host:/x")

        # Read-only by itself: read once; an answer means it is not gone.
        ro = md_line.replace("ext4 /dev/md0 rw", "ext4 /dev/md0 ro")
        asked = []
        def probe(majmin, answer=""):
            asked.append(majmin)
            return answer
        assert storage_gone("/mnt/storage", root + ro, "defaults", sys_root, probe) == "" and asked == ["9:0"]
        assert storage_gone("/mnt/storage", root + ro, "defaults", sys_root,
                            lambda m: "error reading '/dev/block/9:0': Input/output error") == \
            "it turned read-only and /dev/md0 can't be read (error reading '/dev/block/9:0': Input/output error)"
        del asked[:]
        assert storage_gone("/mnt/storage", root + ro, "ro,nofail", sys_root, probe) == "" and asked == []
        assert storage_gone("/mnt/storage", root + md_line, "defaults", sys_root, probe) == "" and asked == []
        # Errors reported since the mount (Linux 6.9 on: ext4 goes read-only
        # without "ro" in mountinfo): read once too.
        assert storage_gone("/mnt/storage", root + md_line, "defaults", sys_root, lambda m: "Input/output error",
                            lambda mount_id, dev: (mount_id, dev) == ("31", "md0")) == \
            "its filesystem reported errors and /dev/md0 can't be read (Input/output error)"
        assert storage_gone("/mnt/storage", root + md_line, "defaults", sys_root, lambda m: "Input/output error",
                            lambda mount_id, dev: False) == ""
        assert ext4_errors(sys_root, "md0") is None

        # StorageGuard: both disks pulled while it runs.
        dev = os.path.join(tmp, "dev")
        os.makedirs(os.path.join(dev, "disk", "by-uuid"))
        Path(dev, "md0").write_text("")
        blocks = {os.path.realpath(os.path.join(dev, "md0"))}
        is_block = lambda path: path in blocks
        fstab_path = os.path.join(tmp, "fstab")
        info_path = os.path.join(tmp, "mountinfo")
        state_path = os.path.join(tmp, "run", "storage.json")
        Path(fstab_path).write_text("/dev/md0   /mnt/storage   ext4   defaults,nofail,x-systemd.device-timeout=3min   0   0\n")
        Path(info_path).write_text(root + md_line)
        active = "Personalities : [raid1]\nmd0 : active raid1 sdb[1] sda[0]\n      3142656 blocks [2/2] [UU]\n"
        degraded = "Personalities : [raid1]\nmd0 : active raid1 sdb[1]\n      3142656 blocks [2/1] [_U]\n"
        broken = "Personalities : [raid1]\nmd0 : broken raid1 sdb[1]\n      3142656 blocks [2/1] [_U]\n"   # as the kernel says it
        world = {"mdstat": active, "stopped": {"mariadb.service": False, "otc.service": False},
                 "active": {"otc.service": True, DEGRADED_UNIT: False}, "umount": [0], "mdstop": [0]}
        clock = {"mono": 5000.0, "wall": 1_800_000_000.0}
        calls = []

        class R:
            def __init__(self, rc=0, err=""):
                self.returncode, self.stdout, self.stderr = rc, "", err

        def systemctl(*args):
            calls.append(("systemctl",) + args)
            return R()

        def bounded(cmd, timeout):
            calls.append(tuple(os.path.basename(cmd[0]) if i == 0 else c for i, c in enumerate(cmd)))
            if os.path.basename(cmd[0]) == "umount":
                rc = world["umount"].pop(0) if world["umount"] else 0
                if rc == 0:
                    Path(info_path).write_text(root)
                return R(rc, "umount: /mnt/storage: target is busy." if rc else "")
            if cmd[1:] == ["--stop", "/dev/md0"]:
                rc = world["mdstop"].pop(0) if world["mdstop"] else 0
                if rc == 0:
                    world["mdstat"] = "Personalities : [raid1]\nunused devices: <none>\n"
                return R(rc, "mdadm: Cannot get exclusive access to /dev/md0" if rc else "")
            return R(1, "unexpected")

        def guard():
            return StorageGuard("/mnt/storage", fstab=fstab_path, mountinfo=info_path, dev_root=dev, sys_root=sys_root,
                                state_path=state_path, systemctl=systemctl, active=lambda u: world["active"][u],
                                stopped=lambda u: world["stopped"][u], bounded=bounded,
                                probe=lambda m: probe(m, world.get("probe", "")),
                                read_mdstat=lambda: world["mdstat"], mono=lambda: clock["mono"],
                                wall=lambda: clock["wall"], uptime=lambda: 86400, is_block=is_block,
                                mdadm_conf=os.path.join(tmp, "mdadm.conf"))

        def step(g, seconds=STORAGE_LOOK_S):
            clock["mono"] += seconds
            clock["wall"] += seconds
            with contextlib.redirect_stdout(io.StringIO()) as out:
                st = g.step(world["mdstat"])
            return st, out.getvalue()

        g = guard()
        g.unit = "mnt-storage.mount"
        assert step(g)[0] == "ok" and calls == []
        # One disk out: it runs on, nothing stopped.
        pull("sda")
        shutil.rmtree(os.path.join(md, "dev-sda"))
        world["mdstat"] = degraded
        for _ in range(5):
            assert step(g)[0] == "ok"
        assert calls == [] and g.dead_array is None
        # The other one too, with the service writing: taken down. md keeps
        # it as its member, in sync with no disk, and calls the array broken.
        pull("sdb")
        world["mdstat"] = broken
        st, said = step(g)
        pulled_at = clock["wall"]
        assert st == "missing" and "can't be used any more: the mirror md0: no disk of it is left (sdb gone)" in said, said
        assert calls == [("systemctl", "stop", "--no-block") + FULL_UNITS], calls
        assert g.dead_array == "md0"
        # Still stopping: nothing more, nothing unmounted, nothing added.
        for _ in range(int(STORAGE_STOP_S / STORAGE_LOOK_S) - 1):
            assert step(g)[0] == "missing"
        assert len(calls) == 1 and not os.path.exists(state_path), calls
        # MariaDB stopped, the service hangs: killed, then unmounted 10 s on.
        world["stopped"]["mariadb.service"] = True
        st, said = step(g)
        assert calls[-1] == ("systemctl", "kill", "--signal=SIGKILL", "otc.service") and "killing it" in said, (calls, said)
        world["stopped"]["otc.service"] = True
        world["active"]["otc.service"] = False
        world["umount"] = [32, 0]                                       # busy, then detached
        del calls[:]
        st, said = step(g)
        assert calls[:3] == [("umount", "/mnt/storage"), ("umount", "--lazy", "/mnt/storage"),
                             ("mdadm", "--stop", "/dev/md0")], calls
        assert "detaching it (umount --lazy)" in said and "/dev/md0 stopped" in said, said
        # ...and the companion at once, the outage's start written for it.
        assert st == "missing" and calls[3:] == [("systemctl", "start", "--no-block", DEGRADED_UNIT)], calls
        assert json.loads(Path(state_path).read_text()) == {"v": 1, "missing_since": int(pulled_at)}
        assert g.dead_array is None
        world["active"][DEGRADED_UNIT] = True
        del calls[:]
        for _ in range(10):
            assert step(g)[0] == "missing"
        assert calls == [], calls                                       # nothing mounted, nothing started
        # Back: udev assembles md0 from both, raid_watch mounts it and starts everything.
        plug("sda")
        plug("sdb")
        _fake_md(sys_root)
        member("sdb", "in_sync")
        world["mdstat"] = active
        st, said = step(g)
        assert calls == [("systemctl", "start", "--no-block", "mnt-storage.mount")] and "is there: mounting" in said, calls
        Path(info_path).write_text(root + md_line)
        del calls[:]
        st, said = step(g)
        assert st == "ok" and calls == [("systemctl", "start", "--no-block") + FULL_UNITS], calls
        assert not os.path.exists(state_path)

        # A dead array that can't be stopped yet (something still holds what
        # the lazy unmount detached): again 10 s doubling, never mounted.
        pull("sda")
        pull("sdb")
        world["mdstat"] = broken
        world["stopped"] = {"mariadb.service": True, "otc.service": True}
        world["umount"] = [0]
        world["mdstop"] = [1, 1, 0]
        del calls[:]
        st, said = step(g)
        assert calls[0] == ("systemctl", "stop", "--no-block") + FULL_UNITS
        assert ("mdadm", "--stop", "/dev/md0") in calls and "could not stop /dev/md0" in said and "again in 10 s" in said, (calls, said)
        assert ("systemctl", "start", "--no-block", "mnt-storage.mount") not in calls
        n = calls.count(("mdadm", "--stop", "/dev/md0"))
        step(g, 8)
        assert calls.count(("mdadm", "--stop", "/dev/md0")) == n
        st, said = step(g, 2)
        assert calls.count(("mdadm", "--stop", "/dev/md0")) == n + 1 and "again in 20 s" in said, said
        step(g, 20)
        assert calls.count(("mdadm", "--stop", "/dev/md0")) == n + 2 and g.dead_array is None
        assert ("systemctl", "start", "--no-block", "mnt-storage.mount") not in calls
        plug("sda")
        plug("sdb")
        world["mdstat"] = active
        step(g)
        Path(info_path).write_text(root + md_line)
        assert step(g)[0] == "ok"

        # Read-only on a disk that answers: read again only after a minute.
        Path(info_path).write_text(root + ro)
        del asked[:]
        for _ in range(int(STORAGE_PROBE_EVERY_S / STORAGE_LOOK_S)):
            assert step(g)[0] == "ok"
        assert asked == ["9:0"], asked
        step(g)
        assert asked == ["9:0", "9:0"], asked
        Path(info_path).write_text(root + md_line)

        # ext4 reporting errors: an old count is no news, a new one has the
        # disk read, and a disk that doesn't answer takes the storage down.
        ext4 = os.path.join(sys_root, "fs", "ext4", "md0")
        os.makedirs(ext4)
        Path(ext4, "errors_count").write_text("2\n")              # left from long ago
        Path(ext4, "last_error_time").write_text("1700000000\n")
        clock["mono"] += STORAGE_PROBE_EVERY_S
        del asked[:]
        assert step(g)[0] == "ok" and asked == [], asked
        assert step(g)[0] == "ok" and asked == []
        Path(ext4, "errors_count").write_text("3\n")
        assert step(g)[0] == "ok" and asked == ["9:0"], asked           # the disk answers: a filesystem error
        world["probe"] = "error reading '/dev/block/9:0': Input/output error"
        world["stopped"] = {"mariadb.service": True, "otc.service": True}
        del calls[:]
        st, said = step(g, STORAGE_PROBE_EVERY_S)
        assert st == "missing" and "its filesystem reported errors and /dev/md0 can't be read" in said, said
        assert calls[:2] == [("systemctl", "stop", "--no-block") + FULL_UNITS, ("umount", "/mnt/storage")], calls
        world["probe"] = ""
        shutil.rmtree(ext4)

        # The single disk pulled: systemd unmounted it, the companion at once.
        os.symlink("../../sdb1", os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        Path(dev, "sdb1").write_text("")
        blocks.add(os.path.realpath(os.path.join(dev, "sdb1")))
        Path(fstab_path).write_text("UUID=ab-cd   /mnt/storage   ext4   defaults,nosuid,nodev,nofail   0   0\n")
        os.makedirs(os.path.join(usb, "sdd", "sdd1"))
        Path(usb, "sdd", "sdd1", "partition").write_text("1\n")
        Path(info_path).write_text(root + one_line)
        world["active"] = {"otc.service": True, DEGRADED_UNIT: False}
        g = guard()
        assert step(g)[0] == "ok"
        shutil.rmtree(os.path.join(usb, "sdd"))
        os.remove(os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        Path(info_path).write_text(root)
        world["active"]["otc.service"] = False
        del calls[:]
        st, said = step(g)
        assert st == "missing" and "its device is gone" in said and calls == [("systemctl", "start", "--no-block", DEGRADED_UNIT)], (st, said, calls)
        # Unmounted by hand with the disk still there: mounted again, no companion for a minute.
        os.symlink("../../sdb1", os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        os.makedirs(os.path.join(usb, "sdd", "sdd1"))
        Path(usb, "sdd", "sdd1", "partition").write_text("1\n")
        Path(info_path).write_text(root + one_line)
        assert step(g)[0] == "ok"
        Path(info_path).write_text(root)
        del calls[:]
        st, _ = step(g)
        assert st == "waiting" and calls == [("systemctl", "start", "--no-block", "mnt-storage.mount")], (st, calls)

        # Still mounted with its device gone (something held it): taken down.
        shutil.rmtree(os.path.join(usb, "sdd"))
        os.remove(os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        Path(info_path).write_text(root + one_line)
        world["stopped"] = {"mariadb.service": True, "otc.service": True}
        g = guard()
        del calls[:]
        st, said = step(g)
        assert st == "missing" and "its device /dev/sdb1 is gone" in said, said
        assert calls[:2] == [("systemctl", "stop", "--no-block") + FULL_UNITS, ("umount", "/mnt/storage")], calls
        assert ("mdadm", "--stop", "/dev/md0") not in calls

        # A storage with no block device behind it, mounted and in use: never
        # taken down. Before, a btrfs storage by UUID was stopped, unmounted,
        # mounted again and taken down 2 s later, for good; an NFS share was
        # unmounted once and never mounted again.
        for fstab_line, mounted in (
                ("UUID=ab-cd   /mnt/storage   btrfs   defaults,nofail   0   0\n", btrfs),
                ("server:/export   /mnt/storage   nfs4   defaults,nofail,_netdev   0   0\n", nfs),
                ("pool   /mnt/storage   fuse.mergerfs   defaults,nofail   0   0\n", fuse)):
            Path(fstab_path).write_text(fstab_line)
            Path(info_path).write_text(root + mounted)
            world["active"] = {"otc.service": True, DEGRADED_UNIT: False}
            g = guard()
            del calls[:]
            for _ in range(10):
                assert step(g)[0] == "ok"
            assert calls == [] and g.down is None, (fstab_line, calls)
        # A block device's mount whose fstab source can't be found again (a
        # share's name): nothing that could not be mounted back is unmounted.
        Path(fstab_path).write_text("server:/export   /mnt/storage   ext4   defaults,nofail   0   0\n")
        Path(info_path).write_text(root + one_line)
        g = guard()
        del calls[:]
        assert step(g)[0] == "ok" and calls == [], calls

def selftest_setup_finish():
    """First-sign-in storage setup cut short after the mount (blkid had no
    UUID yet, the card was full): the next try finishes it - the array in
    mdadm.conf, fstab's line and the drop-ins - instead of taking the mount
    for a finished setup, and touches nothing it didn't mount."""
    import tempfile
    import io
    import contextlib
    global run, _own_storage
    real = (run, _own_storage)
    uuid = "1111aaaa:2222bbbb:3333cccc:4444dddd"
    ran = []
    world = {"blkid": "0a1b2c3d-4e5f-6789-abcd-ef0123456789", "brief": f"ARRAY /dev/md0 metadata=1.2 name=otc:0 UUID={uuid}\n",
             "owned": True}
    def fake_own(mount_point, unenc_path):
        ran.append(f"own {mount_point} {unenc_path}")
        return world["owned"]
    def fake_run(cmd, timeout=None, **_kw):
        ran.append(" ".join([os.path.basename(cmd[0])] + list(cmd[1:])))
        name = os.path.basename(cmd[0])
        if name == "blkid":
            return subprocess.CompletedProcess(cmd, 0 if world["blkid"] else 2, world["blkid"] + "\n", "")
        if name == "mdadm" and cmd[1:3] == ["--detail", "--brief"]:
            return subprocess.CompletedProcess(cmd, 0 if world["brief"] else 1, world["brief"], "" if world["brief"] else "mdadm: cannot open")
        if name in ("update-initramfs", "systemctl"):
            return subprocess.CompletedProcess(cmd, 0, "", "")
        raise AssertionError(cmd)
    with tempfile.TemporaryDirectory() as tmp:
        mp = os.path.join(tmp, "storage")
        os.makedirs(mp)
        fstab, info, conf, units = (os.path.join(tmp, f) for f in ("fstab", "mountinfo", "mdadm.conf", "system"))
        root = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"
        base = "PARTUUID=1-02  /  ext4  defaults  0  1\n"
        def finish(paths, mounted_from):
            Path(info).write_text(root + (f"31 22 8:16 / {mp} rw,nosuid,nodev,relatime - ext4 {mounted_from} rw\n" if mounted_from else ""))
            del ran[:]
            with contextlib.redirect_stdout(io.StringIO()):
                return _finish_setup(paths, mp, "/dev/md0", info, fstab, conf, units)
        run, _own_storage = fake_run, fake_own
        try:
            # One disk, mounted, fstab never written (blkid had no UUID yet):
            # tried again until it can be, then the line and the drop-ins.
            Path(fstab).write_text(base)
            world["blkid"] = ""
            assert finish(["/dev/sdb"], "/dev/sdb") is False and Path(fstab).read_text() == base, ran
            assert ran[:2] == [f"own {mp} {os.path.join(mp, CONFIG['unenc_subdir'])}", "blkid -s UUID -o value /dev/sdb"], ran
            assert not [f for f in os.listdir(tmp) if f.startswith("fstab.otc.")]
            world["blkid"] = "0a1b2c3d-4e5f-6789-abcd-ef0123456789"
            # The storage not given to otc yet (that step failed): tried again too.
            world["owned"] = False
            assert finish(["/dev/sdb"], "/dev/sdb") is False and Path(fstab).read_text() == base, ran
            world["owned"] = True
            assert finish(["/dev/sdb"], "/dev/sdb") is True, ran
            assert Path(fstab).read_text() == base + f"UUID=0a1b2c3d-4e5f-6789-abcd-ef0123456789   {mp}   ext4   {STORAGE_MOUNT_OPTS}   0   0\n"
            for unit in STORAGE_DROPINS:
                assert f"RequiresMountsFor={mp}" in Path(units, f"{unit}.service.d", "otc-storage.conf").read_text()
            assert "systemctl daemon-reload" in ran, ran
            # Done: nothing more (the line is there).
            assert finish(["/dev/sdb"], "/dev/sdb") is True and ran == [], ran
            # Two disks: the mirror's ARRAY line first (mdadm.conf without it,
            # its append had failed), the initramfs, then fstab.
            Path(fstab).write_text(base)
            Path(conf).write_text("# mdadm.conf\nARRAY /dev/md1 metadata=1.2 UUID=9999eeee:0000ffff:1212abab:3434cdcd")
            assert finish(["/dev/sdb", "/dev/sdc"], "/dev/md0") is True, ran
            assert Path(conf).read_text().endswith(f"UUID=9999eeee:0000ffff:1212abab:3434cdcd\nARRAY /dev/md0 metadata=1.2 name=otc:0 UUID={uuid}\n")
            assert ran[:2] == ["mdadm --detail --brief /dev/md0", "update-initramfs -u"] and ran[2].startswith("own "), ran
            assert Path(fstab).read_text().count(f" {mp} ") == 1
            # mdadm.conf naming it already: nothing appended, no initramfs.
            Path(fstab).write_text(base)
            before = Path(conf).read_text()
            assert finish(["/dev/sdb", "/dev/sdc"], "/dev/md0") is True and Path(conf).read_text() == before, ran
            assert not any(r.startswith(("mdadm", "update-initramfs")) for r in ran) and any(r.startswith("own ") for r in ran), ran
            # Its line can't be read: tried again, fstab untouched.
            Path(fstab).write_text(base)
            Path(conf).write_text("")
            world["brief"] = ""
            assert finish(["/dev/sdb", "/dev/sdc"], "/dev/md0") is False and Path(fstab).read_text() == base
            # Not this setup's mount (another disk, the other kind), or not
            # mounted at all: nothing touched.
            for paths, mounted in ((["/dev/sdb"], "/dev/sdz"), (["/dev/sdb"], "/dev/md0"), (["/dev/sdb", "/dev/sdc"], "/dev/sdb"),
                                   ([], "/dev/sdb"), (["/dev/sdb"], "")):
                assert finish(paths, mounted) is True and ran == [] and Path(fstab).read_text() == base, (paths, mounted, ran)
        finally:
            run, _own_storage = real

        # The owner and modes, set on each directory opened without following
        # a link: a symlink planted where unencrypted/ goes is refused, and
        # what it points to keeps its mode. (As this user: the otc user is
        # stood in for by the one running the test.)
        import pwd
        import collections
        real_getpwnam = pwd.getpwnam
        me = collections.namedtuple("pw", "pw_uid pw_gid")(os.getuid(), os.getgid())
        pwd.getpwnam = lambda name: me if name == "otc" else real_getpwnam(name)
        try:
            new = os.path.join(tmp, "new")
            os.makedirs(new)
            with contextlib.redirect_stdout(io.StringIO()):
                assert _own_storage(new, os.path.join(new, "unencrypted")) is True
            assert stat.S_IMODE(os.stat(new).st_mode) == 0o751
            assert stat.S_IMODE(os.stat(os.path.join(new, "unencrypted")).st_mode) == 0o750
            elsewhere = os.path.join(tmp, "elsewhere")
            os.makedirs(elsewhere, mode=0o700)
            os.chmod(elsewhere, 0o700)
            planted = os.path.join(tmp, "planted")
            os.makedirs(planted)
            os.symlink(elsewhere, os.path.join(planted, "unencrypted"))
            with contextlib.redirect_stdout(io.StringIO()) as said:
                assert _own_storage(planted, os.path.join(planted, "unencrypted")) is False
            assert "could not give the new storage to the otc user" in said.getvalue(), said.getvalue()
            assert stat.S_IMODE(os.stat(elsewhere).st_mode) == 0o700
        finally:
            pwd.getpwnam = real_getpwnam

def selftest_bounded():
    """The loop never waits long on a disk, nor ends on one failure: a
    command given up, mdadm --detail given up, a disk's two ends read in a
    child that is given up, the rebuild's source from md's own state, and
    keep_running."""
    import tempfile
    import io
    import contextlib
    global run
    t = time.monotonic()
    r = run([sys.executable, "-c", "import time; time.sleep(30)"], timeout=0.5)
    assert r.returncode == 124 and "given up after 0.5 s" in r.stderr and time.monotonic() - t < 10, (r, time.monotonic() - t)
    assert run([sys.executable, "-c", "print('ok')"], timeout=10).stdout.strip() == "ok"

    real = run
    asked = []
    def fake(answer):
        def fake_run(cmd, timeout=None, **_kw):
            asked.append((os.path.basename(cmd[0]), timeout))
            return answer
        return fake_run
    detail = ("/dev/md0:\n             State : clean, degraded\n    Number   Major   Minor   RaidDevice State\n"
              "       0       8        0        0      active sync   /dev/sda\n")
    try:
        run = fake(subprocess.CompletedProcess([], 124, "", "(given up after 10 s)"))
        assert mdadm_detail("/dev/md0") is None and asked == [("mdadm", SUPERBLOCK_READ_S)], asked
        run = fake(subprocess.CompletedProcess([], 0, detail, ""))
        got = mdadm_detail("/dev/md0")
        assert got["state"] == "clean, degraded" and got["members"] == {0: {"device": "/dev/sda", "state": "active sync"}}, got
        run = fake(subprocess.CompletedProcess([], 1, "", "mdadm: cannot open /dev/md0"))
        assert mdadm_detail("/dev/md0") == {"state": "", "members": {}, "other_devices": set()}   # as it always parsed
    finally:
        run = real

    # A disk's two ends, read in a child: blank, data, can't be read, and a
    # read that never answers (a FIFO nobody writes: open waits) given up.
    with tempfile.TemporaryDirectory() as tmp:
        disk = os.path.join(tmp, "disk")
        Path(disk).write_bytes(b"\0" * (3 << 20))
        assert ends_blank(disk) is True
        Path(disk).write_bytes(b"\xff" * (1 << 20) + b"data" + b"\0" * (2 << 20))
        assert ends_blank(disk) is True                  # the middle isn't read: the ends hold nothing
        Path(disk).write_bytes(b"\0" * ((3 << 20) - 4) + b"tail")
        assert ends_blank(disk) is False
        try:
            ends_blank(os.path.join(tmp, "none"))
            raise AssertionError("a missing disk read as blank")
        except OSError as e:
            assert "No such file" in str(e.strerror), e
        fifo = os.path.join(tmp, "fifo")
        os.mkfifo(fifo)
        t = time.monotonic()
        try:
            ends_blank(fifo, timeout=1)
            raise AssertionError("a disk that never answers read as blank")
        except OSError as e:
            assert e.errno == errno.ETIMEDOUT and e.strerror == "no answer in 1 s", e
        assert time.monotonic() - t < 10

    # The rebuild's source: the member in sync, never the one being copied
    # onto - the old event comparison picked slot 0 on a tie, whichever it was.
    rebuilding = {0: {"device": "/dev/sdb", "state": "spare rebuilding"}, 1: {"device": "/dev/sda", "state": "active sync"}}
    assert rebuild_source_slot(rebuilding, 0) == 1
    assert rebuild_source_slot({0: {"state": "active sync"}, 1: {"state": "spare rebuilding"}}, 1) == 0
    assert rebuild_source_slot({0: {"state": "spare rebuilding"}, 1: {"state": "faulty"}}, 0) is None
    assert rebuild_source_slot({}, None) is None

    # keep_running: a failure is printed once with its traceback, counted
    # after, the poll started again after 1, 2, 4... s (up to 30), back to
    # 1 s after a minute without one; a stop ends it.
    clock = {"t": 0.0}
    slept = []
    script = iter([ValueError("x"), ValueError("x"), KeyError("y"), "pause", ValueError("x"), SystemExit(0)])
    def loop():
        step = next(script)
        if step == "pause":
            clock["t"] += 120
            step = next(script)
        raise step
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        try:
            keep_running(loop, sleep=slept.append, mono=lambda: clock["t"])
            raise AssertionError("keep_running ended without the stop")
        except SystemExit:
            pass
    assert slept == [1, 2, 4, 1], slept
    said = out.getvalue()
    assert said.count("the watcher's poll failed and starts again") == 2 and "Traceback" in said, said
    assert "ValueError: x" in said and "KeyError: 'y'" in said, said
    ran = []
    keep_running(lambda: ran.append(1), sleep=slept.append)
    assert ran == [1]

def selftest_assemble():
    """A dead array's disks that were back before it was stopped (a USB
    brownout: both gone for a second, back under new names while md still
    held the broken array) are handed to udev again, as when plugged in,
    once it is stopped, and while it stays apart - the disks nothing holds
    that carry its UUID, never another array's, a held one or an empty
    reader. fstab names the array /dev/md0 (install.sh) or by its
    filesystem's UUID (this script's own setup), whose link and node go
    with the stopped array."""
    import tempfile
    import io
    import shutil
    import contextlib
    uuid = "1111aaaa:2222bbbb:3333cccc:4444dddd"
    fs_uuid = "0a1b2c3d-4e5f-6789-abcd-ef0123456789"
    assert md_uuid_hex(uuid) == md_uuid_hex("1111AAAA-2222-BBBB-3333-CCCC4444DDDD") == "1111aaaa2222bbbb3333cccc4444dddd"
    assert md_uuid_hex("not a uuid") == "" and md_uuid_hex("") == ""
    conf = ("# mdadm.conf\nDEVICE partitions\nARRAY /dev/md1 metadata=1.2 UUID=9999eeee:0000ffff:1212abab:3434cdcd\n"
            f"ARRAY /dev/md0 metadata=1.2 name=otc:0 UUID={uuid}\n")
    assert conf_array_uuid(conf, "md0") == md_uuid_hex(uuid)
    assert conf_array_uuid(conf.replace("/dev/md0 ", "/dev/md/0 "), "md0") == md_uuid_hex(uuid)
    assert conf_array_uuid(conf, "md2") == "" and conf_array_uuid("", "md0") == ""
    for source in ("UUID=" + fs_uuid, "LABEL=otc", "/dev/md/0", "/dev/disk/by-uuid/" + fs_uuid,
                   "/dev/disk/by-label/otc", "/dev/disk/by-id/md-uuid-1111aaaa:2222bbbb:3333cccc:4444dddd"):
        assert source_may_name_array(source), source
    for source in ("/dev/sdb1", "PARTUUID=1-02", "PARTLABEL=data", "server:/export", "/dev/disk/by-id/usb-Disk-0:0"):
        assert not source_may_name_array(source), source

    root = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"
    md_line = "31 22 9:0 / /mnt/storage rw,relatime shared:12 - ext4 /dev/md0 rw\n"
    by_name = "/dev/md0   /mnt/storage   ext4   defaults,nofail,x-systemd.device-timeout=3min   0   0\n"
    by_uuid = f"UUID={fs_uuid}   /mnt/storage   ext4   {STORAGE_MOUNT_OPTS}   0   0\n"
    with tempfile.TemporaryDirectory() as tmp:
        sys_root, proc_root, dev = (os.path.join(tmp, d) for d in ("sys", "proc", "dev"))
        md = _fake_md(sys_root, members=("sdc", "sdd"))
        os.makedirs(os.path.join(proc_root, "self"))
        Path(proc_root, "self", "mountinfo").write_text(root)
        Path(proc_root, "swaps").write_text("Filename\tType\tSize\tUsed\tPriority\n")

        def disk(name, size=6291456, holders=()):
            base = os.path.join(sys_root, "block", name)
            os.makedirs(os.path.join(base, "holders"), exist_ok=True)
            Path(base, "size").write_text(f"{size}\n")
            Path(base, "dev").write_text(f"8:{len(os.listdir(os.path.join(sys_root, 'block'))) * 16}\n")
            for h in holders:
                Path(base, "holders", h).write_text("")
        os.makedirs(os.path.join(dev, "disk", "by-uuid"))
        md0_node, link = os.path.join(dev, "md0"), os.path.join(dev, "disk", "by-uuid", fs_uuid)
        blocks = set()
        md0_real = os.path.realpath(md0_node)

        def node(there):
            """md0's node and its filesystem's by-uuid link: udev makes them
            once md0 runs, and removes both once it is stopped."""
            if there:
                Path(md0_node).write_text("")
                if not os.path.lexists(link):
                    os.symlink("../../md0", link)
                blocks.add(md0_real)
            else:
                for p in (link, md0_node):
                    if os.path.lexists(p):
                        os.remove(p)
                blocks.discard(md0_real)
        node(True)
        fstab_path, info_path = os.path.join(tmp, "fstab"), os.path.join(tmp, "mountinfo")
        conf_path, state_path = os.path.join(tmp, "mdadm.conf"), os.path.join(tmp, "storage.json")
        Path(conf_path).write_text(conf)
        apart = "Personalities : [raid1]\nunused devices: <none>\n"
        running = "Personalities : [raid1]\nmd0 : active raid1 sdd[1] sdc[0]\n      3142656 blocks [2/2] [UU]\n"
        broken = "Personalities : [raid1]\nmd0 : broken raid1 sdd[1]\n      3142656 blocks [2/1] [_U]\n"
        world = {"mdstat": running, "superblocks": {}, "udev_assembles": True}
        clock = {"mono": 9000.0}
        calls = []

        class R:
            def __init__(self, rc=0, out="", err=""):
                self.returncode, self.stdout, self.stderr = rc, out, err

        def bounded(cmd, timeout):
            calls.append(tuple([os.path.basename(cmd[0])] + list(cmd[1:])))
            name = os.path.basename(cmd[0])
            if name == "umount":
                Path(info_path).write_text(root)
                return R()
            if name == "mdadm" and cmd[1:] == ["--stop", "/dev/md0"]:
                world["mdstat"] = apart
                node(False)
                return R()
            if name == "mdadm" and cmd[1:3] == ["--examine", "--export"]:
                sb = world["superblocks"].get(cmd[3])
                return R(0, f"MD_LEVEL=raid1\nMD_UUID={sb}\n") if sb else R(1, "", f"mdadm: No md superblock detected on {cmd[3]}.")
            if name == "udevadm":
                # udev's md rule as in trixie's mdadm 4.4: a bare disk's add
                # event runs mdadm --incremental - md0 again, from sde and
                # sdf - and its change event nothing ("Bare disks are ready
                # when add event happens").
                if world["udev_assembles"] and cmd[1:3] == ["trigger", "--action=add"]:
                    for gone in ("dev-sdc", "dev-sdd"):
                        shutil.rmtree(os.path.join(md, gone), ignore_errors=True)
                    _fake_md(sys_root, members=("sde", "sdf"))
                    world["mdstat"] = "Personalities : [raid1]\nmd0 : active raid1 sde[0] sdf[1]\n      3142656 blocks [2/2] [UU]\n"
                    node(True)
                return R()
            raise AssertionError(cmd)

        def guard(conf=conf_path):
            return StorageGuard("/mnt/storage", fstab=fstab_path, mountinfo=info_path, dev_root=dev, sys_root=sys_root,
                                state_path=state_path, systemctl=lambda *a: calls.append(("systemctl",) + a) or R(),
                                active=lambda u: False, stopped=lambda u: True, bounded=bounded, probe=lambda m: "",
                                read_mdstat=lambda: world["mdstat"], mono=lambda: clock["mono"], wall=lambda: clock["wall"],
                                uptime=lambda: 86400, is_block=lambda p: p in blocks, mdadm_conf=conf, proc_root=proc_root)
        clock["wall"] = clock["mono"]

        def step(g, seconds=STORAGE_LOOK_S):
            clock["mono"] += seconds
            clock["wall"] += seconds
            with contextlib.redirect_stdout(io.StringIO()) as out:
                st = g.step(world["mdstat"])
            return st, out.getvalue()

        # Plugged in after the brownout: the two disks (sde, sdf), a backup
        # disk of another array, one held by md127 and an empty card reader.
        for name in ("sde", "sdf", "sdg", "sdh"):
            disk(name)
        disk("sdi", holders=("md127",))
        disk("sdj", size=0)
        superblocks = {"/dev/sde": uuid, "/dev/sdf": uuid.upper(), "/dev/sdg": "9999eeee:0000ffff:1212abab:3434cdcd",
                       "/dev/sdi": uuid, "/dev/sdj": uuid}
        trigger = ("udevadm", "trigger", "--action=add", "/dev/sde", "/dev/sdf")
        for fstab_line in (by_name, by_uuid):
            # md0 runs on sdc and sdd, mounted; then both drop for a second:
            # md0 broken, its members gone, the disks back as sde and sdf
            # before md0 was stopped - and udev drops the filesystem's link
            # once blkid can't read md0 any more.
            shutil.rmtree(os.path.join(sys_root, "devices", "virtual", "block", "md0"))
            md = _fake_md(sys_root, members=("sdc", "sdd"))
            world.update(mdstat=running, superblocks={}, udev_assembles=True)
            node(True)
            Path(fstab_path).write_text(fstab_line)
            Path(info_path).write_text(root + md_line)
            g = guard()
            g.unit = "mnt-storage.mount"
            assert step(g)[0] == "ok", fstab_line
            shutil.rmtree(os.path.join(sys_root, "devices", "usb", "sdc"))
            shutil.rmtree(os.path.join(sys_root, "devices", "usb", "sdd"))
            assert md_dead(md) != ""
            world.update(mdstat=broken, superblocks=dict(superblocks))
            if fstab_line == by_uuid:
                os.remove(link)
            # Taken down (MariaDB and the service already stopped), md0 stopped,
            # and its disks handed to udev as plugged in, in the same look.
            # Before, nothing assembled them: no event came after the stop, and
            # the change event handed over later runs no assembly on trixie -
            # nor was anything handed over for a UUID= line, whose link had gone.
            del calls[:]
            st, said = step(g)
            assert st == "missing" and "can't be used any more" in said, (fstab_line, said)
            assert ("mdadm", "--stop", "/dev/md0") in calls and trigger in calls, (fstab_line, calls)
            assert calls.index(trigger) > calls.index(("mdadm", "--stop", "/dev/md0")), calls
            assert not any(c[:2] == ("mdadm", "--examine") and c[-1] in ("/dev/sdi", "/dev/sdj") for c in calls), calls
            assert "/dev/md0 isn't assembled, and /dev/sde, /dev/sdf carry its superblock" in said, said
            # udev assembled it: mounted at the next look, nothing triggered again.
            del calls[:]
            st, said = step(g)
            assert ("systemctl", "start", "--no-block", "mnt-storage.mount") in calls, (fstab_line, calls)
            assert not any(c[0] == "udevadm" for c in calls), calls

        # Seen apart by a guard that didn't stop it (a boot, a plug-in): udev
        # gets 30 s first; staying apart, again 30 s then 60 s later, said
        # once for the same disks; reset once md0 is back. A guard started
        # while it was apart never saw where a UUID= line led: CONFIG's
        # raid_dev, as mdadm.conf names it.
        for fstab_line in (by_name, by_uuid):
            world["mdstat"], world["udev_assembles"] = apart, False
            node(False)
            Path(fstab_path).write_text(fstab_line)
            Path(info_path).write_text(root)
            g = guard()
            g.unit = "mnt-storage.mount"
            times = []
            out = ""
            start = clock["mono"] + STORAGE_LOOK_S
            for _ in range(int(130 / STORAGE_LOOK_S)):
                del calls[:]
                _, said = step(g)
                out += said
                if trigger in calls:
                    times.append(clock["mono"])
            assert [round(t - start) for t in times] == [30, 60, 120], (fstab_line, times, start)
            assert out.count("carry its superblock") == 1, out
            world["mdstat"] = "Personalities : [raid1]\nmd0 : active raid1 sde[0] sdf[1]\n"
            step(g)
            assert g.assemble_delay == 0 and g.assemble_at is None
            # mdadm.conf naming no md0: nothing read, nothing triggered.
            world["mdstat"] = apart
            g = guard(conf=os.path.join(tmp, "none.conf"))
            del calls[:]
            for _ in range(5):
                step(g)
            assert not any(c[0] in ("mdadm", "udevadm") for c in calls), (fstab_line, calls)
        # Not an array: a single disk by UUID, seen there, then gone - nothing,
        # though mdadm.conf names an md0 that isn't assembled.
        Path(dev, "sdb1").write_text("")
        blocks.add(os.path.realpath(os.path.join(dev, "sdb1")))
        os.symlink("../../sdb1", os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        Path(fstab_path).write_text("UUID=ab-cd   /mnt/storage   ext4   defaults,nofail   0   0\n")
        g = guard()
        step(g)
        assert g.md_seen == ("UUID=ab-cd", None), g.md_seen
        os.remove(os.path.join(dev, "disk", "by-uuid", "ab-cd"))
        del calls[:]
        for _ in range(int(130 / STORAGE_LOOK_S)):
            step(g)
        assert not any(c[0] in ("mdadm", "udevadm") for c in calls), calls
        # Nor a disk's own name.
        Path(fstab_path).write_text("/dev/sdb1   /mnt/storage   ext4   defaults,nofail   0   0\n")
        os.remove(os.path.join(dev, "sdb1"))
        g = guard()
        del calls[:]
        for _ in range(int(130 / STORAGE_LOOK_S)):
            step(g)
        assert not any(c[0] in ("mdadm", "udevadm") for c in calls), calls

def selftest_use():
    """Settings' request to erase a disk left alone and add it: the
    request file, what is offered, and every check before anything is
    erased - with fakes for mdadm, wipefs, the mounts and sysfs."""
    import tempfile
    global run, mdstat, exclusive_seq, is_block_device
    real = (run, mdstat, exclusive_seq, is_block_device)
    assert mirror_needs_disk({0: {"state": "active sync"}})
    assert mirror_needs_disk({0: {"state": "active sync"}, 1: {"state": "faulty"}})
    assert not mirror_needs_disk({0: {"state": "active sync"}, 1: {"state": "spare rebuilding"}})
    assert not mirror_needs_disk({0: {"state": "active sync"}, 1: {"state": "active sync"}})

    with tempfile.TemporaryDirectory() as tmp:
        sys_root, proc_root = os.path.join(tmp, "sys"), os.path.join(tmp, "proc")
        def put(rel, value, root=sys_root):
            os.makedirs(os.path.dirname(os.path.join(root, rel)), exist_ok=True)
            Path(root, rel).write_text(value)
        # sdb: 512 GB, an exFAT partition on it, attachment 7; sda: the
        # mirror's working disk.
        size = 512_110_190_592
        for rel, value in (("block/sdb/size", str(size // 512)), ("block/sdb/diskseq", "7\n"), ("block/sdb/dev", "8:16\n"),
                           ("block/sdb/sdb1/dev", "8:17\n"), ("block/sda/size", str(size // 512)), ("block/sda/diskseq", "3")):
            put(rel, value)
        os.makedirs(os.path.join(sys_root, "block/sdb/holders"))
        os.makedirs(os.path.join(sys_root, "block/sdb/sdb1/holders"))
        mounts = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"
        put("self/mountinfo", mounts, proc_root)
        put("swaps", "Filename\tType\tSize\tUsed\tPriority\n/dev/zram0 partition 1 0 100\n", proc_root)

        # What is offered: a stable id for this attachment, none when
        # something holds the disk or the kernel doesn't number it.
        fields, offer = offer_fields("sdb", size, "Generic Card Reader", sys_root, proc_root)
        oid = fields.get("offer", "")
        assert OFFER_ID.fullmatch(oid) and fields["held"] is False and fields["model"] == "Generic Card Reader", fields
        assert offer == {"dev": "/dev/sdb", "seq": 7, "size": size}, offer
        assert offer_fields("sdb", size, "Generic Card Reader", sys_root, proc_root)[0]["offer"] == oid
        assert offer_id("sdb", 8, size) != oid and offer_id("sdc", 7, size) != oid
        assert offer_fields("sdq", size, "", sys_root, proc_root) == ({"model": "", "size": size, "held": False}, None)
        put("self/mountinfo", mounts + "40 22 8:17 / /media/backup rw - exfat /dev/sdb1 rw\n", proc_root)
        assert disk_held("sdb", sys_root, proc_root) == "sdb1 is mounted on /media/backup"
        assert offer_fields("sdb", size, "", sys_root, proc_root) == ({"model": "", "size": size, "held": True}, None)
        put("self/mountinfo", mounts, proc_root)
        put("block/sdb/sdb1/holders/md127", "")
        assert disk_held("sdb", sys_root, proc_root) == "sdb1 is part of an array or volume that is running"
        os.remove(os.path.join(sys_root, "block/sdb/sdb1/holders/md127"))
        put("swaps", "Filename\tType\tSize\tUsed\tPriority\n/dev/sdb1 partition 1 0 -2\n", proc_root)
        assert disk_held("sdb", sys_root, proc_root) == "sdb1 is used as swap"
        put("swaps", "Filename\tType\tSize\tUsed\tPriority\n", proc_root)
        assert disk_held("sdb", sys_root, proc_root) == ""
        offered = {oid: dict(offer, what="sdb 512.1 GB (Generic Card Reader)")}

        # The fakes: md0 with sda working and slot 1 empty, unless a case
        # says otherwise; every command run is recorded.
        degraded = ("    Number   Major   Minor   RaidDevice State\n"
                    "       0       8        0        0      active sync   /dev/sda\n"
                    "       -       0        0        1      removed\n")
        md = {"detail": "State : clean, degraded\n" + degraded, "opened": 7, "wipefs": 0, "add": 0,
              "root": "/dev/mmcblk0p2", "working": size}
        ran = []
        def fake_run(cmd, **_kw):
            ran.append(" ".join([os.path.basename(cmd[0])] + list(cmd[1:])))
            name = os.path.basename(cmd[0])
            if name == "mdadm" and "--detail" in cmd:
                return subprocess.CompletedProcess(cmd, 0, md["detail"], "")
            if name == "findmnt":
                return subprocess.CompletedProcess(cmd, 0, md["root"] + "\n", "")
            if name == "lsblk":
                return subprocess.CompletedProcess(cmd, 0, f"{md['working']}\n", "")
            if name == "wipefs":
                return subprocess.CompletedProcess(cmd, md["wipefs"], "", "wipefs: error" if md["wipefs"] else "")
            if name == "mdadm" and "--add" in cmd:
                return subprocess.CompletedProcess(cmd, md["add"], "", "mdadm: add new device failed" if md["add"] else "")
            raise AssertionError(cmd)
        def fake_exclusive(dev):
            if isinstance(md["opened"], Exception):
                raise md["opened"]
            return md["opened"]
        run, mdstat, exclusive_seq = fake_run, lambda: "md0 : active raid1 sda[0]\n", fake_exclusive
        is_block_device = lambda dev: dev == "/dev/sdb"
        def attempt(offer=oid):
            del ran[:]
            try:
                use_disk(offer, offered, "/dev/md0", sys_root, proc_root)
                return "added"
            except UseRefused as r:
                return r.code
        erased = ["wipefs -a /dev/sdb1", "wipefs -a /dev/sdb", "mdadm --add /dev/md0 /dev/sdb"]
        try:
            # The disk offered, unchanged: its partition, then the disk,
            # are erased, and it is added.
            assert attempt() == "added", ran
            assert [c for c in ran if c.startswith(("wipefs", "mdadm --add"))] == erased, ran
            # No longer the disk offered: another attachment under its
            # name, another size, or an id nobody offered - nothing erased.
            put("block/sdb/diskseq", "8")
            assert attempt() == "gone" and not any(c.startswith("wipefs") for c in ran), ran
            put("block/sdb/diskseq", "7")
            put("block/sdb/size", str(size // 512 - 8))
            assert attempt() == "gone", ran
            put("block/sdb/size", str(size // 512))
            assert attempt("0" * 20) == "gone", ran
            md["opened"] = 9  # what the exclusive open finds disagrees
            assert attempt() == "gone" and not any(c.startswith("wipefs") for c in ran), ran
            md["opened"] = 7
            # Held: a partition mounted, or the disk in use elsewhere.
            put("self/mountinfo", mounts + "40 22 8:17 / /media/backup rw - exfat /dev/sdb1 rw\n", proc_root)
            assert attempt() == "in_use" and not any(c.startswith("wipefs") for c in ran), ran
            put("self/mountinfo", mounts, proc_root)
            md["opened"] = OSError(errno.EBUSY, "Device or resource busy")
            assert attempt() == "in_use" and not any(c.startswith("wipefs") for c in ran), ran
            md["opened"] = 7
            # The mirror no longer lacks a disk: whole again, or a
            # replacement being copied onto - nothing erased.
            md["detail"] = "State : clean\n" + degraded.replace(
                "       -       0        0        1      removed\n", "       1       8       32        1      active sync   /dev/sdc\n")
            assert attempt() == "not_degraded" and not any(c.startswith("wipefs") for c in ran), ran
            md["detail"] = "State : clean, degraded, recovering\n" + degraded.replace(
                "       -       0        0        1      removed\n", "       2       8       32        1      spare rebuilding   /dev/sdc\n")
            assert attempt() == "not_degraded", ran
            md["detail"] = "State : clean, degraded\n" + degraded
            mdstat = lambda: ""
            assert attempt() == "not_degraded", ran
            mdstat = lambda: "md0 : active raid1 sda[0]\n"
            # Smaller than the working disk, the mirror's own, the system's:
            # nothing erased. Larger is fine.
            md["working"] = size * 3
            assert attempt() == "in_use" and not any(c.startswith("wipefs") for c in ran), ran
            md["working"] = size // 2
            assert attempt() == "added", ran
            md["working"] = size
            md["detail"] = "State : clean, degraded\n" + degraded.replace("/dev/sda", "/dev/sdb")
            assert attempt() == "in_use" and not any(c.startswith("wipefs") for c in ran), ran
            md["detail"] = "State : clean, degraded\n" + degraded
            md["root"] = "/dev/sdb2"
            assert attempt() == "in_use" and not any(c.startswith("wipefs") for c in ran), ran
            md["root"] = "/dev/mmcblk0p2"
            is_block_device = lambda dev: False
            assert attempt() == "gone", ran
            is_block_device = lambda dev: dev == "/dev/sdb"
            # Erasing or adding fails: said so.
            md["wipefs"] = 1
            assert attempt() == "failed" and not any(c.startswith("mdadm --add") for c in ran), ran
            md["wipefs"] = 0
            md["add"] = 1
            assert attempt() == "failed", ran
            md["add"] = 0

            # The request: taken from the service's directory, a small
            # regular file of the otc user's only; answered in the outcome.
            run_dir, state = os.path.join(tmp, "run-otc"), os.path.join(tmp, "run-raid-watch")
            os.makedirs(run_dir)
            req, outcome = os.path.join(run_dir, "mirror-disk.request"), os.path.join(state, "mirror-disk.json")
            paths = {"request": req, "state_dir": state, "outcome": outcome, "uid": os.getuid(),
                     "sys_root": sys_root, "proc_root": proc_root}
            rid = "0123456789abcdef"
            import io
            import contextlib
            log = io.StringIO()
            def handle(**kw):
                with contextlib.redirect_stdout(log):
                    return handle_use_request(offered, "/dev/md0", **dict(paths, **kw))
            def ask(body):
                Path(req).write_text(body)
                return handle()
            def said():
                return json.loads(Path(outcome).read_text())
            assert ask(json.dumps({"v": 1, "id": rid, "disk": oid})) is True
            assert said() == {"v": 1, "id": rid, "disk": oid, "code": "added", "what": "sdb 512.1 GB (Generic Card Reader)"}, said()
            assert not os.path.lexists(req) and os.listdir(state) == ["mirror-disk.json"], os.listdir(state)
            assert "sdb 512.1 GB (Generic Card Reader) was erased and added to /dev/md0" in log.getvalue(), log.getvalue()
            # Anything else is refused, and the log says why.
            put("self/mountinfo", mounts + "40 22 8:17 / /media/backup rw - exfat /dev/sdb1 rw\n", proc_root)
            assert ask(json.dumps({"v": 1, "id": rid, "disk": oid})) is False and said()["code"] == "in_use"
            assert "was not used for the mirror: sdb1 is mounted on /media/backup" in log.getvalue(), log.getvalue()
            put("self/mountinfo", mounts, proc_root)
            assert ask(json.dumps({"v": 1, "id": rid, "disk": "f" * 20})) is False and said()["code"] == "gone"
            # Something unexpected on the way: answered all the same.
            def broken(dev):
                raise RuntimeError("no such ioctl")
            exclusive_seq = broken
            assert ask(json.dumps({"v": 1, "id": rid, "disk": oid})) is False and said()["code"] == "failed"
            assert "RuntimeError: no such ioctl" in log.getvalue(), log.getvalue()
            exclusive_seq = fake_exclusive
            for body in ('{"v": 1, "id": "%s", "disk": "%s", "x": 1}' % (rid, oid),
                         '{"v": 1, "id": "%s", "id": "%s", "disk": "%s"}' % (rid, rid, oid),
                         '{"v": 2, "id": "%s", "disk": "%s"}' % (rid, oid),
                         '{"v": 1, "id": "%s", "disk": "/dev/sdb"}' % rid, "[1]", "not json", "{\"v\": NaN}"):
                del ran[:]
                assert ask(body) is False and said() == {"v": 1, "id": "", "disk": "", "code": "bad_request", "what": ""}, body
                assert not ran and not os.path.lexists(req), (body, ran)
            assert handle() is False  # none waiting
            # Never through a link, from a FIFO, nor someone else's file.
            target = os.path.join(tmp, "elsewhere.json")
            Path(target).write_text(json.dumps({"v": 1, "id": rid, "disk": oid}))
            os.symlink(target, req)
            assert handle() is False and said()["code"] == "bad_request"
            assert os.path.exists(target) and not os.path.lexists(req)
            os.mkfifo(req)
            assert handle() is False and said()["code"] == "bad_request"
            Path(req).write_text(json.dumps({"v": 1, "id": rid, "disk": oid}))
            assert handle(uid=os.getuid() + 1) is False and said()["code"] == "bad_request"
            Path(req).write_text(json.dumps({"v": 1, "id": rid, "disk": oid}))
            os.link(req, os.path.join(run_dir, "second-name"))
            assert handle() is False and said()["code"] == "bad_request"
            os.makedirs(os.path.join(req, "inside"))
            assert handle() is False and not os.path.lexists(req)
            assert sorted(os.listdir(state)) == ["mirror-disk.json"], os.listdir(state)
        finally:
            run, mdstat, exclusive_seq, is_block_device = real

if __name__ == "__main__":
    if "--selftest" in sys.argv:
        selftest()
    elif len(sys.argv) == 3 and sys.argv[1] == "--ends-blank":
        # ends_blank's child: a disk's two ends read here, given up from there.
        sys.exit(ends_blank_child(sys.argv[2]))
    else:
        main()
