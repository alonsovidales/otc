// SPDX-License-Identifier: AGPL-3.0-or-later

package mailer

import (
	"errors"
	"fmt"
	"net/textproto"
	"testing"
)

func TestAddressRefused(t *testing.T) {
	rcpt := func(code int, msg string) error { return &RecipientError{Err: &textproto.Error{Code: code, Msg: msg}} }
	for _, c := range []struct {
		err  error
		want bool
	}{
		{rcpt(550, "5.1.1 <x@y.z>: Recipient address rejected: User unknown"), true},
		{rcpt(550, "5.1.2 Bad destination system address"), true},
		{rcpt(553, "5.1.3 Invalid recipient address"), true},
		{rcpt(556, "5.1.10 Recipient address has null MX"), true},
		{fmt.Errorf("sending: %w", rcpt(550, "5.1.1 no such user")), true},
		// May clear up, or not about the recipient's address.
		{rcpt(450, "4.1.1 Recipient address rejected: try again"), false},
		{rcpt(452, "4.5.3 Too many recipients"), false},
		{rcpt(550, "5.7.1 Daily user sending limit exceeded"), false},
		{rcpt(554, "5.7.1 <info@off-the.cloud>: Sender address rejected: not owned by user"), false},
		{rcpt(553, "5.1.8 <info@off-the.cloud>: Sender address rejected: Domain not found"), false},
		{rcpt(550, "Requested action not taken: mailbox unavailable"), false},
		{rcpt(550, ""), false},
		{&RecipientError{Err: errors.New("connection reset by peer")}, false},
		{&textproto.Error{Code: 535, Msg: "5.7.8 Authentication credentials invalid"}, false},
		{&textproto.Error{Code: 550, Msg: "5.1.1 not at RCPT TO"}, false},
		{ErrNotConfigured, false},
		{nil, false},
	} {
		if got := AddressRefused(c.err); got != c.want {
			t.Errorf("AddressRefused(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
