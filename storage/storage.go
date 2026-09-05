// Package storage implements the disk-detection side of the first-run
// "configure storage" step (issues #38/#39).
//
// ListDevices is safe and read-only — it only reads /sys/block and
// /proc/mounts — so it can run directly inside the otc service's own
// (deliberately unprivileged, systemd-hardened — see otc.service's
// NoNewPrivileges/CapabilityBoundingSet) process.
//
// Actually building the array is destructive (wipes/formats whatever's
// selected) and needs root, which that hardened process can't escalate to
// on its own. Rather than a new privileged helper, this reuses
// scripts/raid_watch.py's existing root-owned systemd service: RequestSetup
// just writes the chosen device paths to RequestFile, and raid_watch.py
// picks that up and does the actual work — see its
// perform_pending_storage_setup().
package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/log"
)

type Device struct {
	Path      string
	SizeBytes int64
	Model     string
}

// bootDiskParent returns the whole-disk device backing "/" (e.g.
// "mmcblk0" for "/dev/mmcblk0p2", "sda" for "/dev/sda2") so it — and every
// device that isn't a real, wholly-separate disk — can be excluded from
// what's offered up for repurposing as storage.
func bootDiskParent() (string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[1] != "/" {
			continue
		}
		src := strings.TrimPrefix(fields[0], "/dev/")
		// Strip a trailing partition number: mmcblk0p2 -> mmcblk0,
		// sda2 -> sda, nvme0n1p2 -> nvme0n1.
		partRe := regexp.MustCompile(`^(mmcblk\d+|nvme\d+n\d+)p\d+$|^([a-z]+)\d+$`)
		if m := partRe.FindStringSubmatch(src); m != nil {
			if m[1] != "" {
				return m[1], nil
			}
			return m[2], nil
		}
		return src, nil
	}
	return "", fmt.Errorf("could not find the boot device in /proc/mounts")
}

// ListDevices enumerates whole disks that are safe to offer as candidates
// for storage: real block devices (excludes the boot disk, loop/ram/zram
// devices, and anything already partitioned into an md array as a
// member — mdN arrays themselves are also skipped, since assembling a new
// array out of an existing one's members isn't a case this flow handles).
func ListDevices() ([]Device, error) {
	bootDisk, err := bootDiskParent()
	if err != nil {
		log.Error("storage: could not determine boot disk:", err)
		bootDisk = ""
	}

	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	var devices []Device
	for _, e := range entries {
		name := e.Name()
		if name == bootDisk {
			continue
		}
		if strings.HasPrefix(name, "loop") ||
			strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") ||
			strings.HasPrefix(name, "md") {
			continue
		}

		sizeSectors, err := readSysBlockInt(name, "size")
		if err != nil || sizeSectors == 0 {
			// A card reader with nothing inserted shows up as a
			// zero-size device — not something anyone can actually
			// pick, so leave it off the list entirely.
			continue
		}
		// /sys/block/<dev>/size is always in 512-byte sectors,
		// regardless of the device's real physical sector size.
		sizeBytes := sizeSectors * 512

		model := readSysBlockString(name, "device/model")

		devices = append(devices, Device{
			Path:      "/dev/" + name,
			SizeBytes: sizeBytes,
			Model:     model,
		})
	}

	return devices, nil
}

func readSysBlockInt(dev, attr string) (int64, error) {
	raw, err := os.ReadFile(filepath.Join("/sys/block", dev, attr))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}

func readSysBlockString(dev, attr string) string {
	raw, err := os.ReadFile(filepath.Join("/sys/block", dev, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// RequestFile is where RequestSetup writes the owner's storage choice.
// scripts/raid_watch.py (which runs as root, unlike this service — see the
// package comment) polls this same path and does the actual wipe/format/
// mdadm work, deleting the file once applied.
const RequestFile = "/var/lib/otc/storage_setup_request.json"

type setupRequest struct {
	DevicePaths []string `json:"device_paths"`
}

// RequestSetup implements the 0/1/2-device semantics from issue #39:
//   - 2 devices: wipe both and build a RAID1 mirror across them.
//   - 1 device: wipe and format that single device alone (no redundancy).
//   - 0 devices: no-op — storage stays on the boot disk.
//
// This service can't do any of that itself (see the package comment), so
// it just hands the request off to raid_watch.py by writing it to
// RequestFile; the actual wipe/mdadm/mkfs happens asynchronously, in that
// separate root-owned process.
func RequestSetup(devicePaths []string) error {
	if len(devicePaths) > 2 {
		return fmt.Errorf("choose at most 2 devices, got %d", len(devicePaths))
	}
	// A nil slice (the zero-devices case arrives as one, coming off a
	// protobuf repeated field) marshals to JSON `null`, not `[]` — caught
	// live testing this against pit.otc: raid_watch.py's `len(None)` then
	// blows up instead of taking the "0 devices" branch.
	if devicePaths == nil {
		devicePaths = []string{}
	}

	data, err := json.Marshal(setupRequest{DevicePaths: devicePaths})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(RequestFile), 0755); err != nil {
		return err
	}

	log.Info("storage: writing setup request:", string(data))
	return os.WriteFile(RequestFile, data, 0644)
}
