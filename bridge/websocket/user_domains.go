// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"database/sql"
	"strings"

	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/google/uuid"
)

// ReqRegisterUserDomain's refusals (RespEnvelope.error_code).
const (
	CodeNameTaken   = "name_taken"
	CodeNameHeld    = "name_held"
	CodeDomainLimit = "domain_limit"
	CodeInvalidName = "invalid_name"
)

// userDomainStore is what registerUserDomain needs of the database.
type userDomainStore interface {
	IsValidDevice(owner, domain, secret string) (defined, validSecret bool, err error)
	DomainAccount(domain string) (accountID string, registered bool, err error)
	ClaimUserAddress(domain, primary string) error
	ReplaceUserAddressIdentity(accountID, primary, domain, owner, secret string) (ok bool, err error)
	NameHold(domain, accountID string) (forOthers, forAccount bool, err error)
	NameHeldForPrimary(domain, accountID, primary string) (bool, error)
	CountAccountDomains(accountID string) (n int, err error)
	RegisterUserAddress(accountID, primary, owner, domain, secret string) error
	LogAuthEvent(id, domain, owner, addr, reason string) error
}

// SetIdentityNotice sets how an account's owner is told that a user's
// address now leads to a new identity (main: accounts.NotifyNewIdentity).
func (mg *Manager) SetIdentityNotice(notify func(accountID, domain string)) {
	mg.identityNotice = notify
}

// registerUserDomain answers ReqRegisterUserDomain into resp: a primary
// device registering one of its users' addresses (issue #103) under its
// own account. Since issue #124 an address dialling in is turned away
// unless an account holds it, and nothing else gave a user's address one.
//
// Any device of the account can send this, and a session alone puts a
// device on an account (new names need no confirmation), so it moves no
// name the owner hasn't confirmed: a free name is registered for the user,
// counting toward accounts.MaxDomains; only an address this primary
// registered for one of its users goes to a new identity (the user added
// again), or comes back while held after its release (devices.primary_domain,
// bridge/dao/user_addresses.go). The account's other names - its devices',
// another primary's users' - are name_taken here, and a held one
// name_held: they move with setup and a fresh sign-in, or the account
// page's password. A name another account holds or holds back is refused
// too. A name that moves is emailed to the owner, as elsewhere.
func (mg *Manager) registerUserDomain(req *pb.ReqRegisterUserDomain, resp *pb.RespEnvelope, remote string) {
	mg.answerUserDomain(req, resp, registerUserDomainIn(mg.dao, req, mg.baseHost(), remote))
}

// answerUserDomain puts r, registerUserDomainIn's answer to req, into
// resp, after acting on it.
func (mg *Manager) answerUserDomain(req *pb.ReqRegisterUserDomain, resp *pb.RespEnvelope, r userDomainResult) {
	if r.code != "" || r.msg != "" {
		resp.Error = true
		resp.ErrorCode = r.code
		resp.ErrorMessage = r.msg
		return
	}
	if r.replaced {
		// The address's old identity (a user deleted and added again)
		// stops relaying, on every node.
		mg.DropReplacedIdentity(req.UserDomain, req.UserOwnerUuid)
	}
	if r.newIdentity && mg.identityNotice != nil {
		mg.identityNotice(r.accountID, req.UserDomain)
	}
	resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
}

// userDomainResult is registerUserDomainIn's answer.
type userDomainResult struct {
	// code and msg are the refusal: both empty when the address is the
	// user's now.
	code, msg string
	// accountID is the primary's account.
	accountID string
	// replaced: the address had an older identity, which is no longer
	// valid.
	replaced bool
	// newIdentity: the address leads to an identity it didn't lead to a
	// moment ago - replaced, or a held name taken back - which every
	// client and friend that knows it now reaches: the account's owner is
	// told.
	newIdentity bool
}

// userDomainRefusal is a refusal with code and msg.
func userDomainRefusal(code, msg string) userDomainResult {
	return userDomainResult{code: code, msg: msg}
}

// registerUserDomainIn is registerUserDomain's work.
func registerUserDomainIn(store userDomainStore, req *pb.ReqRegisterUserDomain, baseHost, remote string) userDomainResult {
	defined, valid, err := store.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
	if err != nil && err != sql.ErrNoRows {
		log.Error("error validating the device registering a user's address:", err)
		return userDomainRefusal("", cInternalErrorMsg)
	}
	if !defined || !valid {
		if logErr := store.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, remote, "invalid_secret"); logErr != nil {
			log.Error("error logging auth event:", logErr)
		}
		return userDomainRefusal("", relayText(i18n.ApiRelayInvalidSecret()))
	}

	// One label under the bridge's own domain, and a real identity: the
	// user's instance presents it forever after.
	name, under := strings.CutSuffix(req.UserDomain, "."+baseHost)
	if baseHost == "" || !under || !accounts.NamePattern.MatchString(name) || accounts.ReservedNames[name] ||
		len(req.UserOwnerUuid) < 16 || len(req.UserOwnerUuid) > 64 || len(req.UserSecret) < 32 || len(req.UserSecret) > 128 {
		return userDomainRefusal(CodeInvalidName, "not a user address on this bridge") // i18n-ignore: reaches only a broken device's log
	}

	accountID, _, err := store.DomainAccount(req.Domain)
	if err != nil {
		log.Error("error reading the account of a device registering a user's address:", err)
		return userDomainRefusal("", cInternalErrorMsg)
	}
	owner, registered, err := store.DomainAccount(req.UserDomain)
	if err != nil {
		log.Error("error checking a user's address:", err)
		return userDomainRefusal("", cInternalErrorMsg)
	}
	if registered {
		if owner != accountID {
			return userDomainRefusal(CodeNameTaken, relayText(i18n.ApiErrorNameTaken()))
		}
		if d, v, err := store.IsValidDevice(req.UserOwnerUuid, req.UserDomain, req.UserSecret); err == nil && d && v {
			// Already this user's. Asked with the address's own identity,
			// which only the device the user is on has: the address is
			// this primary's user's (how one registered before the bridge
			// recorded that gets it).
			if accountID != "" && req.UserDomain != req.Domain {
				if err := store.ClaimUserAddress(req.UserDomain, req.Domain); err != nil {
					log.Error("could not record", req.UserDomain, "as a user's address of", req.Domain, ":", err)
				}
			}
			return userDomainResult{accountID: accountID}
		}
		if accountID == "" {
			// Two domains from before accounts: nothing says they are
			// one owner's.
			return userDomainRefusal(CodeNameTaken, relayText(i18n.ApiErrorNameTaken()))
		}
		// A user added again: only an address this primary registered
		// for one of its users. Any other name of the account moves with
		// its owner's confirmation, never on a device's word.
		ok, err := store.ReplaceUserAddressIdentity(accountID, req.Domain, req.UserDomain, req.UserOwnerUuid, req.UserSecret)
		if err != nil {
			log.Error("error handing a user's address to its new identity:", err)
			return userDomainRefusal("", cInternalErrorMsg)
		}
		if !ok {
			log.Info("refused", req.Domain, "a new identity for", req.UserDomain, ": not one of its users' addresses")
			return userDomainRefusal(CodeNameTaken, relayText(i18n.ApiErrorNameTaken()))
		}
		log.Info("user address handed to a new identity by its primary:", req.UserDomain)
		return userDomainResult{accountID: accountID, replaced: true, newIdentity: true}
	}

	// A name held after its release comes back only to the user of this
	// primary it was released by: as a held name its account takes back
	// elsewhere, it reaches whoever knew it.
	heldForOthers, heldForAccount, err := store.NameHold(req.UserDomain, accountID)
	if err != nil {
		log.Error("could not check whether", req.UserDomain, "is held:", err)
		return userDomainRefusal("", cInternalErrorMsg)
	}
	if heldForOthers {
		return userDomainRefusal(CodeNameHeld, relayText(i18n.ApiErrorNameHeld()))
	}
	if heldForAccount {
		mine, err := store.NameHeldForPrimary(req.UserDomain, accountID, req.Domain)
		if err != nil {
			log.Error("could not check whose user", req.UserDomain, "was:", err)
			return userDomainRefusal("", cInternalErrorMsg)
		}
		if !mine {
			log.Info("refused", req.Domain, "the held name", req.UserDomain, ": not one of its users' addresses")
			return userDomainRefusal(CodeNameHeld, relayText(i18n.ApiErrorNameHeld()))
		}
	}
	if accountID != "" {
		n, err := store.CountAccountDomains(accountID)
		if err != nil {
			log.Error("error counting an account's domains:", err)
			return userDomainRefusal("", cInternalErrorMsg)
		}
		if n >= accounts.MaxDomains {
			return userDomainRefusal(CodeDomainLimit, relayText(i18n.ApiErrorDomainLimit(accounts.MaxDomains, accounts.ExtraDomainEUR, accounts.ContactEmail)))
		}
	}
	// The primary's own domain may predate accounts: its users' are then
	// registered the way it was, with none.
	if err := store.RegisterUserAddress(accountID, req.Domain, req.UserOwnerUuid, req.UserDomain, req.UserSecret); err != nil {
		if dao.IsDuplicateKey(err) {
			return userDomainRefusal(CodeNameTaken, relayText(i18n.ApiErrorNameTaken()))
		}
		log.Error("error registering a user's address:", err)
		return userDomainRefusal("", cInternalErrorMsg)
	}
	log.Info("user address registered by its device:", req.UserDomain)
	return userDomainResult{accountID: accountID, newIdentity: heldForAccount}
}
