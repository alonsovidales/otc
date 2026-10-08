// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ncruces/zenity"
)

func TestAutostartAnswer(t *testing.T) {
	for _, c := range []struct {
		err          error
		on, answered bool
	}{
		{nil, true, true},                       // Start at Login
		{zenity.ErrCanceled, false, true},       // Not Now, or closed
		{errors.New("no zenity"), false, false}, // no dialog: ask again next start
	} {
		on, answered := autostartAnswer(c.err)
		if on != c.on || answered != c.answered {
			t.Errorf("%v: got %v %v, want %v %v", c.err, on, answered, c.on, c.answered)
		}
	}
}

// The question pops up unasked: "Not Now" must be the default button, so a
// stray Enter never consents.
func TestAutostartQuestionDefaultsToNotNow(t *testing.T) {
	want := reflect.ValueOf(zenity.DefaultCancel()).Pointer()
	for _, o := range autostartOptions() {
		if reflect.ValueOf(o).Kind() == reflect.Func && reflect.ValueOf(o).Pointer() == want {
			return
		}
	}
	t.Fatal("the start-at-login question has no zenity.DefaultCancel()")
}
