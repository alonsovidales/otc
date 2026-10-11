// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// A status file from a runner with codes, one from before them, and one
// whose code this build doesn't know: Message is what the runner wrote,
// and Text in English (or without a language) is exactly that.
func TestStatusFileWithCodes(t *testing.T) {
	files := []string{
		`{"state":"running","message":"Downloading release 118","code":"downloading","args":{"release":"118"},"version":"117","updated":"2026-10-11T10:00:00Z"}`,
		`{"state":"running","message":"Downloading release 118","version":"117","updated":"2026-10-11T10:00:00Z"}`,
		`{"state":"running","message":"Downloading release 118","code":"from_the_future","args":{"x":"1"},"version":"117","updated":"2026-10-11T10:00:00Z"}`,
		`{"state":"running","message":"Downloading release 118","code":"downloading","args":null,"version":"117","updated":"2026-10-11T10:00:00Z"}`,
	}
	for _, f := range files {
		var st Status
		if err := json.Unmarshal([]byte(f), &st); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if st.State != "running" || st.Message != "Downloading release 118" || st.Version != "117" {
			t.Errorf("%s read as %+v", f, st)
		}
		for _, lang := range []string{"", "en", "es"} {
			if got := st.Text(lang); got != "Downloading release 118" {
				t.Errorf("%s in %q: %q", f, lang, got)
			}
		}
	}
	var st Status
	_ = json.Unmarshal([]byte(files[0]), &st)
	if st.Code != "downloading" || string(st.Args) != `{"release":"118"}` {
		t.Errorf("code and args read as %q %q", st.Code, st.Args)
	}
}

// An interrupted run reads with the same words otc-update-stopped writes,
// and its code.
func TestInterruptedStatus(t *testing.T) {
	if cInterrupted != "The update was interrupted - press Update to try again" {
		t.Fatalf("cInterrupted is %q", cInterrupted)
	}
	running := Status{State: "running", Message: "Building", Code: "building", Version: "91"}
	got := reconcileStatus(func() Status { return running }, func() bool { return true })
	if got.Code != "interrupted" || got.Text("") != cInterrupted || got.Text("en") != cInterrupted {
		t.Errorf("an interrupted run read as %+v", got)
	}
}

// Apply's errors keep their English, and errors.Is still sees the cause.
func TestApplyErrorsEnglishUnchanged(t *testing.T) {
	cause := errors.New("permission denied")
	cases := []struct {
		err  error
		want string
	}{
		{devtext.New(i18n.DevUpdateAlreadyRunning()), "an update is already running"},
		{devtext.Wrap(i18n.DevUpdateRequestFailed(cause.Error()), cause), "requesting the update: permission denied"},
		{devtext.Wrap(i18n.DevUpdateRunnerDownloadFailed(cause.Error()), cause), "downloading the updater: permission denied"},
		{devtext.Wrap(i18n.DevUpdateRunnerChmodFailed(cause.Error()), cause), "making the updater executable: permission denied"},
		{devtext.Wrap(i18n.DevUpdateRunnerStartFailed(cause.Error()), cause), "starting the updater: permission denied"},
	}
	for _, c := range cases {
		if c.err.Error() != c.want || devtext.Text("", c.err) != c.want || devtext.Text("en", c.err) != c.want {
			t.Errorf("got %q, want %q", c.err.Error(), c.want)
		}
	}
	if !errors.Is(cases[1].err, cause) {
		t.Error("the cause is lost")
	}
}
