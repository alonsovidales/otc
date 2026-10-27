// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/raidwatch"
)

// Settings › Storage: a disk plugged in while the mirror lacks one, which
// the device didn't add by itself because something is on it, can be
// erased and added at the owner's word (raidwatch/mirrordisk.go; the root
// watcher, scripts/raid_watch.py, checks everything again before it erases
// anything). The disks offered come in Status (mirror_candidates), the
// choice in UseMirrorDisk. The owner of the primary instance only: the
// demo and container gates (demoGate, containerGate) and a user's instance
// (sup == nil) answer before these.

// Seams for the tests: raidwatch's answers.
var (
	mirrorCandidates = raidwatch.Candidates
	mirrorUseDisk    = raidwatch.UseDisk
)

// mirrorCandidatesProto is Status.mirror_candidates.
func mirrorCandidatesProto() []*pb.MirrorDiskCandidate {
	var out []*pb.MirrorDiskCandidate
	for _, c := range mirrorCandidates() {
		out = append(out, &pb.MirrorDiskCandidate{Id: c.ID, Model: c.Model, Size: c.Size})
	}
	return out
}

// useMirrorDisk answers UseMirrorDisk on the primary instance: the generic
// Ack at once, ok once the request is handed over; a refusal before
// anything is asked carries its code and words both in the Ack and on the
// envelope, as demoRefusal does.
func useMirrorDisk(req *pb.UseMirrorDisk, resp *pb.RespEnvelope, lang string) {
	err := mirrorUseDisk(req.GetDisk())
	if err == nil {
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		return
	}
	var refusal *raidwatch.Refusal
	code := ""
	if errors.As(err, &refusal) {
		code = refusal.Code
	} else {
		log.Error("could not ask for the disk to be used for the mirror:", err)
	}
	msg := errText(lang, err)
	resp.Error = true
	resp.ErrorCode = code
	resp.ErrorMessage = msg
	resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: msg, Code: code}}
}
