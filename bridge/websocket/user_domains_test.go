// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/bridge/accounts"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// fakeDomains is a devices table in memory for registerUserDomainIn.
type fakeDomains struct {
	rows      map[string]fakeDevice // domain ->
	held      map[string]bool
	insertErr error
	authLog   []string
}

type fakeDevice struct{ owner, secret, account string }

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

func (f *fakeDomains) ReplaceDeviceIdentity(accountID, domain, owner, secret string) (bool, error) {
	r, ok := f.rows[domain]
	if !ok || r.account != accountID {
		return false, nil
	}
	f.rows[domain] = fakeDevice{owner, secret, accountID}
	return true, nil
}

func (f *fakeDomains) NameHeld(domain, accountID string) (bool, error) { return f.held[domain], nil }

func (f *fakeDomains) CountAccountDomains(accountID string) (int, error) {
	n := 0
	for _, r := range f.rows {
		if r.account == accountID {
			n++
		}
	}
	return n, nil
}

func (f *fakeDomains) RegisterAccountDevice(accountID, owner, domain, secret string) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.rows[domain] = fakeDevice{owner, secret, accountID}
	return nil
}

func (f *fakeDomains) RegistreDevice(owner, domain, secret string) error {
	return f.RegisterAccountDevice("", owner, domain, secret)
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

// newDomains has Cala, its account's, and Ana, another account's.
func newDomains() *fakeDomains {
	return &fakeDomains{
		rows: map[string]fakeDevice{
			"cala.off-the.cloud": {"cala-owner", "cala-secret", "acc-cala"},
			"ana.off-the.cloud":  {"ana-owner", "ana-secret", "acc-ana"},
		},
		held: map[string]bool{},
	}
}

func userDomainReq(userDomain string) *pb.ReqRegisterUserDomain {
	return &pb.ReqRegisterUserDomain{
		OwnerUuid: "cala-owner", Domain: "cala.off-the.cloud", Secret: "cala-secret",
		UserDomain: userDomain, UserOwnerUuid: cTestUserUUID, UserSecret: cTestUserSecret,
	}
}

// A free name goes to the asking device's account, with the user's own
// identity - what its instance then registers with.
func TestRegisterUserDomainTakesAFreeNameForTheAccount(t *testing.T) {
	f := newDomains()
	code, msg, replaced := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if code != "" || msg != "" || replaced {
		t.Fatalf("expected the name registered, got %q %q %v", code, msg, replaced)
	}
	if got := f.rows["pepe.off-the.cloud"]; got != (fakeDevice{cTestUserUUID, cTestUserSecret, "acc-cala"}) {
		t.Fatalf("registered as %+v", got)
	}
	if d, v, _ := f.IsValidDevice(cTestUserUUID, "pepe.off-the.cloud", cTestUserSecret); !d || !v {
		t.Fatal("the user's instance must now register with its identity")
	}

	// Asked again (every start of the device): nothing changes.
	code, msg, replaced = registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if code != "" || msg != "" || replaced {
		t.Fatalf("asking again must be ok and change nothing, got %q %q %v", code, msg, replaced)
	}
}

// Only a registered device with its secret may ask, and a wrong secret is
// logged like any other.
func TestRegisterUserDomainNeedsTheDevicesSecret(t *testing.T) {
	f := newDomains()
	req := userDomainReq("pepe.off-the.cloud")
	req.Secret = "wrong"
	code, msg, _ := registerUserDomainIn(f, req, cTestBase, "1.2.3.4")
	if code != "" || msg == "" {
		t.Fatalf("expected a refusal without a code, got %q %q", code, msg)
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
		if code, _, _ := registerUserDomainIn(f, userDomainReq(d), cTestBase, "1.2.3.4"); code != CodeInvalidName {
			t.Errorf("%s: expected %s, got %q", d, CodeInvalidName, code)
		}
		if len(f.rows) != 2 {
			t.Errorf("%s: nothing may be registered", d)
		}
	}
	f := newDomains()
	req := userDomainReq("pepe.off-the.cloud")
	req.UserSecret = "short"
	if code, _, _ := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); code != CodeInvalidName {
		t.Errorf("a blank-ish identity must be refused, got %q", code)
	}
}

// Another account's name, or one held back after another account let it
// go, is never taken.
func TestRegisterUserDomainRefusesAnotherAccountsName(t *testing.T) {
	f := newDomains()
	if code, _, _ := registerUserDomainIn(f, userDomainReq("ana.off-the.cloud"), cTestBase, "1.2.3.4"); code != CodeNameTaken {
		t.Fatalf("expected %s, got %q", CodeNameTaken, code)
	}
	if f.rows["ana.off-the.cloud"].owner != "ana-owner" {
		t.Fatal("ana's identity must stay")
	}
	f.held["pepe.off-the.cloud"] = true
	if code, _, _ := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); code != CodeNameHeld {
		t.Fatalf("expected %s, got %q", CodeNameHeld, code)
	}
}

// A user deleted and added again under the same name: the account's own
// address goes to the new identity.
func TestRegisterUserDomainHandsTheAccountsNameToANewIdentity(t *testing.T) {
	f := newDomains()
	f.rows["pepe.off-the.cloud"] = fakeDevice{"old-user", "old-secret", "acc-cala"}
	code, msg, replaced := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if code != "" || msg != "" || !replaced {
		t.Fatalf("expected the name handed over, got %q %q %v", code, msg, replaced)
	}
	if got := f.rows["pepe.off-the.cloud"]; got.owner != cTestUserUUID || got.secret != cTestUserSecret {
		t.Fatalf("still the old identity: %+v", got)
	}
}

// The users' addresses count toward the account's limit.
func TestRegisterUserDomainKeepsTheAccountsLimit(t *testing.T) {
	f := newDomains()
	for i := 1; len(f.rows) < accounts.MaxDomains+1; i++ { // cala + MaxDomains-1 users, plus ana
		f.rows[strings.Repeat("u", i)+".off-the.cloud"] = fakeDevice{"o", "s", "acc-cala"}
	}
	code, msg, _ := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4")
	if code != CodeDomainLimit || msg == "" {
		t.Fatalf("expected %s, got %q %q", CodeDomainLimit, code, msg)
	}
	if _, ok := f.rows["pepe.off-the.cloud"]; ok {
		t.Fatal("nothing may be registered over the limit")
	}
}

// A device whose own domain predates accounts registers its users' the
// way it was registered; two such domains are never taken for one owner's.
func TestRegisterUserDomainForADomainWithoutAccount(t *testing.T) {
	f := newDomains()
	f.rows["old.off-the.cloud"] = fakeDevice{"old-owner", "old-secret", ""}
	req := userDomainReq("pepe.off-the.cloud")
	req.OwnerUuid, req.Domain, req.Secret = "old-owner", "old.off-the.cloud", "old-secret"
	if code, msg, _ := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); code != "" || msg != "" {
		t.Fatalf("expected the name registered, got %q %q", code, msg)
	}
	if got := f.rows["pepe.off-the.cloud"]; got.account != "" || got.owner != cTestUserUUID {
		t.Fatalf("registered as %+v", got)
	}

	f.rows["other.off-the.cloud"] = fakeDevice{"someone", "their-secret", ""}
	req.UserDomain = "other.off-the.cloud"
	if code, _, _ := registerUserDomainIn(f, req, cTestBase, "1.2.3.4"); code != CodeNameTaken {
		t.Fatalf("expected %s, got %q", CodeNameTaken, code)
	}
	if f.rows["other.off-the.cloud"].owner != "someone" {
		t.Fatal("the other domain's identity must stay")
	}
}

// Losing a race for the name is "taken", any other insert error is not.
func TestRegisterUserDomainInsertErrors(t *testing.T) {
	f := newDomains()
	f.insertErr = errors.New("Error 1062: Duplicate entry 'pepe.off-the.cloud' for key 'PRIMARY'")
	if code, _, _ := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); code != CodeNameTaken {
		t.Fatalf("expected %s, got %q", CodeNameTaken, code)
	}
	f.insertErr = errors.New("connection refused")
	if code, msg, _ := registerUserDomainIn(f, userDomainReq("pepe.off-the.cloud"), cTestBase, "1.2.3.4"); code != "" || msg != cInternalErrorMsg {
		t.Fatalf("expected the internal error, got %q %q", code, msg)
	}
}
