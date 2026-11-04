// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import (
	"errors"
	"runtime"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Password hashing is the costliest work a request can ask of the bridge:
// a bcrypt at BcryptCost takes about a quarter of a second of one core. The
// per-address limits bound one client, but nothing bounded them all - many
// networks at once (a botnet, or one IPv6 /48 rotating through its 65,536
// /64s) could keep every core hashing and starve the relay. So at most
// cHashSlots hashes run at once on a node; one that can't start within
// hashWait is refused as busy (ErrBusy), a 503 to try again in a moment,
// instead of queuing without bound.

// cHashSlots is how many bcrypts may run at once: half the cores, at least
// one, so the relay always keeps the other half.
var cHashSlots = max(1, runtime.NumCPU()/2)

var (
	hashSlots = make(chan struct{}, cHashSlots)
	// hashWait is how long a hash waits for a slot (tests shorten it).
	hashWait = 3 * time.Second
)

// HashRetryAfter is the Retry-After a busy answer carries.
const HashRetryAfter = 5 * time.Second

// ErrBusy: the node is hashing as much as it may right now.
var ErrBusy = errors.New("too much password hashing at once: try again in a moment") // i18n-ignore: compared with errors.Is; the handlers answer with a code

// withHashSlot runs fn in a hash slot, or answers ErrBusy when none frees
// up within hashWait.
func withHashSlot(fn func()) error {
	select {
	case hashSlots <- struct{}{}:
	default:
		t := time.NewTimer(hashWait)
		defer t.Stop()
		select {
		case hashSlots <- struct{}{}:
		case <-t.C:
			return ErrBusy
		}
	}
	defer func() { <-hashSlots }()
	fn()
	return nil
}

// HashPassword is a new password's bcrypt at BcryptCost, made in a slot:
// ErrBusy, or bcrypt's own error (ErrPasswordTooLong for more than 72
// bytes).
func HashPassword(password string) (string, error) {
	var hash []byte
	var err error
	if busy := withHashSlot(func() { hash, err = bcrypt.GenerateFromPassword([]byte(password), BcryptCost) }); busy != nil {
		return "", busy
	}
	return string(hash), err
}

// CheckPassword is whether password is hash's, checked in a slot. err is
// only ever ErrBusy: a hash that doesn't parse matches nothing.
func CheckPassword(hash, password string) (match bool, err error) {
	err = withHashSlot(func() { match = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil })
	return match, err
}
