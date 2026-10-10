// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"strings"
	"testing"
)

func TestLanguagesFile(t *testing.T) {
	en := `{"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"}`
	routes := `"routes": {"common": ["web"]}`
	for name, tc := range map[string]struct{ src, msg string }{
		"bad code":           {`{"languages": [` + en + `, {"code": "ES", "tag": "es", "apple": "es", "android": "es", "name": "E", "status": "draft"}], ` + routes + `}`, `code "ES" must be two or three lowercase letters`},
		"twice":              {`{"languages": [` + en + `, ` + en + `], ` + routes + `}`, `code "en" is listed twice`},
		"pseudo":             {`{"languages": [` + en + `, {"code": "qps", "tag": "en-XA", "apple": "x", "android": "x", "name": "P", "status": "draft"}], ` + routes + `}`, "reserved for the pseudo-locale"},
		"bad tag":            {`{"languages": [` + en + `, {"code": "pt", "tag": "pt_PT", "apple": "pt-PT", "android": "pt", "name": "P", "status": "draft"}], ` + routes + `}`, "not a canonical BCP 47 tag"},
		"other language":     {`{"languages": [` + en + `, {"code": "pt", "tag": "es", "apple": "pt", "android": "pt", "name": "P", "status": "draft"}], ` + routes + `}`, "is for another language"},
		"no android":         {`{"languages": [` + en + `, {"code": "es", "tag": "es", "apple": "es", "android": "", "name": "E", "status": "draft"}], ` + routes + `}`, `"android" is the values-<qualifier>`},
		"status":             {`{"languages": [` + en + `, {"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "E", "status": "beta"}], ` + routes + `}`, "status must be"},
		"english first":      {`{"languages": [{"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "E", "status": "draft"}, ` + en + `], ` + routes + `}`, "must be the first language"},
		"english draft":      {`{"languages": [` + strings.Replace(en, "shipping", "draft", 1) + `], ` + routes + `}`, `English must be "shipping"`},
		"unknown field":      {`{"languages": [` + en + `], ` + routes + `, "extra": 1}`, `unknown field "extra"`},
		"unknown consumer":   {`{"languages": [` + en + `], "routes": {"common": ["web", "tv"]}}`, "unknown consumer"},
		"bad root":           {`{"languages": [` + en + `], "routes": {"Common": ["web"]}}`, "a root is one lowercase segment"},
		"empty route":        {`{"languages": [` + en + `], "routes": {"common": []}}`, "must list its consumers"},
		"duplicate route":    {`{"languages": [` + en + `], "routes": {"common": ["web"], "common": ["ios"]}}`, "duplicate key"},
		"duplicate consumer": {`{"languages": [` + en + `], "routes": {"common": ["web", "web"]}}`, `lists "web" twice`},
	} {
		ds := check(t, map[string]string{LanguagesFile: tc.src}, CheckOptions{})
		if got := find(ds, Error, "", tc.msg); len(got) == 0 || got[0].File != LanguagesFile {
			t.Errorf("%s: want an error %q in %s, got:\n%s", name, tc.msg, LanguagesFile, dump(ds))
		}
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("Load without languages.json must fail")
	}
}

func TestNames(t *testing.T) {
	for _, tc := range []struct{ in, android, camel, goName string }{
		{"app.photos.deleted_by", "app_photos_deleted_by", "appPhotosDeletedBy", "AppPhotosDeletedBy"},
		{"common.ok", "common_ok", "commonOk", "CommonOk"},
		{"dev.err.a1_b2", "dev_err_a1_b2", "devErrA1B2", "DevErrA1B2"},
		{"ios.x.1y", "ios_x_1y", "iosX1y", "IosX1y"},
	} {
		if got := AndroidName(tc.in); got != tc.android {
			t.Errorf("AndroidName(%s) = %s", tc.in, got)
		}
		if got := CamelName(tc.in); got != tc.camel {
			t.Errorf("CamelName(%s) = %s", tc.in, got)
		}
		if got := GoName(tc.in); got != tc.goName {
			t.Errorf("GoName(%s) = %s", tc.in, got)
		}
	}
	if PrefixFileName("app.photos") != "app_photos" || PrefixTypeName("app.photos") != "AppPhotos" || PrefixTypeName("common") != "Common" {
		t.Error("prefix names")
	}
	if ArgName("file_name") != "fileName" {
		t.Error("ArgName")
	}
	for _, tc := range []struct {
		name      string
		consumers []string
		want      string
	}{
		{"when", []string{ConsumerGo}, ""},
		{"when", []string{ConsumerAndroid}, "Kotlin"},
		{"type", []string{ConsumerGo, ConsumerOtcSync}, "Go"},
		{"default", []string{ConsumerWeb, ConsumerIOS, ConsumerAndroid, ConsumerMacOS, ConsumerOtcSync, ConsumerWizard}, "Go,Swift,TypeScript"},
		{"in", []string{ConsumerIOS, ConsumerAndroid}, "Kotlin,Swift"},
		{"name", []string{ConsumerWeb, ConsumerIOS, ConsumerAndroid, ConsumerGo}, ""},
		{"class", []string{ConsumerWizard}, ""},
	} {
		if got := strings.Join(ReservedIn(tc.name, tc.consumers), ","); got != tc.want {
			t.Errorf("ReservedIn(%s, %v) = %q, want %q", tc.name, tc.consumers, got, tc.want)
		}
	}
}
