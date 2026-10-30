// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Smart search, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): temporary tables shaped as db.sql's.

func embeddingsTestDB(tb testing.TB) *Dao {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	for _, q := range []string{
		"create temporary table `photo_embeddings` (`hash` varchar(64) not null, `model` varchar(64) not null, `vec` varbinary(2048) not null, `made` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `settings` (`domain` varchar(64) not null default '', `smart_search_enabled` tinyint(1) not null default 0) engine=InnoDB",
		"insert into `settings` (`domain`) values ('t')",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	insertMediaRows(tb, db, "files",
		mediaRow{"h1", "image/jpeg", "/Photos/a.jpg", month(1)},
		mediaRow{"h1", "image/jpeg", "/Backup/a.jpg", month(2)}, // a second copy
		mediaRow{"h2", "image/heic", "/Photos/b.heic", month(3)},
		mediaRow{"h3", "video/mp4", "/Photos/c.mp4", month(4)},  // not a photo
		mediaRow{"h4", "image/png", "/Kept/d.png", month(5)},    // only kept out
		mediaRow{"h5", "image/jpeg", "/Photos/e.jpg", month(6)}, // described by another model
	)
	return d
}

func TestPhotoEmbeddingsMySQL(t *testing.T) {
	d := embeddingsTestDB(t)
	if on, err := d.SmartSearchEnabled(); err != nil || on {
		t.Fatalf("off by default: %v %v", on, err)
	}
	vec := bytes.Repeat([]byte{7}, 801) // a sealed SigLIP 2 base vector's size
	if err := d.SetPhotoEmbedding("h1", "m", vec); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPhotoEmbedding("h5", "old", []byte{1}); err != nil {
		t.Fatal(err)
	}
	// Replacing keeps one row.
	if err := d.SetPhotoEmbedding("h1", "m", append(vec[:800:800], 9)); err != nil {
		t.Fatal(err)
	}

	// To describe: photos only, once each, outside the kept-out folders,
	// without a vector by this model - after the hash asked, a page at a
	// time.
	files, err := d.PhotosToEmbed("", 10, "m", []string{"/Kept/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := hashList(files); got != "h2,h5" {
		t.Errorf("to describe: %s, want h2,h5", got)
	}
	if files, _ := d.PhotosToEmbed("h2", 10, "m", []string{"/Kept/"}); hashList(files) != "h5" {
		t.Errorf("after h2: %s", hashList(files))
	}
	if files, _ := d.PhotosToEmbed("", 1, "m", nil); hashList(files) != "h2" {
		t.Errorf("a page of one: %s", hashList(files))
	}

	got := map[string][]byte{}
	if err := d.EachPhotoEmbedding("m", func(h string, v []byte) error { got[h] = v; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got["h1"]) != 801 || got["h1"][800] != 9 {
		t.Errorf("read back %d vector(s)", len(got))
	}
	stop := errors.New("stop")
	if err := d.EachPhotoEmbedding("old", func(string, []byte) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("fn's error: %v", err)
	}

	if err := d.DelPhotoEmbedding("h1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSmartSearchEnabled(true); err != nil {
		t.Fatal(err)
	}
	if on, _ := d.SmartSearchEnabled(); !on {
		t.Error("not on")
	}
	// Off deletes every vector with it.
	if err := d.SetSmartSearchEnabled(false); err != nil {
		t.Fatal(err)
	}
	n := 0
	d.EachPhotoEmbedding("old", func(string, []byte) error { n++; return nil })
	if on, _ := d.SmartSearchEnabled(); on || n != 0 {
		t.Errorf("off: on %v, %d vector(s) left", on, n)
	}
}

// A database before release 151: no table, no column.
func TestPhotoEmbeddingsWithoutTheirTablesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	if _, err := db.Exec("create temporary table `settings` (`domain` varchar(64) not null default '') engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SmartSearchEnabled(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("switch: %v", err)
	}
	if err := d.SetPhotoEmbedding("h", "m", []byte{1}); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("set: %v", err)
	}
	if _, err := d.PhotosToEmbed("", 10, "m", nil); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("to describe: %v", err)
	}
	if err := d.EachPhotoEmbedding("m", func(string, []byte) error { return nil }); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("each: %v", err)
	}
	if err := d.DelPhotoEmbedding("h"); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func hashList(files []*pb.File) string {
	var out []string
	for _, f := range files {
		out = append(out, f.Hash)
	}
	return strings.Join(out, ",")
}
