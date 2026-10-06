// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/dao"
)

// Issue #186: a folder inside another upload-only folder can't be unlocked
// on its own - the outer one still covers it. That is refused, naming the
// nearest flagged folder above it, and nothing is written; clearing a
// flagged folder of its own, or flagging any folder, is as before.
func TestSetUploadOnlyRefusesToUnlockInsideALockedFolder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	locked := func(paths ...string) {
		rows := sqlmock.NewRows([]string{"path"})
		for _, p := range paths {
			rows.AddRow(p)
		}
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(rows)
	}

	locked("/Photos/")
	err = mg.SetUploadOnly("/Photos/Trip", false)
	var lp *LockedByParentError
	if !errors.As(err, &lp) || lp.Parent != "/Photos/" {
		t.Fatalf("unlocking inside a locked folder: %v, want LockedByParentError for /Photos/", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "Trip") || !strings.Contains(msg, "unlock /Photos") {
		t.Errorf("message %q doesn't say which folder to unlock", msg)
	}

	if lp.OwnFlag {
		t.Error("Trip has no flag of its own")
	}

	// With its own flag too, still covered: refused, its flag kept, and
	// told to unlock the outer folder first and then this one.
	locked("/Photos/", "/Photos/Trip/")
	if err := mg.SetUploadOnly("/Photos/Trip/", false); !errors.As(err, &lp) || !lp.OwnFlag ||
		!strings.Contains(err.Error(), "unlock /Photos first, then Trip") {
		t.Errorf("unlocking a flagged folder inside a locked one: %v, want refused with both steps", err)
	}

	// The nearest locked folder above is the one named.
	locked("/Photos/", "/Photos/Trip/")
	if err := mg.SetUploadOnly("/Photos/Trip/Day1", false); !errors.As(err, &lp) || lp.Parent != "/Photos/Trip/" {
		t.Errorf("nested: %v, want /Photos/Trip/ named", err)
	}

	// "/Photos/" doesn't cover "/Photoshop/".
	locked("/Photos/", "/Photoshop/")
	mock.ExpectExec("delete from `upload_only_folders` where `path` = \\?").WithArgs("/Photoshop/").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetUploadOnly("/Photoshop", false); err != nil {
		t.Errorf("unlocking a folder of its own: %v", err)
	}

	// Locking needs no check.
	mock.ExpectExec("insert ignore into `upload_only_folders`").WithArgs("/Photos/Trip/").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetUploadOnly("/Photos/Trip", true); err != nil {
		t.Errorf("locking: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}
