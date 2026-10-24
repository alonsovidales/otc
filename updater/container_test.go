// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"reflect"
	"testing"
)

func versions(rs []Release) []int {
	var out []int
	for _, r := range rs {
		out = append(out, r.Version)
	}
	return out
}

func pendingInfo() *Info {
	return &Info{
		CurrentVersion: 140, CurrentLabel: "2.0",
		LatestVersion: 143, LatestLabel: "2.3",
		KindsVerified: true,
		Pending: []Release{
			{Version: 141, Kind: KindCritical, Label: "2.1", Description: "Fix."},
			{Version: 142, Kind: KindMajor, Label: "2.2", Description: "More."},
			{Version: 143, Kind: KindMinor, Label: "2.3", Description: "Less."},
		},
	}
}

// In a container only the releases up to the newest published image are
// offered: the newest is its release, and the alert follows from that.
func TestOnlyPublishedImages(t *testing.T) {
	var asked []int
	info := pendingInfo()
	onlyPublishedImages(info, func(v int) bool { asked = append(asked, v); return v <= 142 })
	if got := versions(info.Pending); !reflect.DeepEqual(got, []int{141, 142}) {
		t.Errorf("pending %v, want [141 142]", got)
	}
	if info.LatestVersion != 142 || info.LatestLabel != "2.2" {
		t.Errorf("latest %d %q, want 142 2.2", info.LatestVersion, info.LatestLabel)
	}
	if !reflect.DeepEqual(asked, []int{143, 142}) {
		t.Errorf("asked about %v, want the newest first, stopping at the first published", asked)
	}
	if a := alertFor(info); a == nil || a.Target != 142 || a.Version != "2.2" || a.Level != KindCritical {
		t.Errorf("alert %+v", a)
	}
}

// No image published yet: nothing pending, the installed release is the
// latest, and there is nothing to alert about.
func TestOnlyPublishedImagesNoneYet(t *testing.T) {
	info := pendingInfo()
	onlyPublishedImages(info, func(int) bool { return false })
	if len(info.Pending) != 0 || info.LatestVersion != 140 || info.LatestLabel != "2.0" {
		t.Errorf("%+v", info)
	}
	if a := alertFor(info); a != nil {
		t.Errorf("alert %+v", a)
	}
	if a, ok := nextAlert(nil, info, nil); a != nil || !ok {
		t.Errorf("next alert %+v %v", a, ok)
	}
}

// At most cImageLookups releases are asked about per check, newest first.
func TestOnlyPublishedImagesBoundsTheLookups(t *testing.T) {
	info := &Info{CurrentVersion: 100}
	for v := 101; v <= 120; v++ {
		info.Pending = append(info.Pending, Release{Version: v})
	}
	asked := 0
	onlyPublishedImages(info, func(v int) bool { asked++; return v <= 105 })
	if asked != cImageLookups || len(info.Pending) != 0 || info.LatestVersion != 100 {
		t.Errorf("asked %d times, pending %v, latest %d", asked, versions(info.Pending), info.LatestVersion)
	}
}

// Nothing pending: nothing is asked and nothing changes.
func TestOnlyPublishedImagesNothingPending(t *testing.T) {
	info := &Info{CurrentVersion: 140, LatestVersion: 140}
	onlyPublishedImages(info, func(int) bool { t.Error("asked the registry"); return false })
	if info.LatestVersion != 140 || info.Pending != nil {
		t.Errorf("%+v", info)
	}
}

// In a container Update runs nothing and says how the device is updated.
func TestApplyInAContainer(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "1")
	err := Apply()
	if err == nil || err.Error() != "This device runs in Docker and is updated with its image: on its server, run docker compose pull and then docker compose up -d." {
		t.Errorf("Apply: %v", err)
	}
}
