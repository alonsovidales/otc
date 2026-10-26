// SPDX-License-Identifier: AGPL-3.0-or-later

package status

import (
	"strconv"
	"testing"

	"github.com/alonsovidales/otc/raidwatch"
)

// legacyDegraded is the status line exactly as it was built before the
// catalog (raidwatch's label for the missing disks, "" for none).
func legacyDegraded(active, total int, what string) string {
	msg := "RAID array degraded: only " + strconv.Itoa(active) + " of " + strconv.Itoa(total) + " disks active"
	if what != "" {
		msg += " - " + what + " stopped working"
	}
	return msg
}

// The degraded mirror's line is the same English as before for a request
// without a language, in English, or in a language this build has no text
// for: no disk known, each port, a disk with no known port, and two.
func TestDegradedTextEnglishUnchanged(t *testing.T) {
	top := raidwatch.Member{Position: "top"}
	bottom := raidwatch.Member{Position: "bottom"}
	unknown := raidwatch.Member{}
	cases := []struct {
		ports []raidwatch.Port
		label string
	}{
		{nil, ""},
		{[]raidwatch.Port{top.Where()}, top.Label()},
		{[]raidwatch.Port{bottom.Where()}, bottom.Label()},
		{[]raidwatch.Port{unknown.Where()}, unknown.Label()},
		{[]raidwatch.Port{top.Where(), bottom.Where()}, top.Label() + " and " + bottom.Label()},
	}
	for _, c := range cases {
		want := legacyDegraded(1, 2, c.label)
		for _, lang := range []string{"", "en", "en-GB", "xx"} {
			if got := degradedText(lang, 1, 2, c.ports, c.label, raidwatch.PortUnknown); got != want {
				t.Errorf("lang %q, ports %v: got %q, want %q", lang, c.ports, got, want)
			}
		}
	}
	// The exact words, not only the old construction.
	if got := degradedText("", 1, 2, []raidwatch.Port{raidwatch.PortTop}, top.Label(), raidwatch.PortUnknown); got != "RAID array degraded: only 1 of 2 disks active - the disk in the top blue USB port stopped working" {
		t.Errorf("top port: %q", got)
	}
	if got := degradedText("", 0, 2, nil, "", raidwatch.PortUnknown); got != "RAID array degraded: only 0 of 2 disks active" {
		t.Errorf("no port: %q", got)
	}
}

// The missing disk's port unknown and the working one's known: the line
// names the one still working, in English and in another language; a
// missing disk whose port is known is named whatever else works.
func TestDegradedTextNamesTheDiskStillWorking(t *testing.T) {
	unknown := []raidwatch.Port{raidwatch.PortUnknown}
	if got := degradedText("", 1, 2, unknown, "one of the two storage disks", raidwatch.PortBottom); got != "RAID array degraded: only 1 of 2 disks active - the disk in the bottom blue USB port still works, the other one stopped working" {
		t.Errorf("bottom works: %q", got)
	}
	if got := degradedText("es", 1, 2, unknown, "one of the two storage disks", raidwatch.PortTop); got != "RAID degradado: solo 1 de 2 discos activos; el disco del puerto USB azul superior sigue funcionando y el otro ha dejado de funcionar" {
		t.Errorf("top works, Spanish: %q", got)
	}
	if got := degradedText("", 1, 2, []raidwatch.Port{raidwatch.PortTop}, "the disk in the top blue USB port", raidwatch.PortBottom); got != legacyDegraded(1, 2, "the disk in the top blue USB port") {
		t.Errorf("named disk: %q", got)
	}
}

// The container's mirror names its disks as in .env: the missing one, or
// the one still working when the missing one isn't known, in English and
// in other languages; none known, or several, as on a Raspberry Pi.
func TestDegradedNamedText(t *testing.T) {
	const disk = "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K2222222"
	for _, c := range []struct {
		lang, working string
		disks         []string
		label, want   string
	}{
		{"", "", []string{disk}, "the disk " + disk, "RAID array degraded: only 1 of 2 disks active - the disk " + disk + " stopped working"},
		{"en", "", []string{disk}, "the disk " + disk, "RAID array degraded: only 1 of 2 disks active - the disk " + disk + " stopped working"},
		{"es", "", []string{disk}, "the disk " + disk, "RAID degradado: solo 1 de 2 discos activos; el disco " + disk + " ha dejado de funcionar"},
		{"", disk, []string{""}, "one of the two storage disks", "RAID array degraded: only 1 of 2 disks active - the disk " + disk + " still works, the other one stopped working"},
		{"de", disk, []string{""}, "one of the two storage disks", "RAID-Verbund beeinträchtigt: nur 1 von 2 Laufwerken aktiv – das Laufwerk " + disk + " funktioniert noch, das andere nicht mehr"},
		// A missing disk that is named is named whatever else works.
		{"", "ata-other", []string{disk}, "the disk " + disk, "RAID array degraded: only 1 of 2 disks active - the disk " + disk + " stopped working"},
		{"", "", []string{""}, "one of the two storage disks", legacyDegraded(1, 2, "one of the two storage disks")},
		{"", "", nil, "", legacyDegraded(1, 2, "")},
		{"", "", []string{"ata-a", "ata-b"}, "the disk ata-a and the disk ata-b", legacyDegraded(1, 2, "the disk ata-a and the disk ata-b")},
		{"fr", "", []string{"ata-a", "ata-b"}, "the disk ata-a and the disk ata-b", "Volume RAID dégradé (disques actifs\u00a0: seulement 1 sur 2)"},
	} {
		if got := degradedNamedText(c.lang, 1, 2, c.disks, c.label, c.working); got != c.want {
			t.Errorf("lang %q, disks %q, working %q:\n got %q\nwant %q", c.lang, c.disks, c.working, got, c.want)
		}
	}
}
