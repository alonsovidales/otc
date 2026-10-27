// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "testing"

// A ws:// address is for the home network only.
func TestUnencrypted(t *testing.T) {
	for domain, want := range map[string]bool{
		"ws://192.168.1.10:8080/ws": false, "ws://10.0.0.2/ws": false, "ws://172.20.0.5/ws": false,
		"ws://127.0.0.1:8080/ws": false, "ws://[::1]:8080/ws": false, "ws://[fd12::5]/ws": false,
		"ws://169.254.3.4/ws": false, "ws://nas.local:8080/ws": false, "ws://otc/ws": false,
		"ws://otc.lan/ws": false, "ws://otc.home.arpa/ws": false, "ws://localhost:8080/ws": false,
		"ws://203.0.113.7:8080/ws": true, "ws://otc.example.com/ws": true, "WS://[2001:db8::1]/ws": true,
		"wss://otc.example.com/ws": false, "cala.off-the.cloud": false,
	} {
		if got := Unencrypted(domain); got != want {
			t.Errorf("Unencrypted(%q) = %v, want %v", domain, got, want)
		}
	}
}

// A folder is synced once - not again, nor inside or around another.
func TestPathOverlap(t *testing.T) {
	synced := []NamedPath{{Path: "/home/ana/Photos", Name: "Photos"}, {Path: "/mnt/Résumés/", Name: "Résumés"}}
	for _, tc := range []struct {
		path       string
		ignoreCase bool
		want       OverlapKind
		other      string
	}{
		{"/home/ana/Photos", false, OverlapSame, "Photos"},
		{"/home/ana/Photos/", false, OverlapSame, "Photos"},
		{"/home/ana/Photos/2024", false, OverlapInside, "Photos"},
		{"/home/ana", false, OverlapHolds, "Photos"},
		{"/home/ana/Photos2", false, 0, ""},
		{"/home/ana/photos/2024", false, 0, ""},
		{"/home/ana/photos/2024", true, OverlapInside, "Photos"},
		{"/mnt/Résumés/cv", false, OverlapInside, "Résumés"},
		{"/", false, OverlapHolds, "Photos"},
	} {
		o := PathOverlap(tc.path, "new", synced, tc.ignoreCase)
		switch {
		case tc.want == 0 && o != nil:
			t.Errorf("%q: overlaps %+v", tc.path, o)
		case tc.want != 0 && (o == nil || o.Kind != tc.want || o.Other != tc.other):
			t.Errorf("%q: %+v, want %v with %q", tc.path, o, tc.want, tc.other)
		}
	}
}

// FolderOverlap checks this computer's folders and the device's.
func TestFolderOverlap(t *testing.T) {
	cfg := &Config{RemoteFolders: []RemoteFolder{{ID: "r1", RemotePath: "/Photos", LocalPath: "/home/ana/Fotos"}}}
	device := func(p string) string { return "/linux/pc" + p }
	if o := cfg.FolderOverlap("/home/ana/Elsewhere", "/Photos/2024", device); o == nil || o.Kind != OverlapInside {
		t.Errorf("a device folder inside one synced: %+v", o)
	}
	if o := cfg.FolderOverlap("/home/ana/Fotos/sub", device("/home/ana/Fotos/sub"), device); o == nil || o.Kind != OverlapInside {
		t.Errorf("a local folder inside one synced: %+v", o)
	}
	if o := cfg.FolderOverlap("/home/ana/Music", device("/home/ana/Music"), device); o != nil {
		t.Errorf("a folder of its own: %+v", o)
	}
}
