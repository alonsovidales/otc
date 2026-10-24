// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
)

// ?days= is 30 when missing or not a whole number, and held within 7 to
// 365 otherwise.
func TestGrowthDays(t *testing.T) {
	for q, want := range map[string]int{
		"":                           30,
		"?days=":                     30,
		"?days=abc":                  30,
		"?days=7.5":                  30,
		"?days=7":                    7,
		"?days=30":                   30,
		"?days=90":                   90,
		"?days=365":                  365,
		"?days=6":                    7,
		"?days=0":                    7,
		"?days=-30":                  7,
		"?days=366":                  365,
		"?days=99999999999999999999": 30, // not an int
	} {
		if got := growthDays(httptest.NewRequest(http.MethodGet, "/admin/api/growth"+q, nil)); got != want {
			t.Errorf("%q: %d, want %d", q, got, want)
		}
	}
}

// expectGrowth expects dao.Growth's queries for a period starting on
// first, answering with no rows.
func expectGrowth(mock sqlmock.Sqlmock, first string) {
	mock.ExpectQuery("select count\\(\\*\\), coalesce\\(sum\\(`last_client_at`").
		WillReturnRows(sqlmock.NewRows([]string{"n", "a1", "a7", "a30"}).AddRow(4, 1, 2, 3))
	mock.ExpectQuery("from `devices` where `created` >= \\? group by `monday`").
		WillReturnRows(sqlmock.NewRows([]string{"monday", "n"}))
	mock.ExpectQuery("from `site_visits` where `day` >= \\? group by `day`").WithArgs(first).
		WillReturnRows(sqlmock.NewRows([]string{"day", "n"}))
	mock.ExpectQuery("group by `page`").WithArgs(first, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"page", "n"}))
	mock.ExpectQuery("group by `source`").WithArgs(first, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"source", "n"}))
	mock.ExpectQuery("select min\\(`day`\\), max\\(`day`\\) from `download_counts`").
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(nil, nil))
}

// GET /admin/api/growth answers the period asked for, held within its
// bounds, as JSON with the devices, visits and downloads.
func TestGrowthEndpoint(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := Init(dao.NewWithDB(db), []byte("session-secret"))
	now := time.Now().UTC()
	expectGrowth(mock, now.AddDate(0, 0, -364).Format(time.DateOnly))

	w := httptest.NewRecorder()
	a.Growth(w, httptest.NewRequest(http.MethodGet, "/admin/api/growth?days=1000", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, %s", w.Code, w.Body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Days    int `json:"days"`
		Devices struct {
			Names      int               `json:"names"`
			Active7d   int               `json:"active_7d"`
			NewPerWeek []json.RawMessage `json:"new_per_week"`
		} `json:"devices"`
		Visits struct {
			PerDay  []json.RawMessage `json:"per_day"`
			Pages   []json.RawMessage `json:"pages"`
			Sources []json.RawMessage `json:"sources"`
		} `json:"visits"`
		Downloads struct {
			Day      string            `json:"day"`
			Assets   []json.RawMessage `json:"assets"`
			Releases []json.RawMessage `json:"releases"`
		} `json:"downloads"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Days != 365 || got.Devices.Names != 4 || got.Devices.Active7d != 2 || len(got.Visits.PerDay) != 365 || len(got.Devices.NewPerWeek) < 53 {
		t.Errorf("%s", w.Body)
	}
	// Empty lists, never null, for the page.
	if got.Visits.Pages == nil || got.Visits.Sources == nil || got.Downloads.Assets == nil || got.Downloads.Releases == nil || got.Downloads.Day != "" {
		t.Errorf("%s", w.Body)
	}
}

// A database error is a 500; without an admin session nothing is read.
func TestGrowthEndpointFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := Init(dao.NewWithDB(db), []byte("session-secret"))

	mock.ExpectQuery("select count\\(\\*\\)").WillReturnError(errors.New("primary away"))
	w := httptest.NewRecorder()
	a.Growth(w, httptest.NewRequest(http.MethodGet, "/admin/api/growth", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("database error: status %d", w.Code)
	}

	w = httptest.NewRecorder()
	a.RequireAuth(a.Growth)(w, httptest.NewRequest(http.MethodGet, "/admin/api/growth", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no session: status %d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
