// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"database/sql"
	"strings"
)

// cOwnersChunk bounds the placeholders in one DeviceOwners query.
const cOwnersChunk = 500

// DeviceOwners is the owner uuid of each of domains that is registered,
// keyed by the domain as stored. Any error is returned whole, never a
// partial answer: a caller closes connections for what is missing.
func (dao *Dao) DeviceOwners(ctx context.Context, domains []string) (map[string]string, error) {
	out := make(map[string]string, len(domains))
	for len(domains) > 0 {
		n := min(len(domains), cOwnersChunk)
		args := make([]any, n)
		for i, d := range domains[:n] {
			args[i] = d
		}
		domains = domains[n:]
		rows, err := dao.db.QueryContext(ctx,
			"select `domain`, `owner_uuid` from `devices` where `domain` in (?"+strings.Repeat(", ?", n-1)+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d, owner string
			if err := rows.Scan(&d, &owner); err != nil {
				rows.Close()
				return nil, err
			}
			out[d] = owner
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeviceOwner is domain's owner uuid, looked up as IsValidDevice does
// (the column's collation decides what matches).
func (dao *Dao) DeviceOwner(ctx context.Context, domain string) (owner string, registered bool, err error) {
	err = dao.db.QueryRowContext(ctx, "select `owner_uuid` from `devices` where `domain` = ?", domain).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
}
