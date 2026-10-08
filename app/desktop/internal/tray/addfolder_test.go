// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// Step 2 offers a backup Keep out of Images only (it is always upload
// only), a two-way folder from here both, and a folder from the device
// nothing; what the device said it can't do is left out.
func TestKindOptions(t *testing.T) {
	all := config.State{}
	cases := []struct {
		kind folderKind
		st   config.State
		want []addOption
	}{
		{kindBackup, all, []addOption{optKeepOut}},
		{kindLocal, all, []addOption{optUploadOnly, optKeepOut}},
		{kindDevice, all, nil},
		{kindLocal, config.State{OutOfImagesUnsupported: true}, []addOption{optUploadOnly}},
		{kindLocal, config.State{UploadOnlyUnsupported: true}, []addOption{optKeepOut}},
		{kindBackup, config.State{OutOfImagesUnsupported: true}, nil},
		{kindDevice, config.State{}, nil},
	}
	for _, c := range cases {
		if got := kindOptions(c.kind, c.st); !slices.Equal(got, c.want) {
			t.Errorf("%s %+v: got %v, want %v", kindTitles[c.kind], c.st, got, c.want)
		}
	}
}

// What is ticked becomes the folder's requests; an unticked option sends
// nothing, and a backup is never asked for upload only (it is made so at
// every start).
func TestNewLocalFolder(t *testing.T) {
	f := newLocalFolder("/home/ana/Docs", kindLocal, map[addOption]bool{optUploadOnly: true})
	if f.OneWay || f.UploadOnly == nil || !*f.UploadOnly || f.OutOfImages != nil {
		t.Fatalf("two-way, upload only: %+v", f)
	}
	f = newLocalFolder("/home/ana/Docs", kindLocal, map[addOption]bool{optKeepOut: true, optUploadOnly: false})
	if f.UploadOnly != nil || f.OutOfImages == nil || !*f.OutOfImages {
		t.Fatalf("two-way, kept out: %+v", f)
	}
	f = newLocalFolder("/home/ana/Scans", kindBackup, map[addOption]bool{optKeepOut: true, optUploadOnly: true})
	if !f.OneWay || f.UploadOnly != nil || f.OutOfImages == nil {
		t.Fatalf("backup: %+v", f)
	}
	if f.ID == "" || f.Path != "/home/ana/Scans" {
		t.Fatalf("entry: %+v", f)
	}
}

// The checklist says what each option does; a backup's says upload only
// is always on; and the words are the Mac's.
func TestOptionsWords(t *testing.T) {
	text := optionsText(kindLocal, []addOption{optUploadOnly, optKeepOut})
	for _, want := range []string{engine.UploadOnlyCaption, engine.UploadOnlyTwoWay, engine.OutOfImagesAddCaption, "Sync a folder from this computer"} {
		if !strings.Contains(text, want) {
			t.Errorf("checklist text lacks %q:\n%s", want, text)
		}
	}
	backup := optionsText(kindBackup, []addOption{optKeepOut})
	if !strings.Contains(backup, "Upload only: always on for a backup.") || strings.Contains(backup, engine.UploadOnlyTwoWay) {
		t.Errorf("backup's text:\n%s", backup)
	}
	if q := optionQuestion(kindLocal, optUploadOnly); !strings.HasPrefix(q, "Make the folder upload only?") || !strings.Contains(q, engine.UploadOnlyTwoWay) {
		t.Errorf("question: %q", q)
	}
	if q := optionQuestion(kindBackup, optKeepOut); !strings.Contains(q, "always on for a backup") {
		t.Errorf("backup's question: %q", q)
	}
	if !strings.Contains(explainKindsText, engine.UploadOnlyTwoWay) || !strings.Contains(explainKindsText, imagesExplainKinds) {
		t.Errorf("What Do These Do? lacks the options:\n%s", explainKindsText)
	}
}

// A folder's line says its upload-only request is on its way, and nothing
// once the device has it.
func TestFolderTitleSaysUploadOnly(t *testing.T) {
	base := config.FolderStatus{ID: "r1", Path: "/home/ana/Docs", RemotePath: "/linux/pc/home/ana/Docs", State: "watching"}
	cases := map[string]string{
		"":            "⇅ Docs — Synced",
		"making":      "⇅ Docs — Synced · Making it upload only…",
		"unsupported": "⇅ Docs — Synced · Your device needs an update to make folders upload only.",
	}
	for state, want := range cases {
		f := base
		f.UploadOnly = state
		if got := folderTitle(f); got != want {
			t.Errorf("%q: got %q, want %q", state, got, want)
		}
	}
}

// Windows asks each option on its own: Yes, No (the extra button), and
// Cancel - which, like closing the dialog, stops the whole flow instead of
// meaning No and going on to the folder chooser.
func TestQuestionAnswer(t *testing.T) {
	if yes, err := questionAnswer(nil); !yes || err != nil {
		t.Fatalf("Yes: %v %v", yes, err)
	}
	if yes, err := questionAnswer(zenity.ErrExtraButton); yes || err != nil {
		t.Fatalf("No: %v %v", yes, err)
	}
	for _, e := range []error{zenity.ErrCanceled, errors.New("no dialog program")} {
		if _, err := questionAnswer(e); err == nil {
			t.Fatalf("%v went on", e)
		}
	}
}
