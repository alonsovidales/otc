// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestSocialStatusToPb(t *testing.T) {
	sc := &Social{}

	cases := map[string]pb.FriendShipStatus{
		"pending":  pb.FriendShipStatus_Pending,
		"accepted": pb.FriendShipStatus_Accepted,
		"blocked":  pb.FriendShipStatus_Blocked,
		"unknown":  pb.FriendShipStatus_Pending, // zero value fallback
	}

	for in, want := range cases {
		if got := sc.statusToPb(in); got != want {
			t.Errorf("statusToPb(%q) = %v, want %v", in, got, want)
		}
	}
}

// isAllowedFriendDomain is the one guard between an inbound friend request
// (reachable pre-auth, from anyone) and this device dialing out to
// whatever domain it names — the connectToDevice callers all route
// through it precisely so a request naming a LAN address or arbitrary
// internal hostname can't turn into an SSRF primitive.
func TestIsAllowedFriendDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   bool
	}{
		{"pit.off-the.cloud", true},
		{"cala.off-the.cloud", true},
		{"off-the.cloud", true},     // the bare TLD itself
		{"PIT.OFF-THE.CLOUD", true}, // domains are case-insensitive
		{"evil.com", false},
		{"off-the.cloud.evil.com", false}, // suffix trick
		{"192.168.1.1", false},            // LAN address
		{"169.254.169.254", false},        // cloud metadata address
		{"localhost", false},
		{"notoff-the.cloud", false}, // must be a *sub*domain, not just a suffix match on the raw string
		{"", false},
	}

	for _, c := range cases {
		if got := isAllowedFriendDomain(c.domain); got != c.want {
			t.Errorf("isAllowedFriendDomain(%q) = %v, want %v", c.domain, got, c.want)
		}
	}
}

// NewPublication's wait for a just-uploaded file's background thumbnail
// was measured giving up (5s total, 20 attempts of 250ms) before a real,
// un-raced thumbnail actually finished (8s, on a Raspberry Pi, once the
// thumbnail step started doing EXIF/HEIC/orientation work for every image)
// — a real "publish a brand new photo" failure, not a test of the polling
// mechanism itself (that's exercised by NewPublication's own retry loop in
// practice, not worth re-deriving with a fake filesmanager here). This
// pins the total budget somewhere comfortably past what was actually
// measured, so a future change to either constant can't silently shrink it
// back down under that again.
func TestThumbnailPollBudgetCoversMeasuredWorstCase(t *testing.T) {
	const measuredWorstCase = 8 * time.Second
	total := cThumbnailPollInterval * time.Duration(cThumbnailPollAttempts)
	if total <= measuredWorstCase {
		t.Errorf("total poll budget %v does not comfortably cover the measured 8s worst case", total)
	}
}

// shouldCompressForSocial is NewPublication's actual decision of whether a
// file needs compressing (issue #60) - pinned directly here since driving
// NewPublication itself needs a real (or faked) filesmanager, same
// reasoning as TestThumbnailPollBudgetCoversMeasuredWorstCase above.
func TestShouldCompressForSocial(t *testing.T) {
	cases := []struct {
		name string
		mime string
		size int
		want bool
	}{
		{"small video stays as-is", "video/mp4", 5 * 1024 * 1024, false},
		{"oversized video gets compressed", "video/mp4", 11 * 1024 * 1024, true},
		{"exactly at the limit stays as-is", "video/mp4", cSocialVideoSizeLimit, false},
		{"one byte over the limit gets compressed", "video/mp4", cSocialVideoSizeLimit + 1, true},
		{"oversized quicktime video gets compressed", "video/quicktime", 15 * 1024 * 1024, true},
		{"large image is never touched", "image/jpeg", 20 * 1024 * 1024, false},
		{"empty mime is never touched", "", 20 * 1024 * 1024, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldCompressForSocial(c.mime, c.size); got != c.want {
				t.Errorf("shouldCompressForSocial(%q, %d) = %v, want %v", c.mime, c.size, got, c.want)
			}
		})
	}
}
