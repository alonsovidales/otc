// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Places: an area is a join after the collection's, so its bounds follow
// the joins' args and come before the WHERE's; one across the
// antimeridian keeps the longitudes on either side of it; and the
// images-only rule of plain browsing still applies with an area alone.
func TestSearchMediaClausesWithAnArea(t *testing.T) {
	before := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	fiji := &MediaArea{South: -21, West: 177, North: -12, East: -178}
	from, where, _, _, args := searchMediaClauses("/Photos/", []string{"beach"}, []string{"p1"}, "g1", true, &before, []string{"/Photos/Private/"}, fiji)
	want := []any{"beach", "p1", "g1", -21.0, -12.0, 177.0, -178.0,
		"/Photos/", "/Photos/", "/Photos/%", "/Photos/%/%",
		"/Photos/Private/", "/Photos/Private/", "/Photos/Private/%",
		before}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args\n%v\nwant\n%v", args, want)
	}
	if !strings.HasSuffix(from, " join `photo_locations` as `pl` on `pl`.`hash` = `f`.`hash` and `pl`.`lat` between ? and ? and (`pl`.`lon` >= ? or `pl`.`lon` <= ?)") {
		t.Errorf("from %s", from)
	}
	if strings.Count(from, "?")+strings.Count(where, "?") != len(want) {
		t.Errorf("%d placeholders for %d args", strings.Count(from, "?")+strings.Count(where, "?"), len(want))
	}
	from, where, _, _, args = searchMediaClauses("", nil, nil, "", true, nil, nil, &MediaArea{South: 1, West: 2, North: 3, East: 4})
	if !strings.HasSuffix(from, "`pl`.`lon` between ? and ?") || where != " where `f`.`mime` like 'image%'" || !reflect.DeepEqual(args, []any{1.0, 3.0, 2.0, 4.0}) {
		t.Errorf("area alone: %s %s %v", from, where, args)
	}
}
