// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Codes before prose: a device says it in the code (its message may be in
// another language), a build before v9 only in English; a message that
// comes with a code is never read. Same cases as the Mac's ErrorCodesTests.
func TestUnknownPayload(t *testing.T) {
	cases := []struct {
		name string
		resp *pb.RespEnvelope
		want bool
	}{
		{"no answer", nil, false},
		{"the code", &pb.RespEnvelope{Error: true, ErrorCode: "unknown_payload",
			ErrorMessage: "This device does not understand that request: its software is older than the app. Check for updates in Settings."}, true},
		{"the code, a translated message", &pb.RespEnvelope{Error: true, ErrorCode: "unknown_payload",
			ErrorMessage: "Este dispositivo no entiende esa petición"}, true},
		{"a build before v9", &pb.RespEnvelope{Error: true, ErrorMessage: "unknown payload"}, true},
		{"another error", &pb.RespEnvelope{Error: true, ErrorMessage: "database is busy"}, false},
		{"another code with the old message", &pb.RespEnvelope{Error: true, ErrorCode: "local_unavailable", ErrorMessage: "unknown payload"}, false},
		{"not an error", &pb.RespEnvelope{ErrorMessage: "unknown payload"}, false},
	}
	for _, c := range cases {
		if got := unknownPayload(c.resp); got != c.want {
			t.Errorf("%s: unknownPayload = %v, want %v", c.name, got, c.want)
		}
	}
}
