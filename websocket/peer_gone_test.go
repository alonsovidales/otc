// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"io"
	"testing"

	gorilla "github.com/gorilla/websocket"
)

// A peer going away is not logged as an error; anything else still is.
func TestPeerGone(t *testing.T) {
	for _, err := range []error{
		&gorilla.CloseError{Code: gorilla.CloseAbnormalClosure, Text: "unexpected EOF"},
		&gorilla.CloseError{Code: gorilla.CloseNormalClosure},
		fmt.Errorf("read: %w", io.ErrUnexpectedEOF),
		io.EOF,
	} {
		if !peerGone(err) {
			t.Errorf("%v: want peerGone", err)
		}
	}
	if peerGone(errors.New("frame too large")) || peerGone(&gorilla.CloseError{Code: gorilla.CloseMessageTooBig}) {
		t.Error("a real error was taken for a peer going away")
	}
}
