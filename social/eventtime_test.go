// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #149: a friend's post keeps the time its author published it, not
// the moment it synced in.
func TestEventTimeKeepsTheOriginalTime(t *testing.T) {
	posted := time.Date(2024, 5, 17, 18, 30, 0, 0, time.UTC)
	stamped := time.Date(2024, 5, 17, 18, 31, 0, 0, time.UTC)
	ev := &pb.Event{Dt: timestamppb.New(stamped)}

	if got := eventTime(posted.Unix(), ev); !got.Equal(posted) {
		t.Errorf("with the author's time: got %v, want %v", got, posted)
	}
	if got := eventTime(0, ev); !got.Equal(stamped) {
		t.Errorf("without it, the event's own time: got %v, want %v", got, stamped)
	}
	if got := eventTime(0, &pb.Event{}); time.Since(got) > time.Minute {
		t.Errorf("with no time at all, now: got %v", got)
	}
	future := time.Now().Add(48 * time.Hour)
	if got := eventTime(future.Unix(), ev); got.After(time.Now()) {
		t.Errorf("a time in the future must be capped at now, got %v", got)
	}
}
