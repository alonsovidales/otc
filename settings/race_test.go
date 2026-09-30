// SPDX-License-Identifier: AGPL-3.0-or-later

package settings

import (
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
)

// Issue #171: the identity changes (bridge switch-on, Regenerate secret)
// while the bridge dial and push relay read it. Run with -race.
func TestSettingsReadWhileSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 50; i++ {
		mock.ExpectExec("update `settings`").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	st := &Settings{dao: dao.NewWithDB(db), domain: "a.off-the.cloud"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			st.SetBridgeSecret("s")
			st.SetIdentity("u", "b.off-the.cloud", "t")
			st.SetFaceRecognitionEnabled(i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			u, d, s := st.Identity()
			_ = u + d + s + st.Domain() + st.BridgeSecret() + st.DeviceUuid()
			_ = st.FaceRecognitionEnabled()
		}
	}()
	wg.Wait()
}
