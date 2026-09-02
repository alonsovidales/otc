package social

import (
	"testing"

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
