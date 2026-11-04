// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/alonsovidales/otc/log"
)

// A device's relay secret and an account's setup codes are kept hashed:
// a read of the database (a backup, the replica's host, a dump) no longer
// hands out what registers as every device or claims names for an account.
//
//   - devices.secret_hash is SecretHash of the device's secret. The secrets
//     are random - 192 bits or more, made by the device, the setup wizard or
//     the bridge - so a fast hash is enough: nobody finds one from its hash.
//   - account_setup_codes keeps a setup code by a keyed hash the accounts
//     package makes (an 8-character code is 40 bits, which a plain hash
//     would not hide), with whether it may move a name to a new device.
//
// The schema moves in two steps, so that both nodes keep working while the
// cluster is deployed one node at a time, and a bridge whose migrations
// haven't run keeps working too:
//
//  1. migrations/013-hashed-secrets.sql adds secret_hash (filled in from
//     secret) and account_setup_codes. With both forms there, this code
//     writes both - the plaintext for a node still on an older release,
//     which reads only that - and checks a device by its plaintext, which
//     is the one an older node keeps current. Before 013 it works as before,
//     on the plaintext alone.
//  2. migrations/014-drop-plaintext-secrets.sql, once every node runs this
//     code, fills secret_hash in once more (what an older node wrote in the
//     meantime), drops devices.secret and account_tokens. From then on only
//     the hashes are written and read.
//
// Which step a database is at is found the way optionalColumn finds a
// column: a query naming what isn't there fails (1054, or 1146 for a
// table), is not tried again for cColumnRecheck, and the same query
// without it runs instead.

// SecretHash is what the bridge keeps of a device's relay secret: its
// SHA-256, in hex.
func SecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// schemaPart is a column (or, with no column, a table) that this binary
// runs with or without while a migration adds or drops it.
type schemaPart struct {
	table, column string
	// gone: missing is where the schema is going (a migration drops it),
	// logged once at Info rather than as something to fix.
	gone      bool
	migration string
}

func (p schemaPart) String() string {
	if p.column == "" {
		return "the table " + p.table
	}
	return "the column " + p.table + "." + p.column
}

// missingIn is whether err is MySQL refusing a statement because p isn't
// there.
func (p schemaPart) missingIn(err error) bool {
	if p.column == "" {
		return isUnknownTable(err, p.table)
	}
	return isUnknownColumn(err, p.column)
}

var (
	cDevicesSecret     = schemaPart{table: "devices", column: "secret", gone: true, migration: "014-drop-plaintext-secrets.sql"}
	cDevicesSecretHash = schemaPart{table: "devices", column: "secret_hash", migration: "013-hashed-secrets.sql"}
	cSetupCodesTable   = schemaPart{table: "account_setup_codes", migration: "013-hashed-secrets.sql"}
	cAccountTokens     = schemaPart{table: "account_tokens", gone: true, migration: "014-drop-plaintext-secrets.sql"}
)

// runPart is optionalColumn.run for p, a column or a table.
func (c *optionalColumn) runPart(p schemaPart, with, without func() error) error {
	if since := c.missingSince.Load(); since == 0 || time.Now().UnixNano()-since >= int64(cColumnRecheck) {
		err := with()
		if !p.missingIn(err) {
			if since != 0 && (err == nil || errors.Is(err, sql.ErrNoRows)) && c.missingSince.CompareAndSwap(since, 0) {
				log.Info("dao:", p.String(), "is there now")
			}
			return err
		}
		if c.missingSince.Swap(time.Now().UnixNano()) == 0 {
			if p.gone {
				log.Info("dao:", p.String(), "is gone (bridge/db/migrations/"+p.migration+"): going on without it")
			} else {
				log.Error("dao:", p.String(), "is missing - run bridge/db/migrations/"+p.migration+"; going on without it")
			}
		}
	}
	return without()
}

// isUnknownTable is whether err is MySQL refusing a statement because
// table doesn't exist (ER_NO_SUCH_TABLE, "Table 'otc.account_tokens'
// doesn't exist").
func isUnknownTable(err error, table string) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1146 && strings.Contains(me.Message, "."+table+"'")
	}
	return strings.Contains(err.Error(), "doesn't exist") && strings.Contains(err.Error(), "."+table+"'")
}

// withSecret runs write with the columns a device's secret goes in now and
// their values: the plaintext and its hash while the schema has both, the
// plaintext alone before migration 013, the hash alone after 014.
func (dao *Dao) withSecret(secret string, write func(cols []string, vals []any) error) error {
	h := SecretHash(secret)
	return dao.deviceSecret.runPart(cDevicesSecret, func() error {
		return dao.deviceSecretHash.runPart(cDevicesSecretHash, func() error {
			return write([]string{"secret", "secret_hash"}, []any{secret, h})
		}, func() error {
			return write([]string{"secret"}, []any{secret})
		})
	}, func() error {
		return write([]string{"secret_hash"}, []any{h})
	})
}

// secretMatches runs query with the condition that matches a device's
// current secret and its arguments: its plaintext while the schema keeps
// one (the form a node of an older release keeps current), its hash once
// migration 014 has dropped the plaintext.
func (dao *Dao) secretMatches(secret string, query func(cond string, args []any) error) error {
	return dao.deviceSecret.runPart(cDevicesSecret, func() error {
		return query("`secret` = ?", []any{secret})
	}, func() error {
		return query("`secret_hash` = ?", []any{SecretHash(secret)})
	})
}

// setColumns is "`a` = ?, `b` = ?" for cols.
func setColumns(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = "`" + c + "` = ?"
	}
	return strings.Join(parts, ", ")
}

// columnList is "`a`, `b`" for cols, and its placeholders "?, ?".
func columnList(cols []string) (names, marks string) {
	n, m := make([]string, len(cols)), make([]string, len(cols))
	for i, c := range cols {
		n[i], m[i] = "`"+c+"`", "?"
	}
	return strings.Join(n, ", "), strings.Join(m, ", ")
}

// insertDevice is the insert of a new device row: cols and vals, then its
// secret in the columns withSecret picks.
func (dao *Dao) insertDevice(cols []string, vals []any, secret string) error {
	return dao.withSecret(secret, func(secretCols []string, secretVals []any) error {
		names, marks := columnList(append(append([]string{}, cols...), secretCols...))
		_, err := dao.db.Exec("insert into `devices` ("+names+") values ("+marks+")", append(append([]any{}, vals...), secretVals...)...)
		return err
	})
}

// ---------------------------------------------------------------------
// Setup codes
// ---------------------------------------------------------------------

// cLegacySetupPurpose is account_tokens' purpose for a setup code, as the
// releases before migration 013 store and look it up.
const cLegacySetupPurpose = "setup"

// ErrNoSetupCodeTable: neither account_setup_codes nor account_tokens is
// there, so a setup code has nowhere to be kept.
var ErrNoSetupCodeTable = errors.New("the database has no table for setup codes (bridge/db/migrations/013-hashed-secrets.sql)")

// SetupCode is a live setup code: the account it was made for, and whether
// it may move one of the account's names to a new device - made right
// after the account's owner proved who they are (their password, a
// sign-in with Google or Apple), not on the strength of a session alone.
type SetupCode struct {
	AccountID      string
	OwnerConfirmed bool
}

// SaveSetupCode keeps a setup code for accountID until now+ttl: by
// codeHash (the accounts package's keyed hash of it) in
// account_setup_codes, and - while the database still has account_tokens,
// which a node of an older release looks codes up in - as it is there too.
// Before migration 013 only the latter, as before.
func (dao *Dao) SaveSetupCode(code, codeHash, accountID string, ownerConfirmed bool, now time.Time, ttl time.Duration) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stored := false
	if err := dao.setupCodes.runPart(cSetupCodesTable, func() error {
		_, err := tx.Exec("insert into `account_setup_codes` (`code_hash`, `account_id`, `owner_confirmed`, `created`, `expires`) values (?, ?, ?, ?, ?)",
			codeHash, accountID, ownerConfirmed, now.UTC(), now.Add(ttl).UTC())
		stored = stored || err == nil
		return err
	}, func() error { return nil }); err != nil {
		return err
	}
	if err := dao.legacySetupTokens.runPart(cAccountTokens, func() error {
		_, err := tx.Exec("insert into `account_tokens` (`token`, `account_id`, `purpose`, `expires`) values (?, ?, ?, ?)",
			code, accountID, cLegacySetupPurpose, now.Add(ttl).UTC())
		stored = stored || err == nil
		return err
	}, func() error { return nil }); err != nil {
		return err
	}
	if !stored {
		return ErrNoSetupCodeTable
	}
	return tx.Commit()
}

// SetupCodeFor is the live setup code codeHash names - or, made by a node
// of an older release, code itself in account_tokens; found is false for an
// unknown or expired one. A code of an older node never moves a name: none
// says how it was made. Before migration 013, when every code is one of
// those, each may, as before.
func (dao *Dao) SetupCodeFor(code, codeHash string, now time.Time) (sc SetupCode, found bool, err error) {
	hashed := true
	err = dao.setupCodes.runPart(cSetupCodesTable, func() error {
		err := dao.db.QueryRow("select `account_id`, `owner_confirmed` from `account_setup_codes` where `code_hash` = ? and `expires` > ?",
			codeHash, now.UTC()).Scan(&sc.AccountID, &sc.OwnerConfirmed)
		if err == sql.ErrNoRows {
			return nil
		}
		found = err == nil
		return err
	}, func() error {
		hashed = false
		return nil
	})
	if err != nil || found {
		return sc, found, err
	}
	err = dao.legacySetupTokens.runPart(cAccountTokens, func() error {
		var id string
		err := dao.db.QueryRow("select `account_id` from `account_tokens` where `token` = ? and `purpose` = ? and `expires` > ?",
			code, cLegacySetupPurpose, now.UTC()).Scan(&id)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		sc, found = SetupCode{AccountID: id, OwnerConfirmed: !hashed}, true
		return nil
	}, func() error { return nil })
	return sc, found, err
}

// pruneSetupCodes drops the expired setup codes, wherever they are kept.
func (dao *Dao) pruneSetupCodes(now time.Time) error {
	if err := dao.legacySetupTokens.runPart(cAccountTokens, func() error {
		_, err := dao.db.Exec("delete from `account_tokens` where `expires` < ?", now.UTC())
		return err
	}, func() error { return nil }); err != nil {
		return err
	}
	return dao.setupCodes.runPart(cSetupCodesTable, func() error {
		_, err := dao.db.Exec("delete from `account_setup_codes` where `expires` < ?", now.UTC())
		return err
	}, func() error { return nil })
}

// deleteSetupCodes drops accountID's setup codes in tx, wherever they are
// kept (DeleteAccount).
func (dao *Dao) deleteSetupCodes(tx *sql.Tx, accountID string) error {
	if err := dao.legacySetupTokens.runPart(cAccountTokens, func() error {
		_, err := tx.Exec("delete from `account_tokens` where `account_id` = ?", accountID)
		return err
	}, func() error { return nil }); err != nil {
		return err
	}
	return dao.setupCodes.runPart(cSetupCodesTable, func() error {
		_, err := tx.Exec("delete from `account_setup_codes` where `account_id` = ?", accountID)
		return err
	}, func() error { return nil })
}
