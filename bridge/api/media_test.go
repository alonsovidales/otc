// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "testing"

// Issue #110: the Range header is the whole contract between a video
// player and this proxy - misreading one means either the wrong bytes or
// a player that refuses to start.
func TestParseRange(t *testing.T) {
	for _, tc := range []struct {
		name      string
		header    string
		wantErr   bool
		present   bool
		start     int64
		end       int64
		hasEnd    bool
		suffixLen int64
	}{
		{name: "no header means the whole file", header: ""},
		// What every browser sends to open a video.
		{name: "open-ended from the start", header: "bytes=0-", present: true},
		{name: "a seek lands mid-file", header: "bytes=1048576-2097151", present: true, start: 1048576, end: 2097151, hasEnd: true},
		// A player probing an MP4's header asks for a few bytes;
		// answering with megabytes breaks the contract and wastes the
		// bandwidth this feature exists to save.
		{name: "a short probe keeps its end", header: "bytes=0-31", present: true, end: 31, hasEnd: true},
		{name: "an end before the start is malformed", header: "bytes=100-50", wantErr: true},
		// Players read an MP4's trailing index this way when the moov
		// atom is at the end, so this has to be understood rather than
		// treated as a start offset of zero.
		{name: "suffix range", header: "bytes=-1024", present: true, suffixLen: 1024},
		{name: "multi-range takes the first span", header: "bytes=100-199,300-399", present: true, start: 100, end: 199, hasEnd: true},
		{name: "whitespace is tolerated", header: " bytes=42- ", present: true, start: 42},
		{name: "a unit that isn't bytes is refused", header: "items=0-1", wantErr: true},
		{name: "a missing dash is malformed", header: "bytes=100", wantErr: true},
		{name: "a negative start is malformed", header: "bytes=-0", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRange(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRange(%q) = %+v, want an error", tc.header, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q): %v", tc.header, err)
			}
			if got.present != tc.present || got.start != tc.start || got.suffixLen != tc.suffixLen ||
				got.end != tc.end || got.hasEnd != tc.hasEnd {
				t.Errorf("parseRange(%q) = %+v, want {present:%v start:%d end:%d hasEnd:%v suffixLen:%d}",
					tc.header, got, tc.present, tc.start, tc.end, tc.hasEnd, tc.suffixLen)
			}
		})
	}
}
