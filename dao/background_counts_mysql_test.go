// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// Each background pass's count is its listing's rows, once by content
// (Status.background_tasks), against a real MySQL/MariaDB
// (OTC_TEST_MYSQL_DSN, as search_files_mysql_test.go).
func TestBackgroundCountsMySQL(t *testing.T) {
	// Smart search: photos only, outside the kept-out folders, without a
	// vector by this model (embeddingsTestDB's rows: h1 twice, h2, h5;
	// h3 a video, h4 only kept out).
	d := embeddingsTestDB(t)
	if err := d.SetPhotoEmbedding("h1", "m", bytes.Repeat([]byte{7}, 801)); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPhotoEmbedding("h5", "old", []byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		excluded []string
		want     int64
	}{{[]string{"/Kept/"}, 2}, {nil, 3}} {
		files, err := d.PhotosToEmbed("", 100, "m", c.excluded)
		if err != nil {
			t.Fatal(err)
		}
		n, err := d.CountPhotosToEmbed("m", c.excluded)
		if err != nil || n != c.want || int64(len(files)) != n {
			t.Errorf("to describe, %v kept out: %d (%v), listed %d, want %d", c.excluded, n, err, len(files), c.want)
		}
	}
	// Every photo and video after a hash, once each.
	if n, err := d.CountMediaFilesAfter(""); err != nil || n != 5 {
		t.Errorf("media: %d %v, want 5", n, err)
	}
	if n, err := d.CountMediaFilesAfter("h2"); err != nil || n != 3 {
		t.Errorf("media after h2: %d %v, want 3", n, err)
	}

	// On this day: as PhotosToScore, with and without the model.
	m := memoriesTestDB(t)
	day := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	insertMediaRows(t, m.db, "files",
		mediaRow{ph(1), "image/jpeg", "/P/a.jpg", day},
		mediaRow{ph(1), "image/jpeg", "/Q/a.jpg", day}, // a second copy
		mediaRow{ph(2), "image/jpeg", "/P/b.jpg", day},
		mediaRow{ph(3), "image/jpeg", "/P/c.jpg", day},
		mediaRow{ph(4), "image/jpeg", "/Private/d.jpg", day},
		mediaRow{ph(5), "video/mp4", "/P/e.mp4", day},
	)
	if err := m.SetPhotoQuality(ph(1), PhotoQuality{Aesthetic: f32(5), Sharpness: 1}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPhotoQuality(ph(2), PhotoQuality{Sharpness: 1}); err != nil {
		t.Fatal(err)
	}
	for _, withModel := range []bool{false, true} {
		files, err := m.PhotosToScore("", 100, withModel, []string{"/Private/"})
		if err != nil {
			t.Fatal(err)
		}
		n, err := m.CountPhotosToScore(withModel, []string{"/Private/"})
		if err != nil || n != int64(len(files)) || n == 0 {
			t.Errorf("to measure (model %v): %d %v, listed %d", withModel, n, err, len(files))
		}
	}

	// Places: photos and videos without a row.
	p := placesTestDB(t)
	insertMediaRows(t, p.db, "files",
		mediaRow{ph(1), "image/jpeg", "/P/a.jpg", day},
		mediaRow{ph(1), "image/jpeg", "/Q/a.jpg", day},
		mediaRow{ph(2), "video/mp4", "/P/b.mp4", day},
		mediaRow{ph(3), "image/jpeg", "/P/c.jpg", day},
		mediaRow{ph(6), "text/plain", "/P/f.txt", day},
	)
	if err := p.SetPhotoLocation(ph(3), 1, 2, true); err != nil {
		t.Fatal(err)
	}
	files, err := p.MediaWithoutLocation("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := p.CountMediaWithoutLocation(); err != nil || n != 2 || int64(len(files)) != n {
		t.Errorf("without a position: %d %v, listed %d, want 2", n, err, len(files))
	}

	// Before their tables: the release's migration hasn't run.
	bare := NewWithDB(searchTestDB(t))
	if _, err := bare.CountPhotosToEmbed("m", nil); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("without photo_embeddings: %v", err)
	}
	if _, err := bare.CountMediaWithoutLocation(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("without photo_locations: %v", err)
	}
}
