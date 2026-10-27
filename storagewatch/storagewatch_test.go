// SPDX-License-Identifier: AGPL-3.0-or-later

package storagewatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/diskspace"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/push"
)

// at makes the storage pct% full (of a 4 TB disk) for the next looks.
func at(t *testing.T, pct *int) {
	t.Helper()
	prev := usageOf
	usageOf = func(string) (diskspace.Usage, error) {
		size := uint64(4_000_000_000_000)
		used := size * uint64(*pct) / 100
		return diskspace.Usage{Total: size + size/20, Used: used, Avail: size - used}, nil
	}
	t.Cleanup(func() { usageOf = prev })
}

type seen struct {
	alerts []alerttext.Alert
	pushes []push.Text
}

func (s *seen) notify() Notify {
	return Notify{
		AlertText: func(a alerttext.Alert) { s.alerts = append(s.alerts, a) },
		PushText:  func(t push.Text) { s.pushes = append(s.pushes, t) },
	}
}

// 90% full is alerted once, 98% once more and pushed; looks in between,
// and a restart (the level is kept in the storage folder), say nothing
// again; going back under a level by more than 5 points arms it again.
func TestStorageAlertsAtNinetyAndNinetyEight(t *testing.T) {
	dir := t.TempDir()
	pct := 50
	at(t, &pct)
	var s seen
	look := func(p int) {
		pct = p
		check(dir, s.notify())
	}
	look(50)
	look(89)
	if len(s.alerts) != 0 {
		t.Fatalf("alerted under 90%%: %+v", s.alerts)
	}
	look(91)
	if len(s.alerts) != 1 || len(s.pushes) != 0 {
		t.Fatalf("at 91%%: %d alerts, %d pushes", len(s.alerts), len(s.pushes))
	}
	if want := alerttext.EnglishOf(i18n.DevAlertStorageAlmostFull(91)); s.alerts[0].Title != want || s.alerts[0].Key != "dev.alert.storage.almost_full" {
		t.Errorf("title %q (%s), want %q", s.alerts[0].Title, s.alerts[0].Key, want)
	}
	if s.alerts[0].DetailKey != "dev.alert.storage.detail" {
		t.Errorf("detail key %q", s.alerts[0].DetailKey)
	}
	look(93)
	look(90)
	if len(s.alerts) != 1 {
		t.Fatalf("90%% alerted again: %d", len(s.alerts))
	}
	look(98)
	if len(s.alerts) != 2 || len(s.pushes) != 1 || s.alerts[1].Key != "dev.alert.storage.full" {
		t.Fatalf("at 98%%: %+v, %d pushes", s.alerts, len(s.pushes))
	}
	if title, body := s.pushes[0]("en"); title != "Your device is almost full" || body == "" {
		t.Errorf("push %q / %q", title, body)
	}
	// A restart: the level alerted is remembered.
	look(99)
	if len(s.alerts) != 2 {
		t.Fatalf("alerted again after a restart: %d", len(s.alerts))
	}
	if b, err := os.ReadFile(filepath.Join(dir, stateFile)); err != nil || string(b) != "98\n" {
		t.Fatalf("state %q, %v", b, err)
	}
	// Back to 92%: 98 is armed again, 90 is still alerted.
	look(92)
	look(98)
	if len(s.alerts) != 3 || len(s.pushes) != 2 {
		t.Fatalf("98%% not alerted again after 92%%: %d alerts", len(s.alerts))
	}
	// Under 85%: everything is armed again, and the state goes.
	look(80)
	if _, err := os.Stat(filepath.Join(dir, stateFile)); !os.IsNotExist(err) {
		t.Fatal("the state stayed after the storage emptied")
	}
	look(90)
	if len(s.alerts) != 4 {
		t.Fatalf("90%% not alerted again after 80%%: %d", len(s.alerts))
	}
}

func TestRearmed(t *testing.T) {
	for _, c := range []struct{ alerted, pct, want int }{
		{98, 99, 98}, {98, 93, 98}, {98, 92, 90}, {98, 84, 0},
		{90, 85, 90}, {90, 84, 0}, {0, 50, 0},
	} {
		if got := rearmed(c.alerted, c.pct); got != c.want {
			t.Errorf("rearmed(%d, %d) = %d, want %d", c.alerted, c.pct, got, c.want)
		}
	}
}
