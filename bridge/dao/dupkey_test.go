// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestIsDuplicateKey(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ER_DUP_ENTRY", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'x' for key 'domain'"}, true},
		{"wrapped ER_DUP_ENTRY", fmt.Errorf("inserting: %w", &mysql.MySQLError{Number: 1062}), true},
		{"another MySQL error", &mysql.MySQLError{Number: 1040, Message: "Too many connections"}, false},
		{"a lost connection", driver.ErrBadConn, false},
		{"text only", errors.New("Error 1062: Duplicate entry 'x' for key 'domain'"), true},
	} {
		if got := IsDuplicateKey(c.err); got != c.want {
			t.Errorf("%s: IsDuplicateKey() = %v, want %v", c.name, got, c.want)
		}
	}
}
