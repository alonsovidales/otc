// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/bridge/accounts"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// fakeDomains is a devices table in memory for registerUserDomainIn,
// with the names held after their release.
type fakeDomains struct {
	rows      map[string]fakeDevice // domain ->
	held      map[string]fakeHold   // domain ->
	holdErr   error
	insertErr error
	authLog   []string
}

// fakeDevice is a devices row: primary is primary_domain, the device whose
// user's address it is ("" for any other name).
type fakeDevice struct{ owner, secret, account, primary string }

// fakeHold is a released_domains row (account "": released by no account).
type fakeHold struct{ account, primary string }

func (f *fakeDomains) IsValidDevice(owner, domain, secret string) (bool, bool, error) {
	r, ok := f.rows[domain]
	if !ok {
		return false, false, nil
	}
	return true, r.owner == owner && r.secret == secret, nil
}

func (f *fakeDomains) DomainAccount(domain string) (string, bool, error) {
	r, ok := f.rows[domain]
	return r.account, ok, nil
}

func (f *fakeDomains) ClaimUserAddress(domain, primary string) error {
	if r, ok := f.rows[domain]; ok && r.primary == "" {
		r.primary = primary
		f.rows[domain] = r
	}
	return nil
}

// ReplaceUserAddressIdentity is the dao's `where domain = ? and account_id
// = ? and primary_domain = ?`.
func (f *fakeDomains) ReplaceUserAddressIdentity(accountID, primary, domain, owner, secret string) (bool, error) {
	r, ok := f.rows[domain]
	if !ok || r.account != accountID || r.primary != primary {
		return false, nil
	}
	f.rows[domain] = fakeDevice{owner, secret, accountID, primary}
	return true, nil
}

func (f *fakeDomains) NameHold(domain, accountID string) (bool, bool, error) {
	if f.holdErr != nil {
		return false, false, f.holdErr
	}
	h, ok := f.held[domain]
	if !ok {
		return false, false, nil
	}
	if accountID != "" && h.account == accountID {
		return false, true, nil
	}
	return true, false, nil
}

func (f *fakeDomains) NameHeldForPrimary(domain, accountID, primary string) (bool, error) {
	h, ok := f.held[domain]
	return ok && h.account == accountID && h.primary == primary, nil
}

func (f *fakeDomains) CountAccountDomains(accountID string) (int, error) {
	n := 0
	for _, r := range f.rows {
		if r.account == accountID {
			n++
		}
	}
	return n, nil
}

func (f *fakeDomains) RegisterUserAddress(accountID, primary, owner, domain, secret string) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.rows[domain] = fakeDevice{owner, secret, accountID, primary}
	return nil
}

func (f *fakeDomains) LogAuthEvent(id, domain, owner, addr, reason string) error {
	f.authLog = append(f.authLog, domain+" "+reason)
	return nil
}

const (
	cTestBase       = "off-the.cloud"
	cTestUserUUID   = "0b6f2a54-user-4c1e-9d55-7f00aa11bb22"
	cTestUserSecret = "user-secret-0123456789abcdefghijklmnop"
)

// newDomains has Cala, its account's primary, and Ana, another account's.
func newDomains() *fakeDomains {
	return &fakeDomains{
		rows: map[string]fakeDevice{
			"cala.off-the.cloud": {"cala-owner", "cala-secret", "acc-cala", ""},
			"ana.off-the.cloud":  {"ana-owner", "ana-secret", "acc-ana", ""},
		},
		held: map[string]fakeHold{},
	}
}

func userDomainReq(userDomain string) *pb.ReqRegisterUserDomain {
	return &pb.ReqRegisterUserDomain{
		OwnerUuid: "cala-owner", Domain: "cala.off-the.cloud", Secret: "cala-secret",
		UserDomain: userDomain, UserOwnerUuid: cTestUserUUID, UserSecret: cTestUserSecret,
	}
}

// ok is an answer that registered (or confirmed) the address.
func (r userDomainResult) ok() bool { return r.code == "" && r.msg == "" }

// A free name goes to the asking device's account, with the user's own
// identity - what its instance then registers with - as that device's
// user's address.
func TestRegisterUserDomainTakesAFreeNameForTheAccount(t *testing.T) {
	f := newDomains()
	r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if !r.ok() || r.replaced || r.newIdentity || r.accountID != "acc-cala" {
		t.Fatalf("expected the name registered, got %+v", r)
	}
	if got := f.rows["pepe.off-the.cloud"]; got != (fakeDevice{cTestUserUUID, cTestUserSecret, "acc-cala", "cala.off-the.cloud"}) {
		t.Fatalf("registered as %+v", got)
	}
	if d, v, _ := f.IsValidDevice(cTestUserUUID, "pepe.off-the.cloud", cTestUserSecret); !d || !v {
		t.Fatal("the user's instance must now register with its identity")
	}

	// Asked again (every start of the device): nothing changes.
	if r = registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); !r.ok() || r.replaced || r.newIdentity {
		t.Fatalf("asking again must be ok and change nothing, got %+v", r)
	}
}

// Only a registered device with its secret may ask, and a wrong secret is
// logged like any other.
func TestRegisterUserDomainNeedsTheDevicesSecret(t *testing.T) {
	f := newDomains()
	req := userDomainReq("pepe.off-the.cloud")
	req.Secret = "wrong"
	if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); r.code != "" || r.msg == "" {
		t.Fatalf("expected a refusal without a code, got %+v", r)
	}
	if _, ok := f.rows["pepe.off-the.cloud"]; ok {
		t.Fatal("nothing may be registered for a wrong secret")
	}
	if len(f.authLog) != 1 || !strings.HasSuffix(f.authLog[0], "invalid_secret") {
		t.Fatalf("expected one invalid_secret event, got %v", f.authLog)
	}
}

func TestRegisterUserDomainRefusesWhatIsNotAUserAddress(t *testing.T) {
	for _, d := range []string{
		"pepe.example.com",        // another domain
		"off-the.cloud",           // the bridge itself
		"www.off-the.cloud",       // reserved
		"a.b.off-the.cloud",       // two labels
		"-pepe.off-the.cloud",     // not a DNS label
		"PEPE.off-the.cloud",      // upper case
		"pepe.off-the.cloud.evil", // a suffix, not the domain
	} {
		f := newDomains()
		if r := registerUserDomainIn(f, userDomainReq(d), cTestBase, "1.2.3.4"); r.code != CodeInvalidName {
			t.Errorf("%s: expected %s, got %+v", d, CodeInvalidName, r)
		}
		if len(f.rows) != 2 {
			t.Errorf("%s: nothing may be registered", d)
		}
	}
	f := newDomains()
	req := userDomainReq("pepe.off-the.cloud")
	req.UserSecret = "short"
	if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); r.code != CodeInvalidName {
		t.Errorf("a blank-ish identity must be refused, got %+v", r)
	}
}

// Another account's name, or one held back after another account let it
// go, is never taken.
func TestRegisterUserDomainRefusesAnotherAccountsName(t *testing.T) {
	f := newDomains()
	if r := registerUserDomainIn(f, userDomainReq("ana.off-the.cloud"), cTestBase, "1.2.3.4"); r.code != CodeNameTaken {
		t.Fatalf("expected %s, got %+v", CodeNameTaken, r)
	}
	if f.rows["ana.off-the.cloud"].owner != "ana-owner" {
		t.Fatal("ana's identity must stay")
	}
	for _, h := range []fakeHold{{account: "acc-ana", primary: "ana.off-the.cloud"}, {}} {
		f.held["pepe.off-the.cloud"] = h
		if r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); r.code != CodeNameHeld {
			t.Fatalf("held as %+v: expected %s, got %+v", h, CodeNameHeld, r)
		}
		if _, ok := f.rows["pepe.off-the.cloud"]; ok {
			t.Fatalf("held as %+v: nothing may be registered", h)
		}
	}
}

// A user deleted and added again under the same name: the address this
// primary registered for it goes to the new identity, and the owner is
// told.
func TestRegisterUserDomainHandsItsUsersAddressToANewIdentity(t *testing.T) {
	f := newDomains()
	f.rows["pepe.off-the.cloud"] = fakeDevice{"old-user", "old-secret", "acc-cala", "cala.off-the.cloud"}
	r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if !r.ok() || !r.replaced || !r.newIdentity || r.accountID != "acc-cala" {
		t.Fatalf("expected the name handed over, got %+v", r)
	}
	if got := f.rows["pepe.off-the.cloud"]; got != (fakeDevice{cTestUserUUID, cTestUserSecret, "acc-cala", "cala.off-the.cloud"}) {
		t.Fatalf("now %+v", got)
	}
}

// Any device of the account may ask - including one registered a moment
// ago with a session alone, which needs no confirmation - so only the
// asking primary's own users' addresses move here. The account's device
// names (the one setup registered, one added on the account page), another
// primary's users' and the asking device's own name stay as they are:
// moving those takes their owner's confirmation.
func TestRegisterUserDomainNeverMovesTheAccountsOtherNames(t *testing.T) {
	const evilOwner, evilSecret = "evil-owner-0123456789", "evil-secret-0123456789abcdefghijklmnop"
	rows := map[string]fakeDevice{
		"casa.off-the.cloud":   {"casa-owner", "casa-secret", "acc1", ""},
		"byhand.off-the.cloud": {"byhand-owner", "byhand-secret", "acc1", ""},
		"pepe.off-the.cloud":   {"pepe-owner", "pepe-secret", "acc1", "casa.off-the.cloud"},
		// Registered with a stale session (claim or the account page).
		"newpi.off-the.cloud": {evilOwner, evilSecret, "acc1", ""},
	}
	for _, c := range []struct{ asker, name string }{
		{"newpi", "casa"},
		{"newpi", "byhand"},
		{"newpi", "pepe"},
		{"casa", "newpi"},
		{"casa", "casa"},
	} {
		f := &fakeDomains{rows: map[string]fakeDevice{}, held: map[string]fakeHold{}}
		for d, r := range rows {
			f.rows[d] = r
		}
		asker := f.rows[c.asker+".off-the.cloud"]
		req := &pb.ReqRegisterUserDomain{
			OwnerUuid: asker.owner, Domain: c.asker + ".off-the.cloud", Secret: asker.secret,
			UserDomain: c.name + ".off-the.cloud", UserOwnerUuid: "attacker-uuid-0123456789", UserSecret: "attacker-secret-0123456789abcdefghijklmnop",
		}
		r := registerUserDomainIn(f, req, cTestBase, "198.51.100.9")
		if r.code != CodeNameTaken || r.replaced || r.newIdentity {
			t.Errorf("%s asking for %s: expected %s, got %+v", c.asker, c.name, CodeNameTaken, r)
		}
		for d, want := range rows {
			if f.rows[d] != want {
				t.Errorf("%s asking for %s: %s is now %+v", c.asker, c.name, d, f.rows[d])
			}
		}
	}
}

// A held name comes back here only as the user's address of the primary
// asking that it was when it was released: the user added again. The
// account's other released names, and another primary's released users,
// are the owner's to take back on the account page.
func TestRegisterUserDomainTakesBackOnlyItsOwnUsersHeldNames(t *testing.T) {
	f := newDomains()
	f.rows["newpi.off-the.cloud"] = fakeDevice{"newpi-owner", "newpi-secret", "acc-cala", ""}
	f.held["pepe.off-the.cloud"] = fakeHold{account: "acc-cala", primary: "cala.off-the.cloud"}
	f.held["casa.off-the.cloud"] = fakeHold{account: "acc-cala"}

	newpi := userDomainReq("pepe.off-the.cloud")
	newpi.OwnerUuid, newpi.Domain, newpi.Secret = "newpi-owner", "newpi.off-the.cloud", "newpi-secret"
	if r := registerUserDomainIn(f, newpi, cTestBase, "1.2.3.4"); r.code != CodeNameHeld || r.newIdentity {
		t.Fatalf("another device taking cala's released user: expected %s, got %+v", CodeNameHeld, r)
	}
	for _, req := range []*pb.ReqRegisterUserDomain{userDomainReq("casa.off-the.cloud"), func() *pb.ReqRegisterUserDomain {
		r := userDomainReq("casa.off-the.cloud")
		r.OwnerUuid, r.Domain, r.Secret = "newpi-owner", "newpi.off-the.cloud", "newpi-secret"
		return r
	}()} {
		if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); r.code != CodeNameHeld || r.newIdentity {
			t.Fatalf("%s taking a released device name: expected %s, got %+v", req.Domain, CodeNameHeld, r)
		}
	}
	if _, ok := f.rows["pepe.off-the.cloud"]; ok {
		t.Fatal("nothing may be registered for a refused held name")
	}
	if _, ok := f.rows["casa.off-the.cloud"]; ok {
		t.Fatal("nothing may be registered for a refused held name")
	}

	r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if !r.ok() || r.replaced || !r.newIdentity {
		t.Fatalf("cala taking its released user back: expected it registered, got %+v", r)
	}
	if got := f.rows["pepe.off-the.cloud"]; got != (fakeDevice{cTestUserUUID, cTestUserSecret, "acc-cala", "cala.off-the.cloud"}) {
		t.Fatalf("registered as %+v", got)
	}
}

// An address registered before the bridge recorded whose user's it is
// becomes its primary's when the primary asks for it with the address's
// own identity (every start of the primary); then, and only then, that
// primary may give it a new identity.
func TestRegisterUserDomainRecordsItsUsersAddressesFromTheirIdentity(t *testing.T) {
	f := newDomains()
	f.rows["pepe.off-the.cloud"] = fakeDevice{cTestUserUUID, cTestUserSecret, "acc-cala", ""}
	req := userDomainReq("pepe.off-the.cloud")
	req.UserOwnerUuid, req.UserSecret = "new-user-0123456789", "new-secret-0123456789abcdefghijklmnop"
	if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); r.code != CodeNameTaken {
		t.Fatalf("a new identity for an address not yet known to be cala's user's: expected %s, got %+v", CodeNameTaken, r)
	}

	if r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); !r.ok() || r.replaced || r.newIdentity {
		t.Fatalf("asking with the address's identity: expected ok, got %+v", r)
	}
	if got := f.rows["pepe.off-the.cloud"].primary; got != "cala.off-the.cloud" {
		t.Fatalf("expected pepe recorded as cala's user's address, got %q", got)
	}
	if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); !r.ok() || !r.replaced {
		t.Fatalf("now cala's user's address: expected the new identity, got %+v", r)
	}

	// A primary asking for its own name with its own identity records
	// nothing.
	self := userDomainReq("cala.off-the.cloud")
	self.UserOwnerUuid, self.UserSecret = "cala-owner-0123456789", "cala-secret-0123456789abcdefghijklmnop"
	f.rows["cala.off-the.cloud"] = fakeDevice{self.UserOwnerUuid, self.UserSecret, "acc-cala", ""}
	self.OwnerUuid, self.Secret = self.UserOwnerUuid, self.UserSecret
	if r := registerUserDomainIn(f, self, cTestBase, "1.2.3.4"); !r.ok() {
		t.Fatalf("expected ok, got %+v", r)
	}
	if got := f.rows["cala.off-the.cloud"].primary; got != "" {
		t.Fatalf("a primary's own name recorded as its user's: %q", got)
	}
}

// A hold that can't be read is "try again", never a registration.
func TestRegisterUserDomainHoldErrors(t *testing.T) {
	f := newDomains()
	f.holdErr = errors.New("connection refused")
	if r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); r.code != "" || r.msg != cInternalErrorMsg {
		t.Fatalf("expected the internal error, got %+v", r)
	}
	if _, ok := f.rows["pepe.off-the.cloud"]; ok {
		t.Fatal("nothing may be registered when the hold can't be read")
	}
}

// The users' addresses count toward the account's limit.
func TestRegisterUserDomainKeepsTheAccountsLimit(t *testing.T) {
	f := newDomains()
	for i := 1; len(f.rows) < accounts.MaxDomains+1; i++ { // cala + MaxDomains-1 users, plus ana
		f.rows[strings.Repeat("u", i)+".off-the.cloud"] = fakeDevice{"o", "s", "acc-cala", "cala.off-the.cloud"}
	}
	if r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); r.code != CodeDomainLimit || r.msg == "" {
		t.Fatalf("expected %s, got %+v", CodeDomainLimit, r)
	}
	if _, ok := f.rows["pepe.off-the.cloud"]; ok {
		t.Fatal("nothing may be registered over the limit")
	}
}

// A device whose own domain predates accounts registers its users' the
// way it was registered; two such domains are never taken for one owner's.
func TestRegisterUserDomainForADomainWithoutAccount(t *testing.T) {
	f := newDomains()
	f.rows["old.off-the.cloud"] = fakeDevice{"old-owner", "old-secret", "", ""}
	req := userDomainReq("pepe.off-the.cloud")
	req.OwnerUuid, req.Domain, req.Secret = "old-owner", "old.off-the.cloud", "old-secret"
	if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); !r.ok() {
		t.Fatalf("expected the name registered, got %+v", r)
	}
	if got := f.rows["pepe.off-the.cloud"]; got.account != "" || got.owner != cTestUserUUID {
		t.Fatalf("registered as %+v", got)
	}

	f.rows["other.off-the.cloud"] = fakeDevice{"someone", "their-secret", "", ""}
	req.UserDomain = "other.off-the.cloud"
	if r := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); r.code != CodeNameTaken {
		t.Fatalf("expected %s, got %+v", CodeNameTaken, r)
	}
	if f.rows["other.off-the.cloud"].owner != "someone" {
		t.Fatal("the other domain's identity must stay")
	}
}

// Losing a race for the name is "taken", any other insert error is not.
func TestRegisterUserDomainInsertErrors(t *testing.T) {
	f := newDomains()
	f.insertErr = errors.New("Error 1062: Duplicate entry 'pepe.off-the.cloud' for key 'PRIMARY'")
	if r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); r.code != CodeNameTaken {
		t.Fatalf("expected %s, got %+v", CodeNameTaken, r)
	}
	f.insertErr = errors.New("connection refused")
	if r := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); r.code != "" || r.msg != cInternalErrorMsg {
		t.Fatalf("expected the internal error, got %+v", r)
	}
}

// The answer: a refusal carries its code, a new identity is told to the
// account's owner (once), and anything else is a plain ack.
func TestAnswerUserDomain(t *testing.T) {
	var told []string
	mg := &Manager{bridges: map[string]*bridgePool{}, identityNotice: func(accountID, domain string) {
		told = append(told, accountID+" "+domain)
	}}
	req := userDomainReq("pepe.off-the.cloud")
	for _, c := range []struct {
		r    userDomainResult
		told bool
	}{
		{userDomainRefusal(CodeNameTaken, "taken"), false},
		{userDomainResult{accountID: "acc-cala"}, false},
		{userDomainResult{accountID: "acc-cala", replaced: true, newIdentity: true}, true},
		{userDomainResult{accountID: "acc-cala", newIdentity: true}, true},
	} {
		told = nil
		resp := &pb.RespEnvelope{}
		mg.answerUserDomain(req, resp, c.r)
		if resp.Error != (c.r.code != "") || resp.ErrorCode != c.r.code {
			t.Errorf("%+v: answered %+v", c.r, resp)
		}
		if !resp.Error && resp.GetRespAck() == nil {
			t.Errorf("%+v: no ack", c.r)
		}
		if want := []string{"acc-cala pepe.off-the.cloud"}; c.told && (len(told) != 1 || told[0] != want[0]) || !c.told && len(told) != 0 {
			t.Errorf("%+v: told %v", c.r, told)
		}
	}
}
