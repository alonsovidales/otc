// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Issue #182 (and #176): an account can be deleted by its owner, a device
// can give its name back, and an account's data can be exported.

// DeleteAccount deletes accountID and everything kept for it: its
// domains (each held for 30 days like any release, so no one else can
// take a name its owner's friends still know), their push registrations
// and traffic counts, its sign-in links, setup tokens and app sign-in
// codes, and the account row itself. It returns the domains it released.
// auth_events (security log) are left to their 90-day retention.
func (dao *Dao) DeleteAccount(accountID string) (domains []string, err error) {
	rows, err := dao.db.Query("select `domain` from `devices` where `account_id` = ?", accountID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		domains = append(domains, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		"delete from `devices` where `account_id` = ?",
		"delete from `account_logins` where `account_id` = ?",
		"delete from `account_tokens` where `account_id` = ?",
		"delete from `app_signin_codes` where `account_id` = ?",
		"delete from `accounts` where `id` = ?",
	} {
		if _, err := tx.Exec(q, accountID); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	for _, d := range domains {
		if err := dao.recordRelease(d, accountID); err != nil {
			log.Error("could not hold released name", d, ":", err)
		}
		dao.forgetDomain(d)
	}
	return domains, nil
}

// ReleaseDeviceDomain deletes domain when owner and secret are the
// device's own (the device leaving the bridge). ok is false when they
// don't match. A domain already gone (its account deleted) is ok: there
// is nothing left to release.
func (dao *Dao) ReleaseDeviceDomain(owner, domain, secret string) (ok bool, err error) {
	defined, valid, err := dao.IsValidDevice(owner, domain, secret)
	if err == sql.ErrNoRows || !defined {
		return true, nil
	}
	if err != nil || !valid {
		return false, err
	}
	var accountID sql.NullString
	if err := dao.db.QueryRow("select `account_id` from `devices` where `domain` = ?", domain).Scan(&accountID); err != nil {
		return false, err
	}
	res, err := dao.db.Exec("delete from `devices` where `domain` = ? and `owner_uuid` = ? and `secret` = ?", domain, owner, secret)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := dao.recordRelease(domain, accountID.String); err != nil {
		log.Error("could not hold released name", domain, ":", err)
	}
	dao.forgetDomain(domain)
	return true, nil
}

// AccountExport is everything the bridge keeps for an account, as its
// owner downloads it (GDPR Art. 15 and 20).
type AccountExport struct {
	Account struct {
		ID        string    `json:"id"`
		Email     string    `json:"email"`
		Name      string    `json:"name"`
		Surname   string    `json:"surname"`
		Country   string    `json:"country"`
		Created   time.Time `json:"created"`
		LastSeen  time.Time `json:"last_seen"`
		FreeUntil time.Time `json:"free_until"`
		Password  bool      `json:"has_password"`
	} `json:"account"`
	SignIns []string              `json:"sign_in_providers"`
	Domains []AccountExportDomain `json:"domains"`
}

type AccountExportDomain struct {
	Domain       string     `json:"domain"`
	Created      *time.Time `json:"created,omitempty"`
	Disabled     bool       `json:"disabled"`
	LastClientAt *time.Time `json:"last_client_at,omitempty"`
	PushTokens   int        `json:"push_tokens"`
	// Relayed traffic over the last 90 days (requests and bytes), the
	// counts the admin panel shows.
	Requests int64 `json:"requests_90d"`
	BytesIn  int64 `json:"bytes_in_90d"`
	BytesOut int64 `json:"bytes_out_90d"`
}

// ExportAccount gathers AccountExport for accountID.
func (dao *Dao) ExportAccount(accountID string) (*AccountExport, error) {
	acc, err := dao.GetAccount(accountID)
	if err != nil || acc == nil {
		return nil, err
	}
	out := &AccountExport{}
	a := &out.Account
	a.ID, a.Email, a.Name, a.Surname, a.Country = acc.ID, acc.Email, acc.Name, acc.Surname, acc.Country
	a.Created, a.LastSeen, a.FreeUntil, a.Password = acc.Created, acc.LastSeen, acc.FreeUntil, acc.PasswordHash != ""

	rows, err := dao.db.Query("select `provider` from `account_logins` where `account_id` = ?", accountID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			out.SignIns = append(out.SignIns, p)
		}
	}
	rows.Close()

	rows, err = dao.db.Query("select `domain`, `created`, `disabled`, `last_client_at` from `devices` where `account_id` = ?", accountID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d AccountExportDomain
		var created, last sql.NullTime
		if err := rows.Scan(&d.Domain, &created, &d.Disabled, &last); err != nil {
			rows.Close()
			return nil, err
		}
		if created.Valid {
			d.Created = &created.Time
		}
		if last.Valid {
			d.LastClientAt = &last.Time
		}
		out.Domains = append(out.Domains, d)
	}
	rows.Close()

	for i := range out.Domains {
		d := &out.Domains[i]
		var apns, fcm, web int
		_ = dao.db.QueryRow("select count(*) from `push_apns_tokens` where `domain` = ?", d.Domain).Scan(&apns)
		_ = dao.db.QueryRow("select count(*) from `push_fcm_tokens` where `domain` = ?", d.Domain).Scan(&fcm)
		_ = dao.db.QueryRow("select count(*) from `push_web_subs` where `domain` = ?", d.Domain).Scan(&web)
		d.PushTokens = apns + fcm + web
		_ = dao.db.QueryRow("select coalesce(sum(`requests`),0), coalesce(sum(`bytes_in`),0), coalesce(sum(`bytes_out`),0) from `device_metrics` where `domain` = ?",
			d.Domain).Scan(&d.Requests, &d.BytesIn, &d.BytesOut)
	}
	return out, nil
}
