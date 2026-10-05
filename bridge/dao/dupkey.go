// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// IsDuplicateKey is whether err is MySQL refusing an insert because the
// key already exists (ER_DUP_ENTRY): a lost race for a name, not an
// outage. The message check covers errors that only carry the text.
func IsDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}
