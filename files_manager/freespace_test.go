// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/diskspace"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// withDisk makes every free-space check see a disk of total bytes with
// avail of them left to write, for the rest of the test.
func withDisk(t *testing.T, total, avail uint64) {
	t.Helper()
	prev := diskUsageOf
	diskUsageOf = func(string) (diskspace.Usage, error) {
		return diskspace.Usage{Total: total, Used: total - avail, Avail: avail}, nil
	}
	t.Cleanup(func() { diskUsageOf = prev })
}

// The reserve: 1 GiB on a large disk, 2% of a small one, never under
// 64 MiB.
func TestFreeSpaceReserve(t *testing.T) {
	for _, c := range []struct {
		total uint64
		want  int64
	}{
		{4_000_000_000_000, 1 << 30},
		{32_000_000_000, 640_000_000},
		{2_000_000_000, 64 << 20},
	} {
		if got := freeSpaceReserve(c.total); got != c.want {
			t.Errorf("a %d-byte disk keeps %d free, want %d", c.total, got, c.want)
		}
	}
}

// A write that would leave less than the reserve is refused, saying how
// much it needs and how much can still be written - storage_full for the
// apps - and one that fits goes ahead.
func TestCheckFreeSpaceRefusesWhatWouldFillTheDisk(t *testing.T) {
	const total = 4_000_000_000_000
	withDisk(t, total, 1<<30+500_000_000) // 500 MB over the reserve
	if err := checkFreeSpace("/storage", 400_000_000); err != nil {
		t.Fatalf("a write that fits was refused: %v", err)
	}
	err := checkFreeSpace("/storage", 600_000_000)
	if !errors.Is(err, ErrStorageFull) || !IsStorageFull(err) {
		t.Fatalf("got %v, want a refusal for want of space", err)
	}
	if want := "not enough space on the device: this needs 600 MB, and only 500 MB is left"; err.Error() != want {
		t.Errorf("English %q, want %q", err.Error(), want)
	}
	if es := devtext.Text("es", err); !strings.Contains(es, "600") || es == err.Error() {
		t.Errorf("Spanish %q", es)
	}
	// What can't be read doesn't stop a write: it fails by itself if full.
	diskUsageOf = func(string) (diskspace.Usage, error) { return diskspace.Usage{}, errors.New("statfs: no") }
	if err := checkFreeSpace("/storage", 1<<40); err != nil {
		t.Errorf("an unknown disk refused a write: %v", err)
	}
}

// A write the disk took and then couldn't finish (it filled meanwhile) is
// storage_full too.
func TestENOSPCIsStorageFull(t *testing.T) {
	err := &fs.PathError{Op: "write", Path: "/storage/.blob-1", Err: syscall.ENOSPC}
	if !IsStorageFull(err) || IsStorageFull(errors.New("EOF")) {
		t.Fatal("ENOSPC isn't storage_full, or something else is")
	}
}

// A chunked upload needs room for all of it and for what the uploads
// already under way still have to write: a sync starts several at once.
func TestBeginUploadCountsTheUploadsUnderWay(t *testing.T) {
	_, ses := galleryTestEnv(t)
	owner := new(int)
	t.Cleanup(func() { (&Manager{}).AbortUploadsOf(owner) })
	withDisk(t, 4_000_000_000_000, 1<<30+10<<20) // 10 MiB over the reserve
	mg := &Manager{}
	if _, err := mg.BeginUpload(ses, "/a.mov", 6<<20, false, nil, nil, "", owner); err != nil {
		t.Fatalf("the first upload was refused: %v", err)
	}
	if _, err := mg.BeginUpload(ses, "/b.mov", 6<<20, false, nil, nil, "", owner); !IsStorageFull(err) {
		t.Fatalf("two uploads that don't fit together: got %v", err)
	}
	if _, err := mg.BeginUpload(ses, "/c.mov", 1<<20, false, nil, nil, "", owner); err != nil {
		t.Fatalf("a small one that fits with the first was refused: %v", err)
	}
}

// An upload in one message is refused before anything is written.
func TestUploadFileRefusedWhenTheDiskIsFull(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	withDisk(t, 4_000_000_000_000, 1<<30) // exactly the reserve
	before, _ := os.ReadDir(storage)
	_, err := (&Manager{}).UploadFile(ses, "/full/a.jpg", []byte("\xff\xd8\xff\xe0 too much for a full disk"), false, nil, nil, "")
	if !IsStorageFull(err) {
		t.Fatalf("got %v, want storage_full", err)
	}
	if after, _ := os.ReadDir(storage); len(after) != len(before) {
		t.Fatalf("something was written: %d entries, were %d", len(after), len(before))
	}
}

// fileRows is a mocked GetFileByPath answer for f.
func fileRows(f *pb.File) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
		AddRow(f.Hash, f.Mime, now, now, f.Path, dao.FileSize(f))
}

// A share link's archive and a gallery's copies are refused before they
// start when the disk can't hold them: no archive is left half written,
// no job started.
func TestShareLinkAndGalleryRefusedWhenTheDiskIsFull(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	photo := libraryFile(t, ses, "full.jpg", "image/jpeg", []byte("JPEG bytes"))
	dao.SetFileSize(photo, 2<<30)
	withDisk(t, 4_000_000_000_000, 1<<30+1<<30) // 1 GiB over the reserve
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour})

	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = ?").
		WithArgs(photo.Path).WillReturnRows(fileRows(photo))
	before, _ := filepath.Glob(filepath.Join(storage, "*-*-*-*-*"))
	if _, err := mg.GetSharedLink(ses, []string{photo.Path}, "kim.off-the.cloud"); !IsStorageFull(err) {
		t.Fatalf("share link: got %v, want storage_full", err)
	}
	if after, _ := filepath.Glob(filepath.Join(storage, "*-*-*-*-*")); len(after) != len(before) {
		t.Fatalf("an archive was written: %v", after)
	}

	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = ?").
		WithArgs(photo.Path).WillReturnRows(fileRows(photo))
	if _, err := mg.StartSharedGallery(ses, &pb.SharedGallerySource{Paths: []string{photo.Path}}, "", 24, "kim.off-the.cloud", false); !IsStorageFull(err) {
		t.Fatalf("gallery: got %v, want storage_full", err)
	}
	// At low resolution only small copies are made: that fits.
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = ?").
		WithArgs(photo.Path).WillReturnRows(fileRows(photo))
	mock.ExpectExec("insert into `shared_links`").WillReturnResult(sqlmock.NewResult(1, 1))
	job, err := mg.StartSharedGallery(ses, &pb.SharedGallerySource{Paths: []string{photo.Path}}, "", 24, "kim.off-the.cloud", true)
	if err != nil {
		t.Fatalf("a low-resolution gallery that fits was refused: %v", err)
	}
	waitGalleryJob(t, job.JobId)
}

// waitGalleryJob waits for the gallery job id to finish.
func waitGalleryJob(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := SharedGalleryJobState(id, ""); ok && j.Finished {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the gallery job never finished")
}
