// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"

	"github.com/alonsovidales/otc/log"
)

// A user's address (issue #103, pepe.off-the.cloud) is registered by the
// primary device the user is on, over the websocket
// (ReqRegisterUserDomain), and given a new identity the same way when the
// user is added again. Any device of the account can send that request,
// and whoever gets a name receives every client and friend that knows it,
// so the primary may move only its own users' addresses that way - the
// account's other names move only with their owner's confirmation (setup
// with a fresh sign-in, the account page's password).
//
// devices.primary_domain records whose user's address a name is: set as
// the primary registers it (RegisterUserAddress), or asks for it again with
// the address's own identity (ClaimUserAddress, at every start of the
// primary - how the addresses registered before the column get theirs);
// NULL for every other name. released_domains.primary_domain carries it
// while a released address is held (recordRelease), so the user added
// again under the same name gets it back (NameHeldForPrimary). A name moved
// with its owner's confirmation (ReplaceDeviceIdentity) loses it, and so
// does every user's address of a primary that is released (forgetPrimary):
// whoever registers that name later is not the device the users were on.
//
// Without the columns (migration 015 not run), new addresses are
// registered as before, and no name moves over the websocket.

var (
	cDevicesPrimary  = schemaPart{table: "devices", column: "primary_domain", migration: "015-user-addresses.sql"}
	cReleasedPrimary = schemaPart{table: "released_domains", column: "primary_domain", migration: "015-user-addresses.sql"}
)

// RegisterUserAddress registers domain, the address of one of primary's
// users, with that user's identity: under accountID, the primary's account
// (RegisterAccountDevice), or with no account for a primary from before
// accounts (RegistreDevice).
func (dao *Dao) RegisterUserAddress(accountID, primary, owner, domain, secret string) error {
	cols, vals := []string{"owner_uuid", "domain"}, []any{owner, domain}
	if accountID != "" {
		cols, vals = append(cols, "account_id", "created"), append(vals, accountID, time.Now())
	}
	return dao.devicePrimary.runPart(cDevicesPrimary, func() error {
		return dao.insertDevice(append(append([]string{}, cols...), "primary_domain"), append(append([]any{}, vals...), primary), secret)
	}, func() error {
		return dao.insertDevice(cols, vals, secret)
	})
}

// ClaimUserAddress records primary as the device domain is a user's
// address of, unless one is recorded already. The caller has checked that
// primary asked for it with the address's own identity, which only the
// device the user is on has.
func (dao *Dao) ClaimUserAddress(domain, primary string) error {
	return dao.devicePrimary.runPart(cDevicesPrimary, func() error {
		_, err := dao.db.Exec("update `devices` set `primary_domain` = ? where `domain` = ? and `primary_domain` is null", primary, domain)
		return err
	}, func() error { return nil })
}

// ReplaceUserAddressIdentity is ReplaceDeviceIdentity for primary asking
// for one of its users' addresses: ok only when domain is accountID's and
// a user's address of primary. Any other name of the account - another
// device's, the primary's own - is left as it is (ok false), and so is
// every name without the column.
func (dao *Dao) ReplaceUserAddressIdentity(accountID, primary, domain, owner, secret string) (ok bool, err error) {
	err = dao.devicePrimary.runPart(cDevicesPrimary, func() error {
		return dao.withSecret(secret, func(cols []string, vals []any) error {
			args := append(append([]any{owner}, vals...), domain, accountID, primary)
			res, err := dao.db.Exec("update `devices` set `owner_uuid` = ?, "+setColumns(cols)+
				" where `domain` = ? and `account_id` = ? and `primary_domain` = ?", args...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			ok = n > 0
			return err
		})
	}, func() error {
		ok = false
		return nil
	})
	return ok, err
}

// NameHeldForPrimary reports whether domain was released less than
// cNameHold ago by accountID as a user's address of primary: the one held
// name primary takes back over the websocket. False without the column.
func (dao *Dao) NameHeldForPrimary(domain, accountID, primary string) (held bool, err error) {
	err = dao.releasedPrimary.runPart(cReleasedPrimary, func() error {
		var n int
		err := dao.db.QueryRow(
			"select count(*) from `released_domains` where `domain` = ? and `released_at` > ? "+
				"and `account_id` = ? and `primary_domain` = ?",
			domain, time.Now().Add(-cNameHold), accountID, primary,
		).Scan(&n)
		held = n > 0
		return err
	}, func() error {
		held = false
		return nil
	})
	return held, err
}

// primaryOf is the primary domain is a user's address of, "" for none -
// or when it can't be read, which leaves the hold of a released address
// without it: the user added again then asks the owner for the name, as
// for any other held name.
func (dao *Dao) primaryOf(domain string) string {
	var primary sql.NullString
	err := dao.devicePrimary.runPart(cDevicesPrimary, func() error {
		err := dao.db.QueryRow("select `primary_domain` from `devices` where `domain` = ?", domain).Scan(&primary)
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}, func() error { return nil })
	if err != nil {
		log.Error("could not read whose user's address", domain, "is:", err)
		return ""
	}
	return primary.String
}

// forgetPrimary drops domain, a name just released, as the primary of any
// user's address and of any held one. Best effort, logged.
func (dao *Dao) forgetPrimary(domain string) {
	if err := dao.devicePrimary.runPart(cDevicesPrimary, func() error {
		_, err := dao.db.Exec("update `devices` set `primary_domain` = null where `primary_domain` = ?", domain)
		return err
	}, func() error { return nil }); err != nil {
		log.Error("could not forget", domain, "as its users' device:", err)
	}
	if err := dao.releasedPrimary.runPart(cReleasedPrimary, func() error {
		_, err := dao.db.Exec("update `released_domains` set `primary_domain` = null where `primary_domain` = ?", domain)
		return err
	}, func() error { return nil }); err != nil {
		log.Error("could not forget", domain, "as its held users' device:", err)
	}
}
