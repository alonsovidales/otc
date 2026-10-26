// SPDX-License-Identifier: AGPL-3.0-or-later

package container

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The container's own disk mirror (docker/compose.mirror.yaml, opt-in): the
// image's start-up script has otc-mirror assemble a RAID1 on the two disks
// named in .env and mount it on the storage folder, and hands the device
// which array that is and which disks it is made of. The host's other
// arrays share /proc/mdstat and /sys/block with it; the device looks at
// this one only.
const (
	// EnvMirrorArray is the array's kernel name, "md127".
	EnvMirrorArray = "OTC_MIRROR_ARRAY"
	// EnvMirrorDev1 and EnvMirrorDev2 are each disk's block device number,
	// "major:minor" ("8:16"): the kernel's names (sdb) change from one
	// boot to the next, the numbers of a disk that stays plugged in don't.
	EnvMirrorDev1 = "OTC_MIRROR_DEV1"
	EnvMirrorDev2 = "OTC_MIRROR_DEV2"
	// EnvMirrorLabel1 and EnvMirrorLabel2 are each disk's name as the
	// owner wrote it in .env, without its folder
	// ("ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111"): what the alerts call it.
	EnvMirrorLabel1 = "OTC_MIRROR_LABEL1"
	EnvMirrorLabel2 = "OTC_MIRROR_LABEL2"
)

// cMirrorLabelMax is the longest disk name kept, in characters: a
// /dev/disk/by-id name is well under it.
const cMirrorLabelMax = 128

// reMirrorArray is a kernel md array name: md0, md127, or md_otc where the
// host's mdadm names its arrays (CREATE names=yes) - the entrypoint's rule.
var reMirrorArray = regexp.MustCompile(`^md[A-Za-z0-9_]{1,32}$`)

// MirrorArray is the container's mirror ("md127"), or "" when there is
// none: outside a container, without the mirror override, or a value that
// isn't an md array's name.
func MirrorArray() string {
	if !Enabled() {
		return ""
	}
	name := strings.TrimSpace(os.Getenv(EnvMirrorArray))
	if !reMirrorArray.MatchString(name) {
		return ""
	}
	return name
}

// MirrorDisk is one of the two disks the mirror is built on.
type MirrorDisk struct {
	Dev   string // "major:minor", as sysfs writes it (md/dev-*/block/dev)
	Label string // its name in .env, as the owner knows it
}

// MirrorDisks are the disks named in .env, disk 1 first, each with its
// device number and its name; one whose number or name is missing or
// malformed is left out. Nil outside a container or without a mirror.
func MirrorDisks() []MirrorDisk {
	if MirrorArray() == "" {
		return nil
	}
	var disks []MirrorDisk
	for _, env := range [][2]string{{EnvMirrorDev1, EnvMirrorLabel1}, {EnvMirrorDev2, EnvMirrorLabel2}} {
		dev, ok := devNumber(os.Getenv(env[0]))
		label := mirrorLabel(os.Getenv(env[1]))
		if ok && label != "" {
			disks = append(disks, MirrorDisk{Dev: dev, Label: label})
		}
	}
	return disks
}

// devNumber is s, "major:minor", written the way sysfs writes it (no
// leading zeros, no spaces), and whether it is one.
func devNumber(s string) (string, bool) {
	major, minor, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return "", false
	}
	ma, err1 := strconv.ParseUint(major, 10, 32)
	mi, err2 := strconv.ParseUint(minor, 10, 32)
	if err1 != nil || err2 != nil {
		return "", false
	}
	return strconv.FormatUint(ma, 10) + ":" + strconv.FormatUint(mi, 10), true
}

// mirrorLabel is a disk's name as alerts may show it: printable characters
// only, trimmed, at most cMirrorLabelMax of them.
func mirrorLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > cMirrorLabelMax {
		s = strings.TrimSpace(string(r[:cMirrorLabelMax]))
	}
	return s
}
