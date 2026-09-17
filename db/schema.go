// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db embeds db.sql so the running otc binary can provision a brand
// new per-user database at runtime (issue #82) without depending on the
// source tree (/opt/otc-src) being present on disk - a curl|bash-installed
// device doesn't keep it around after scripts/install.sh finishes.
package db

import (
	_ "embed"
	"strings"
)

//go:embed db.sql
var fullFile string

// Schema is db.sql with its header stripped (the CREATE USER/drop
// database/create database/GRANT/use otc lines meant for the very first,
// hand-run bootstrap) - just the CREATE TABLE statements, safe to execute
// against any already-created, already-selected empty database. Mirrors
// the `tail -n +10 db/db.sql` convention scripts/install.sh and
// Makefile.pi's db-schema target already use for the exact same reason.
var Schema = func() string {
	lines := strings.SplitN(fullFile, "\n", 10)
	if len(lines) < 10 {
		return fullFile
	}
	return lines[9]
}()
