// SPDX-License-Identifier: AGPL-3.0-or-later

package container

import (
	"slices"
	"strings"
	"testing"
)

// The mirror the start-up script hands over: its array and its two disks,
// only in a container and only with an array's name.
func TestMirror(t *testing.T) {
	t.Setenv(EnvVar, "1")
	t.Setenv(EnvMirrorArray, "md127")
	t.Setenv(EnvMirrorDev1, "8:16")
	t.Setenv(EnvMirrorDev2, " 8:32 ")
	t.Setenv(EnvMirrorLabel1, "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111")
	t.Setenv(EnvMirrorLabel2, "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K2222222")
	if got := MirrorArray(); got != "md127" {
		t.Errorf("MirrorArray() = %q", got)
	}
	t.Setenv(EnvMirrorArray, "md_otc") // named by the host's mdadm (CREATE names=yes)
	if got := MirrorArray(); got != "md_otc" {
		t.Errorf("MirrorArray() = %q", got)
	}
	t.Setenv(EnvMirrorArray, "md127")
	if got := MirrorArray(); got != "md127" {
		t.Errorf("MirrorArray() = %q", got)
	}
	want := []MirrorDisk{
		{Dev: "8:16", Label: "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111"},
		{Dev: "8:32", Label: "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K2222222"},
	}
	if got := MirrorDisks(); !slices.Equal(got, want) {
		t.Errorf("MirrorDisks() = %+v", got)
	}

	// A disk without a number or a name is left out; the other stays.
	t.Setenv(EnvMirrorDev2, "sdc")
	if got := MirrorDisks(); !slices.Equal(got, want[:1]) {
		t.Errorf("a malformed number: %+v", got)
	}
	t.Setenv(EnvMirrorDev2, "8:32")
	t.Setenv(EnvMirrorLabel1, " \t")
	if got := MirrorDisks(); !slices.Equal(got, want[1:]) {
		t.Errorf("an empty name: %+v", got)
	}

	// Not an md array's name: no mirror at all.
	for _, v := range []string{"", "md", "sda", "md127/../..", "../md0", "md 1", "md-otc", "md" + strings.Repeat("1", 33)} {
		t.Setenv(EnvMirrorArray, v)
		if got := MirrorArray(); got != "" {
			t.Errorf("%s=%q: %q", EnvMirrorArray, v, got)
		}
		if got := MirrorDisks(); got != nil {
			t.Errorf("%s=%q: disks %+v", EnvMirrorArray, v, got)
		}
	}

	// Outside a container the variables mean nothing.
	t.Setenv(EnvMirrorArray, "md127")
	t.Setenv(EnvVar, "")
	if MirrorArray() != "" || MirrorDisks() != nil {
		t.Errorf("outside a container: %q %+v", MirrorArray(), MirrorDisks())
	}
}

func TestDevNumber(t *testing.T) {
	for in, want := range map[string]string{
		"8:16":         "8:16",
		" 259:0\n":     "259:0",
		"008:016":      "8:16",
		"9:127":        "9:127",
		"":             "",
		"8":            "",
		"8:":           "",
		":16":          "",
		"8:16:1":       "",
		"-8:16":        "",
		"8:1x":         "",
		"4294967296:0": "",
	} {
		got, ok := devNumber(in)
		if got != want || ok != (want != "") {
			t.Errorf("devNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// A disk's name reaches the alerts as it is, printable characters only:
// no control or direction characters, at most cMirrorLabelMax.
func TestMirrorLabel(t *testing.T) {
	for in, want := range map[string]string{
		"ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111":  "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111",
		"  usb-Samsung_PSSD_T7_S5T0NS0W123456-0:0 ": "usb-Samsung_PSSD_T7_S5T0NS0W123456-0:0",
		"nvme-eui.0025388b91c1a2b3":                 "nvme-eui.0025388b91c1a2b3",
		"disk\x1b[31m1":                             "disk[31m1",
		"a\u202eb\u2066c":                           "abc",
		"bad\xffbyte":                               "badbyte",
		"\n":                                        "",
	} {
		if got := mirrorLabel(in); got != want {
			t.Errorf("mirrorLabel(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", cMirrorLabelMax+10)
	if got := mirrorLabel(long); got != strings.Repeat("é", cMirrorLabelMax) {
		t.Errorf("a long name: %d characters", len([]rune(got)))
	}
}
