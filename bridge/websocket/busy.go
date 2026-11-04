// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// cBusyPrefix starts the payload of a pong in which a device names the
// requests it is still working on (the device's websocket.BusyPrefix):
// then their ids, as uvarints. The bridge waits for a request from the
// last of those (forwardWithTimeout) instead of from when it was sent, so
// a long one - a post's videos cut and shrunk on the device, a large file
// over a slow line - isn't taken for a device that stopped answering. A
// device from before this sends none, and is waited for as before.
const cBusyPrefix = "otcbusy"

// busyIDs reads the ids of such a pong; none for any other pong, or a
// payload that doesn't read whole.
func busyIDs(payload string) []int32 {
	rest, ok := strings.CutPrefix(payload, cBusyPrefix)
	if !ok {
		return nil
	}
	var ids []int32
	for b := []byte(rest); len(b) > 0; {
		v, n := protowire.ConsumeVarint(b)
		if n < 0 || v > 1<<32-1 {
			return nil
		}
		ids = append(ids, int32(uint32(v)))
		b = b[n:]
	}
	return ids
}
