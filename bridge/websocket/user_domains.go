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
	ReplaceDeviceIdentity(accountID, domain, owner, secret string) (ok bool, err error)
	NameHeld(domain, accountID string) (bool, error)
	CountAccountDomains(accountID string) (n int, err error)
	RegisterAccountDevice(accountID, owner, domain, secret string) error
	RegistreDevice(owner, domain, secret string) error
	LogAuthEvent(id, domain, owner, addr, reason string) error
}

// registerUserDomain answers ReqRegisterUserDomain into resp: a primary
// device registering one of its users' addresses (issue #103) under its
// own account. Since issue #124 an address dialling in is turned away
// unless an account holds it, and nothing else gave a user's address one.
// The rules are claimName's for an account adding a name: the account's
// own name is handed to the new identity, a name another account holds
// or holds back is refused, and the address counts toward
// accounts.MaxDomains.
func (mg *Manager) registerUserDomain(req *pb.ReqRegisterUserDomain, resp *pb.RespEnvelope, remote string) {
	code, msg, replaced := registerUserDomainIn(mg.dao, req, mg.baseHost(), remote)
	if code != "" || msg != "" {
		resp.Error = true
		resp.ErrorCode = code
		resp.ErrorMessage = msg
		return
	}
	if replaced {
		// The address's old identity (a user deleted and added again)
		// stops relaying, on every node.
		mg.DropReplacedIdentity(req.UserDomain, req.UserOwnerUuid)
	}
	resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
}

// registerUserDomainIn is registerUserDomain's work: the refusal's code
// and message (both empty when the address is the user's now), and
// whether an older identity of the account's was replaced.
func registerUserDomainIn(store userDomainStore, req *pb.ReqRegisterUserDomain, baseHost, remote string) (code, msg string, replaced bool) {
	defined, valid, err := store.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
	if err != nil && err != sql.ErrNoRows {
		log.Error("error validating the device registering a user's address:", err)
		return "", cInternalErrorMsg, false
	}
	if !defined || !valid {
		if logErr := store.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, remote, "invalid_secret"); logErr != nil {
			log.Error("error logging auth event:", logErr)
		}
		return "", relayText(i18n.ApiRelayInvalidSecret()), false
	}

	// One label under the bridge's own domain, and a real identity: the
	// user's instance presents it forever after.
	name, under := strings.CutSuffix(req.UserDomain, "."+baseHost)
	if baseHost == "" || !under || !accounts.NamePattern.MatchString(name) || accounts.ReservedNames[name] ||
		len(req.UserOwnerUuid) < 16 || len(req.UserOwnerUuid) > 64 || len(req.UserSecret) < 32 || len(req.UserSecret) > 128 {
		return CodeInvalidName, "not a user address on this bridge", false // i18n-ignore: reaches only a broken device's log
	}

	accountID, _, err := store.DomainAccount(req.Domain)
	if err != nil {
		log.Error("error reading the account of a device registering a user's address:", err)
		return "", cInternalErrorMsg, false
	}
	owner, registered, err := store.DomainAccount(req.UserDomain)
	if err != nil {
		log.Error("error checking a user's address:", err)
		return "", cInternalErrorMsg, false
	}
	if registered {
		if owner != accountID {
			return CodeNameTaken, relayText(i18n.ApiErrorNameTaken()), false
		}
		if d, v, err := store.IsValidDevice(req.UserOwnerUuid, req.UserDomain, req.UserSecret); err == nil && d && v {
			return "", "", false // already this user's
		}
		if accountID == "" {
			// Two domains from before accounts: nothing says they are
			// one owner's.
			return CodeNameTaken, relayText(i18n.ApiErrorNameTaken()), false
		}
		ok, err := store.ReplaceDeviceIdentity(accountID, req.UserDomain, req.UserOwnerUuid, req.UserSecret)
		if err != nil || !ok {
			log.Error("error handing a user's address to its new identity:", err)
			return "", cInternalErrorMsg, false
		}
		log.Info("user address handed to a new identity by its account:", req.UserDomain)
		return "", "", true
	}

	if held, err := store.NameHeld(req.UserDomain, accountID); err != nil {
		log.Error("could not check whether", req.UserDomain, "is held:", err)
	} else if held {
		return CodeNameHeld, relayText(i18n.ApiErrorNameHeld()), false
	}
	if accountID != "" {
		var n int
		if n, err = store.CountAccountDomains(accountID); err != nil {
			log.Error("error counting an account's domains:", err)
			return "", cInternalErrorMsg, false
		}
		if n >= accounts.MaxDomains {
			return CodeDomainLimit, relayText(i18n.ApiErrorDomainLimit(accounts.MaxDomains, accounts.ExtraDomainEUR, accounts.ContactEmail)), false
		}
		err = store.RegisterAccountDevice(accountID, req.UserOwnerUuid, req.UserDomain, req.UserSecret)
	} else {
		// The primary's own domain predates accounts: its users' are
		// registered the way it was.
		err = store.RegistreDevice(req.UserOwnerUuid, req.UserDomain, req.UserSecret)
	}
	if err != nil {
		if dao.IsDuplicateKey(err) {
			return CodeNameTaken, relayText(i18n.ApiErrorNameTaken()), false
		}
		log.Error("error registering a user's address:", err)
		return "", cInternalErrorMsg, false
	}
	log.Info("user address registered by its device:", req.UserDomain)
	return "", "", false
}
