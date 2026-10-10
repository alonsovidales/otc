// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
)

// cDuplicatedChildEnv marks the child process
// TestTakenPathAnswersDuplicatedFile runs in.
const cDuplicatedChildEnv = "OTC_WS_DUPLICATED_CHILD"

// An upload or link refused because the path holds other content answers
// error_code duplicated_file - on UploadFile, FinishUpload and LinkFile -
// with the message it always had, which apps released before the code
// match; any other refusal of the same requests carries no code.
func TestTakenPathAnswersDuplicatedFile(t *testing.T) {
	if os.Getenv(cDuplicatedChildEnv) != "1" {
		// In a child process: the files manager reads [otc] storage-path,
		// and a loaded config can't be unloaded (searchPhotosStorage).
		name := "TestTakenPathAnswersDuplicatedFile"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cDuplicatedChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	storage := searchPhotosStorage(t)
	ch, mock := ownerWithLibrary(t)
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	now := time.Now()
	referenced := func(hash string, n int) {
		mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
			WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(n))
	}
	pathTaken := func(path string) {
		mock.ExpectExec("insert into `files`").WillReturnError(&mysql.MySQLError{Number: 1062})
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(path).
			WillReturnRows(sqlmock.NewRows(cols).AddRow(strings.Repeat("e", 64), "text/plain", now, now, path, 5))
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	}
	onDisk := func(hash string) {
		p := filepath.Join(storage, hash)
		if err := os.WriteFile(p, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(p) })
	}
	refused := func(what string, resp *pb.RespEnvelope, code, message string) {
		t.Helper()
		if resp == nil || !resp.Error || resp.ErrorCode != code || resp.ErrorMessage != message || resp.GetRespFile() != nil {
			t.Errorf("%s: got %+v, want error_code %q, message %q", what, resp, code, message)
		}
	}

	// UploadFile, its content on the disk already.
	content := []byte("an upload to a path that holds something else")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	onDisk(hash)
	referenced(hash, 1)
	pathTaken("/docs/a.txt")
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 11, Payload: &pb.ReqEnvelope_ReqUploadFile{
		ReqUploadFile: &pb.UploadFile{Path: "/docs/a.txt", Content: content},
	}})
	refused("UploadFile", resp, "duplicated_file", "error trying to upload file: Duplicated file")

	// The chunked upload: refused at FinishUpload.
	content = []byte("a chunked upload to a path that holds something else")
	sum = sha256.Sum256(content)
	hash = hex.EncodeToString(sum[:])
	t.Cleanup(func() { os.Remove(filepath.Join(storage, hash)) })
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 12, Payload: &pb.ReqEnvelope_ReqBeginUpload{
		ReqBeginUpload: &pb.BeginUpload{Path: "/docs/b.txt", Size: int64(len(content))},
	}})
	id := resp.GetRespUploadStarted().GetUploadId()
	if resp.Error || id == "" {
		t.Fatalf("BeginUpload: %+v", resp)
	}
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 13, Payload: &pb.ReqEnvelope_ReqUploadChunk{
		ReqUploadChunk: &pb.UploadChunk{UploadId: id, Data: content},
	}})
	if resp.Error || resp.GetRespUploadProgress().GetReceived() != int64(len(content)) {
		t.Fatalf("UploadChunk: %+v", resp)
	}
	referenced(hash, 1)
	pathTaken("/docs/b.txt")
	referenced(hash, 1) // still used elsewhere: the committed content stays
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 14, Payload: &pb.ReqEnvelope_ReqFinishUpload{
		ReqFinishUpload: &pb.FinishUpload{UploadId: id, Sha256: hash},
	}})
	refused("FinishUpload", resp, "duplicated_file", "error finishing the upload: Duplicated file")

	// LinkFile.
	hash = strings.Repeat("d", 64)
	onDisk(hash)
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?").WithArgs(hash).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(hash, "text/plain", now, now, "/elsewhere/c.txt", 7))
	pathTaken("/docs/c.txt")
	link := &pb.ReqEnvelope{Id: 15, Payload: &pb.ReqEnvelope_ReqLinkFile{
		ReqLinkFile: &pb.LinkFile{Path: "/docs/c.txt", Hash: hash},
	}}
	resp, _ = ch.processMessage(link)
	refused("LinkFile", resp, "duplicated_file", "error trying to link file: Duplicated file")

	// Other refusals stay as they were, with no code.
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?").WithArgs(hash).
		WillReturnRows(sqlmock.NewRows(cols))
	resp, _ = ch.processMessage(link)
	refused("LinkFile, unknown hash", resp, "", "error trying to link file: no file with that hash on this device")
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 16, Payload: &pb.ReqEnvelope_ReqFinishUpload{
		ReqFinishUpload: &pb.FinishUpload{UploadId: id, Sha256: hash},
	}})
	refused("FinishUpload, finished already", resp, "", "error finishing the upload: unknown or expired upload")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
