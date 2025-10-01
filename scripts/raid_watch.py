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
import time
import json
import glob
import shlex
import signal
import subprocess
from pathlib import Path

# ----------------------------
# CONFIG (edit to your setup)
# ----------------------------
CONFIG = {
    # Your array device
    "raid_dev": "/dev/md0",

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
    },
}

# ----------------------------
# GPIO setup
# ----------------------------
try:
    import RPi.GPIO as GPIO
except ImportError:
    GPIO = None
    print("WARNING: RPi.GPIO not available. LED operations will be no-ops (dry run).")

def gpio_setup():
    if GPIO is None:
        return
    GPIO.setmode(GPIO.BCM)
    for slot, pins in CONFIG["leds"].items():
        for color in ("red", "green"):
            GPIO.setup(pins[color], GPIO.OUT)
            GPIO.output(pins[color], GPIO.LOW)

def gpio_cleanup():
    if GPIO is None:
        return
    GPIO.cleanup()

def led_set(slot: int, red_on: bool, green_on: bool):
    pins = CONFIG["leds"][slot]
    if GPIO is None:
        # Dry-run printout
        return
    GPIO.output(pins["red"], GPIO.HIGH if red_on else GPIO.LOW)
    GPIO.output(pins["green"], GPIO.HIGH if green_on else GPIO.LOW)

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
    info = {"state": "", "members": {}}

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

def pick_candidate_disk(existing_member_size, detail):
    """Pick a disk not in md array, large enough, and not the root disk."""
    # Disks present
    disks = list_block_disks()  # list of ("/dev/sdX", size)
    # Disks to exclude: any base disk already present in array
    in_array_bases = member_base_disks(detail)
    # Also exclude the root disk (if it’s an sdX device)
    root_base = root_base_disk()

    candidates = []
    for d, sz in disks:
        base = base_disk(d)

        # Exclude any disk that is already a member (whole disk or parent of a partition member)
        if base in in_array_bases:
            continue

        # Exclude root disk if applicable
        if root_base and base == root_base:
            continue

        # Exclude mounted disks (anything with a mountpoint)
        mounts = run(["lsblk", "-ndo", "MOUNTPOINT", d]).stdout.strip().splitlines()
        if any(m for m in mounts if m):
            continue

        # Must be large enough relative to existing member
        if existing_member_size and sz < int(existing_member_size * CONFIG["auto_repair_min_size_ratio"]):
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

def raid_name_from_dev(raid_dev):
    return Path(raid_dev).name  # e.g., md0

def now():
    return time.time()

# ----------------------------
# LED state machine
# ----------------------------
class BlinkState:
    def __init__(self, period):
        self.period = max(0.2, float(period))
        print(f"Blink state: {self.period}")
        self.next_toggle = now()
        self.on = False

    def step(self):
        print("Blink step")
        t = now()
        if t >= self.next_toggle:
            self.on = not self.on
            self.next_toggle = t + self.period / 2.0
            print(f"Next toggle: {self.next_toggle}")

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

    print(f"[raid-watch] Monitoring {raid_dev} (name: {raid_name})")
    gpio_setup()

    led = LedController(CONFIG["blink_period_s"])

    def handle_sigterm(sig, frame):
        print("[raid-watch] SIGTERM received. Cleaning up GPIO.")
        gpio_cleanup()
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, handle_sigterm)
    signal.signal(signal.SIGINT, handle_sigterm)

    try:
        while True:
            detail = mdadm_detail(raid_dev)
            state = (detail.get("state") or "").lower()
            members = detail.get("members", {})
            print(f"members: {members}")

            # Default: turn off until we decide
            led.set_mode(0, "off")
            led.set_mode(1, "off")

            # Determine per-slot status
            # Slots that exist in array definition
            present_slots = sorted(members.keys())

            # Detect resync/recovery via /proc/mdstat
            mdst = mdstat()
            rebuilding = array_is_resyncing(mdst, raid_name)

            # Identify source vs target during rebuild:
            # mdadm --detail marks the rebuilding device as "(rebuilding)" or "spare rebuilding"
            rebuilding_slot = None
            for slot, m in members.items():
                if "rebuild" in m["state"]:
                    rebuilding_slot = slot
                    break

            # If we want to also confirm direction: compare Events counters
            events_by_slot = {}
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
                print(f"Degraded: {members}")
                # Some member missing/failed
                for slot in (0, 1):
                    print(f"Slot: {slot}")
                    if slot not in members:
                        print(f"Slot not a member")
                        # Missing member -> solid red
                        led.set_mode(slot, "solid_red")
                    else:
                        # The surviving member: green unless rebuilding
                        if rebuilding and rebuilding_slot == slot:
                            print(f"rebuilding slot")
                            # If the only member is somehow "rebuilding" (rare), blink red
                            led.set_mode(slot, "blink_red")
                        else:
                            print(f"not rebuilding slot")
                            led.set_mode(slot, "solid_green")
            else:
                # Two members present
                if rebuilding and rebuilding_slot is not None:
                    print(f"Rebuilding")
                    # Target (rebuilding) -> blink red
                    led.set_mode(rebuilding_slot, "blink_red")
                    # Source (newer events) -> blink green
                    src = source_slot if source_slot is not None else (0 if rebuilding_slot == 1 else 1)
                    led.set_mode(src, "blink_green")
                else:
                    # Healthy + in-sync
                    # mdadm reports "clean", "active", etc. Without rebuild markers.
                    print(f"Healty")
                    led.set_mode(0, "solid_green")
                    led.set_mode(1, "solid_green")

            # Apply LED states (updates blinkers)
            led.apply()

            # Auto-repair path
            if CONFIG["auto_repair_enable"]:
                print("Auto repair enabled")
                # If degraded and exactly one member present, try to find a candidate
                if ("degraded" in state) or (len(members) < 2):
                    print("Degraded:", state)
                    # Determine size of existing member to filter candidates
                    sizes = []
                    for m in members.values():
                        sizes.append(size_of(m["device"]))
                    existing_size = max(sizes) if sizes else 0
                    candidate = pick_candidate_disk(existing_size, detail)
                    if candidate:
                        print(f"[raid-watch] Candidate new disk detected: {candidate}")
                        to_add = candidate
                        if CONFIG["auto_add_mode"] == "partition":
                            to_add = prepare_disk_for_raid(candidate)
                        print(f"[raid-watch] Adding {to_add} to {raid_dev} ...")
                        r = add_member(raid_dev, to_add)
                        if r.returncode != 0:
                            print(f"[raid-watch] mdadm --add failed: {r.stderr}")
                        else:
                            print("[raid-watch] Member added, rebuild should start automatically.")

            time.sleep(CONFIG["poll_s"])

    finally:
        gpio_cleanup()

if __name__ == "__main__":
    main()
