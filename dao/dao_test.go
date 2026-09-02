package dao

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestStatusToPb(t *testing.T) {
	d := &Dao{}

	cases := map[string]pb.FriendShipStatus{
		"pending":  pb.FriendShipStatus_Pending,
		"accepted": pb.FriendShipStatus_Accepted,
		"blocked":  pb.FriendShipStatus_Blocked,
		"unknown":  pb.FriendShipStatus_Pending, // zero value fallback
	}

	for in, want := range cases {
		if got := d.statusToPb(in); got != want {
			t.Errorf("statusToPb(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPbToStatus(t *testing.T) {
	d := &Dao{}

	cases := map[pb.FriendShipStatus]string{
		pb.FriendShipStatus_Pending:  "pending",
		pb.FriendShipStatus_Accepted: "accepted",
		pb.FriendShipStatus_Blocked:  "blocked",
	}

	for in, want := range cases {
		if got := d.pbToStatus(in); got != want {
			t.Errorf("pbToStatus(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusToPbAndPbToStatusRoundTrip(t *testing.T) {
	d := &Dao{}

	for _, s := range []string{"pending", "accepted", "blocked"} {
		if got := d.pbToStatus(d.statusToPb(s)); got != s {
			t.Errorf("round trip for %q produced %q", s, got)
		}
	}
}
