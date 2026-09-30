// SPDX-License-Identifier: AGPL-3.0-or-later

package profile

import (
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #171: the owner's profile is edited while other connections read
// it. Run with -race.
func TestProfileReadWhileSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 50; i++ {
		mock.ExpectExec("update `profile`").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	pr := InitFromPb(dao.NewWithDB(db), &pb.Profile{Name: "a", Domain: "x"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			pr.SetProfile("name", []byte{byte(i)}, "text")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_, _, _ = pr.Snapshot()
			_ = pr.Name() + pr.Domain() + pr.Text()
			_ = pr.Image()
		}
	}()
	wg.Wait()
}
