// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// cCodeMemoriesOff is RespEnvelope.error_code on GetMemories while On this
// day is off: the apps hide it.
const cCodeMemoriesOff = "memories_off"

// hiddenMemory is HideMemory's kind and value as the device keeps them.
func hiddenMemory(req *pb.HideMemory) dao.HiddenMemory {
	kind := ""
	switch req.Kind {
	case pb.MemoryHideKind_MemoryHidePhoto:
		kind = dao.HideMemoryPhoto
	case pb.MemoryHideKind_MemoryHideDay:
		kind = dao.HideMemoryDay
	case pb.MemoryHideKind_MemoryHideMemory:
		kind = dao.HideMemoryMemory
	case pb.MemoryHideKind_MemoryHidePerson:
		kind = dao.HideMemoryPerson
	}
	return dao.HiddenMemory{Kind: kind, Value: req.Value}
}
