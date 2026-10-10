// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"testing"
)

func TestStoredKeys(t *testing.T) {
	en := entries(
		"dev.alert_failed", `{"text": "{name} could not be processed", "args": [["name", "user"]], "note": "n", "stored": true}`,
		"dev.other", `{"text": "Other", "note": "n"}`,
	)
	c := load(t, map[string]string{enFile("dev"): en})
	rec := string(c.StoredRecord())
	wantRec := "{\n  \"dev.alert_failed\": [[\"name\", \"user\"]]\n}\n"
	if rec != wantRec {
		t.Fatalf("StoredRecord:\n%s\nwant:\n%s", rec, wantRec)
	}
	if got := string(load(t, map[string]string{enFile("dev"): `{"dev.other": {"text": "Other", "note": "n"}}`}).StoredRecord()); got != "{}\n" {
		t.Errorf("no stored keys: %q", got)
	}

	// With the record committed, the key can't go, lose its mark or change
	// its arguments; its text can change.
	for name, tc := range map[string]struct{ src, msg string }{
		"deleted":    {`{"dev.other": {"text": "Other", "note": "n"}}`, "a stored key was deleted"},
		"unmarked":   {`{"dev.alert_failed": {"text": "{name} could not be processed", "args": [["name", "user"]], "note": "n"}}`, `must keep "stored": true`},
		"renamed":    {`{"dev.alert_failed": {"text": "{who} could not be processed", "args": [["who", "user"]], "note": "n", "stored": true}}`, "arguments can never change"},
		"retyped":    {`{"dev.alert_failed": {"text": "{name} could not be processed", "args": [["name", "text"]], "note": "n", "stored": true}}`, "arguments can never change"},
		"new arg":    {`{"dev.alert_failed": {"text": "{name} could not be processed: {why}", "args": [["name", "user"], ["why", "text"]], "note": "n", "stored": true}}`, "arguments can never change"},
		"text moved": {`{"dev.alert_failed": {"text": "Could not process {name}", "args": [["name", "user"]], "note": "n", "stored": true}}`, ""},
	} {
		ds := check(t, map[string]string{enFile("dev"): tc.src, StoredFile: rec}, CheckOptions{})
		if tc.msg == "" {
			wantNoErrors(t, ds)
			continue
		}
		if len(find(ds, Error, "dev.alert_failed", tc.msg)) == 0 {
			t.Errorf("%s: no error %q:\n%s", name, tc.msg, dump(ds))
		}
	}

	// A record that isn't valid is an error of its own.
	ds := check(t, map[string]string{enFile("dev"): en, StoredFile: `{"dev.alert_failed": "name"}`}, CheckOptions{})
	want(t, ds, Error, "dev.alert_failed", `[["name", "type"], ...]`)
}
