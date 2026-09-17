// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"

	"github.com/alonsovidales/otc/cfg"
	otcdb "github.com/alonsovidales/otc/db"
	"github.com/alonsovidales/otc/log"
)

// adminDSN opens a connection using the primary instance's own [mysql]
// user/pass/port (the same credentials dao.Init() already uses), but with
// no `db=` component - just enough to run CREATE/DROP DATABASE and
// CREATE/DROP USER against the server itself, not any one schema. This is
// why issue #82's plan calls for granting the app's own MySQL user
// CREATE/DROP/CREATE USER/GRANT OPTION privileges - see db-schema's setup
// SQL and the required one-time migration note for already-provisioned
// devices.
func adminDSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(127.0.0.1:%d)/?parseTime=true&charset=utf8mb4,utf8",
		cfg.GetStr("mysql", "user"),
		cfg.GetStr("mysql", "pass"),
		cfg.GetInt("mysql", "port"),
	)
}

// ProvisionUserDatabase creates a brand new user's database, a dedicated
// MySQL user scoped to just that database, loads the full schema
// (db/schema.go's embedded copy of db.sql, sans its own hand-run-only
// header) into it, and seeds its `settings` row (device_uuid/subdomain/
// bridge_secret) - the same steps install.sh/Makefile.pi's db-schema step
// already does by hand for the very first device, just parameterized per
// user and run from Go at runtime instead. Uses the freshly created
// per-user credentials (not the primary's own admin connection) for the
// settings insert, since the primary's own [mysql] user is deliberately
// NOT granted DML on other schemas - only CREATE/DROP/CREATE USER/GRANT
// OPTION (see adminDSN's doc comment), matching least-privilege: creating
// schemas is all the primary itself ever needs to do directly.
func ProvisionUserDatabase(dbName, dbUser, dbPass, deviceUuid, subdomain, bridgeSecret string) (err error) {
	admin, err := sql.Open("mysql", adminDSN())
	if err != nil {
		return err
	}
	defer admin.Close()

	// Backtick-quoted identifiers can't be parameterized (?) the normal
	// way - dbName/dbUser are always our own generated `otc_<uuid-no-
	// dashes>` strings (see supervisor.go), never user-supplied, so this
	// is safe the same way files_manager's own path-building already is.
	stmts := []string{
		fmt.Sprintf("CREATE DATABASE `%s`", dbName),
		fmt.Sprintf("CREATE USER '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, dbPass),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", dbName, dbUser),
		"FLUSH PRIVILEGES",
	}
	for _, stmt := range stmts {
		if _, err = admin.Exec(stmt); err != nil {
			return fmt.Errorf("provisioning %s: %w", dbName, err)
		}
	}

	userDB, err := sql.Open("mysql", fmt.Sprintf(
		"%s:%s@tcp(127.0.0.1:%d)/%s?parseTime=true&charset=utf8mb4,utf8&multiStatements=true",
		dbUser, dbPass, cfg.GetInt("mysql", "port"), dbName,
	))
	if err != nil {
		return err
	}
	defer userDB.Close()

	if _, err = userDB.Exec(otcdb.Schema); err != nil {
		return fmt.Errorf("loading schema into %s: %w", dbName, err)
	}

	if _, err = userDB.Exec(
		"insert into `settings` (`device_uuid`, `subdomain`, `bridge_secret`) select ?, ?, ? where not exists (select 1 from `settings`)",
		deviceUuid, subdomain, bridgeSecret,
	); err != nil {
		return fmt.Errorf("seeding settings for %s: %w", dbName, err)
	}
	return nil
}

// DropUserDatabase is ProvisionUserDatabase's inverse - called after the
// supervisor has confirmed the user's own process has actually exited
// (see supervisor.Stop), never before, so nothing is still holding
// connections open against the schema being dropped.
func DropUserDatabase(dbName, dbUser string) (err error) {
	admin, err := sql.Open("mysql", adminDSN())
	if err != nil {
		return err
	}
	defer admin.Close()

	if _, err = admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)); err != nil {
		return fmt.Errorf("dropping database %s: %w", dbName, err)
	}
	if _, err = admin.Exec(fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost'", dbUser)); err != nil {
		log.Error("dropping MySQL user for", dbName, ":", err)
	}
	return nil
}

// UserStorageUsageMB opens a short-lived connection - as the PRIMARY
// instance's own [mysql] user, which needs a one-time `GRANT SELECT ON
// *.*` alongside the CREATE/DROP/CREATE USER/GRANT OPTION grant
// provisioning itself needs (see the required migration note wherever
// this is documented) - to a user's own database and sums files.size:
// each user's own files_manager already tracks this per file, so this is
// exact and free of any filesystem walk, and avoids having to separately
// store/manage that user's own dedicated MySQL password anywhere just for
// this one read-only query.
func UserStorageUsageMB(dbName string) (mb float64, err error) {
	userDB, err := sql.Open("mysql", fmt.Sprintf(
		"%s:%s@tcp(127.0.0.1:%d)/%s?parseTime=true&charset=utf8mb4,utf8",
		cfg.GetStr("mysql", "user"), cfg.GetStr("mysql", "pass"), cfg.GetInt("mysql", "port"), dbName,
	))
	if err != nil {
		return 0, err
	}
	defer userDB.Close()

	var bytes int64
	if err = userDB.QueryRow("select coalesce(sum(`size`), 0) from `files`").Scan(&bytes); err != nil {
		return 0, err
	}
	return float64(bytes) / (1024 * 1024), nil
}
