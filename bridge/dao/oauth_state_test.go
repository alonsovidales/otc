// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Issue #164: a state is used once - when another request deleted it first
// (0 rows), this one doesn't get it.
func TestConsumeOAuthStateOnlyOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("select `return_url`, `created` from `oauth_states` where `state` = \\? for update").
		WillReturnRows(sqlmock.NewRows([]string{"return_url", "created"}).AddRow("/account", time.Now()))
	mock.ExpectExec("delete from `oauth_states`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if _, found, err := NewWithDB(db).ConsumeOAuthState("s"); err != nil || found {
		t.Fatalf("a state someone else consumed was accepted: %v %v", found, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("for update").
		WillReturnRows(sqlmock.NewRows([]string{"return_url", "created"}).AddRow("/account", time.Now()))
	mock.ExpectExec("delete from `oauth_states`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if u, found, err := NewWithDB(db).ConsumeOAuthState("s"); err != nil || !found || u != "/account" {
		t.Fatalf("a fresh state: %q %v %v", u, found, err)
	}
}
