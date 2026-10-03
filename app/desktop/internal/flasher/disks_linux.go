// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Never offered: virtual devices, RAID/LVM, optical drives, internal NVMe.
var skipPrefixes = []string{"loop", "ram", "zram", "dm-", "md", "nbd", "sr", "fd", "nvme"}

// The mount points that make a disk the system's own.
var systemMounts = map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/boot/firmware": true,
	"/efi": true, "/usr": true, "/var": true, "/home": true}

func sysRead(name, file string) string {
	b, _ := os.ReadFile(filepath.Join("/sys/block", name, file))
	return strings.TrimSpace(string(b))
}

// ListDisks lists the removable disks: USB disks and card readers, and SD
// cards in a built-in reader - never one the system runs from.
func ListDisks() ([]Disk, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	system := systemDisks()
	var out []Disk
	for _, e := range entries {
		name := e.Name()
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(name, p) {
				skip = true
			}
		}
		if skip || system[name] {
			continue
		}
		link, _ := filepath.EvalSymlinks(filepath.Join("/sys/block", name))
		removable := sysRead(name, "removable") == "1" || strings.Contains(link, "/usb") || strings.HasPrefix(name, "mmcblk")
		if !removable {
			continue
		}
		sectors, _ := strconv.ParseInt(sysRead(name, "size"), 10, 64)
		if sectors == 0 { // a reader with no card in it
			continue
		}
		label := strings.TrimSpace(sysRead(name, "device/vendor") + " " + sysRead(name, "device/model"))
		if label == "" {
			label = sysRead(name, "device/name") // SD cards
		}
		out = append(out, Disk{ID: "/dev/" + name, Name: label, Size: sectors * 512})
	}
	// Tests only (root, on purpose): a loop device standing in for a card.
	if t := os.Getenv("OTC_FLASH_TEST_DISK"); t != "" && os.Geteuid() == 0 {
		name := filepath.Base(t)
		sectors, _ := strconv.ParseInt(sysRead(name, "size"), 10, 64)
		out = append(out, Disk{ID: t, Name: "Test disk", Size: sectors * 512})
	}
	return out, nil
}

// baseDisk is the disk a partition belongs to (sdb1 -> sdb, mmcblk0p2 ->
// mmcblk0), from sysfs.
func baseDisk(dev string) string {
	name := filepath.Base(dev)
	if _, err := os.Stat(filepath.Join("/sys/block", name)); err == nil {
		return name
	}
	link, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", name))
	if err != nil {
		return name
	}
	return filepath.Base(filepath.Dir(link))
}

// systemDisks are the disks holding a system mount point or swap; for
// device-mapper (LVM, LUKS) it follows the slaves down to the real disks.
func systemDisks() map[string]bool {
	out := map[string]bool{}
	var mark func(dev string)
	mark = func(dev string) {
		if real, err := filepath.EvalSymlinks(dev); err == nil {
			dev = real
		}
		name := baseDisk(dev)
		out[name] = true
		slaves, _ := os.ReadDir(filepath.Join("/sys/class/block", filepath.Base(dev), "slaves"))
		for _, s := range slaves {
			mark("/dev/" + s.Name())
		}
	}
	for _, m := range mounts() {
		if systemMounts[m.target] && strings.HasPrefix(m.source, "/dev/") {
			mark(m.source)
		}
	}
	if f, err := os.Open("/proc/swaps"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if fields := strings.Fields(sc.Text()); len(fields) > 0 && strings.HasPrefix(fields[0], "/dev/") {
				mark(fields[0])
			}
		}
	}
	return out
}

type mount struct{ source, target string }

func mounts() []mount {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []mount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 {
			// Spaces in mount points are written as \040.
			out = append(out, mount{fields[0], strings.ReplaceAll(fields[1], `\040`, " ")})
		}
	}
	return out
}

type linuxDisk struct{ *os.File }

func (d linuxDisk) Sync() error { return d.File.Sync() }

// openDisk unmounts whatever the desktop mounted from the card and opens
// it exclusively (O_EXCL fails while anything still has it mounted).
func openDisk(d Disk) (device, error) {
	name := filepath.Base(d.ID)
	for _, m := range mounts() {
		if strings.HasPrefix(m.source, "/dev/") && baseDisk(m.source) == name {
			if err := unix.Unmount(m.target, 0); err != nil {
				return nil, fmt.Errorf("the card is in use (%s): close any window or program using it and try again", m.target)
			}
		}
	}
	f, err := os.OpenFile(d.ID, os.O_RDWR|unix.O_EXCL|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", d.ID, err)
	}
	return linuxDisk{f}, nil
}

// dropCache makes the read-back come from the card, not from memory.
func dropCache(dev device) {
	if d, ok := dev.(linuxDisk); ok {
		_ = unix.IoctlSetInt(int(d.Fd()), unix.BLKFLSBUF, 0)
		_ = unix.Fadvise(int(d.Fd()), 0, 0, unix.FADV_DONTNEED)
	}
}

// eject powers a USB reader off (or ejects the card), best effort: the
// desktop may already have mounted the new card's boot partition.
func eject(d Disk) {
	name := filepath.Base(d.ID)
	for _, m := range mounts() {
		if strings.HasPrefix(m.source, "/dev/") && baseDisk(m.source) == name {
			_ = unix.Unmount(m.target, 0)
		}
	}
	if exec.Command("udisksctl", "power-off", "--no-user-interaction", "-b", d.ID).Run() != nil {
		_ = exec.Command("eject", d.ID).Run()
	}
}

func openStatus(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW, 0)
}

// elevate runs exe with args as root: through pkexec, which asks for the
// password in the desktop's own dialog, or directly when already root.
func elevate(exe string, args []string) (func() error, error) {
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(exe, args...)
	} else {
		pk, err := exec.LookPath("pkexec")
		if err != nil {
			return nil, errors.New("pkexec is not installed - run `sudo otc-sync flash` in a terminal instead")
		}
		cmd = exec.Command(pk, append([]string{exe}, args...)...)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() error {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) && os.Geteuid() != 0 {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && (ws.ExitStatus() == 126 || ws.ExitStatus() == 127) {
				return ErrCancelled // dismissed, or not authorised
			}
		}
		return err
	}, nil
}
