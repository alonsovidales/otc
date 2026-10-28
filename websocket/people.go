// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// cMaxRemoveFromPerson is the most photos one RemoveFromPerson may name:
// far more than anyone picks in a grid, and still a small request (64
// bytes a hash) and a few statements of dao's chunks.
const cMaxRemoveFromPerson = 10000

// removeFromPersonRefusal is why a RemoveFromPerson is refused before
// anything is read, or nil: it names a person, at least one photo and at
// most cMaxRemoveFromPerson, and a person to move them to that isn't the
// one they leave (whether that one exists is the transaction's to say:
// dao.ErrNoSuchPerson).
func removeFromPersonRefusal(req *pb.RemoveFromPerson) *i18n.Msg {
	var m i18n.Msg
	switch {
	case req.GetPersonId() == "":
		m = i18n.DevFilesPersonMissing()
	case len(req.GetHashes()) == 0:
		m = i18n.DevFilesPersonNoPhotos()
	case len(req.GetHashes()) > cMaxRemoveFromPerson:
		m = i18n.DevFilesPersonTooMany(cMaxRemoveFromPerson)
	case req.GetToPersonId() == req.GetPersonId():
		m = i18n.DevFilesPersonSame()
	default:
		return nil
	}
	return &m
}
