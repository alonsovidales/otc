// SPDX-License-Identifier: AGPL-3.0-or-later

package bgprocessor

import (
	"testing"

	"github.com/alonsovidales/otc/session"
)

func TestSetSession(t *testing.T) {
	bg := &BgProcessor{}
	ses := &session.Session{Uuid: "test-uuid"}

	bg.SetSession(ses)

	if bg.session != ses {
		t.Error("SetSession did not store the provided session")
	}
}
