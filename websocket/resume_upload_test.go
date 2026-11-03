// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// cResumeChildEnv marks the child process TestUploadResumesOnAnotherConnection
// runs in.
const cResumeChildEnv = "OTC_WS_RESUME_CHILD"

// Issue #205, over real sockets: an upload begun with its sha256 on a
// connection that closes goes on, on another connection, from what the
// device holds (UploadStarted.received, the same upload_id); a third
// connection beginning it while the second is still open takes it over,
// and the second's chunks are refused from then on; FinishUpload commits
// the whole content. One begun without a sha256 goes with its connection,
// its temp file too.
func TestUploadResumesOnAnotherConnection(t *testing.T) {
	if os.Getenv(cResumeChildEnv) != "1" {
		// In a child process: the files manager reads [otc] storage-path,
		// and a loaded config can't be unloaded (searchPhotosStorage).
		name := "TestUploadResumesOnAnotherConnection"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cResumeChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	storage := searchPhotosStorage(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mg := &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}
	mg.backfillOnce.Do(func() {}) // no sign-in backfill against the mock
	ses := newTestAuthenticatedSession(t)

	served := make(chan struct{}, 8)
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mg.serveConnection(conn, r, nil)
		conn.Close()
		served <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	gone := func(what string) {
		t.Helper()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the device never saw the connection close", what)
		}
	}

	type client struct {
		c  *gorilla.Conn
		id int32
	}
	call := func(cl *client, env *pb.ReqEnvelope) *pb.RespEnvelope {
		t.Helper()
		cl.id++
		env.Id = cl.id
		b, _ := proto.Marshal(env)
		if err := cl.c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
			t.Fatal(err)
		}
		cl.c.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := cl.c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var resp pb.RespEnvelope
		if err := proto.Unmarshal(data, &resp); err != nil || resp.Id != cl.id {
			t.Fatalf("reply %d: %v (%d)", cl.id, err, resp.Id)
		}
		return &resp
	}
	dial := func() *client {
		t.Helper()
		c, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		token, _, err := session.IssueToken(ses, session.PasswordGeneration())
		if err != nil {
			t.Fatal(err)
		}
		cl := &client{c: c}
		resp := call(cl, &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: token}}})
		if !resp.GetRespAck().GetOk() {
			t.Fatalf("sign-in: %+v", resp)
		}
		return cl
	}

	content := make([]byte, 5<<20+12345)
	rand.Read(content)
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	t.Cleanup(func() { os.Remove(filepath.Join(storage, hash)) })
	size := int64(len(content))
	begin := func(cl *client, sha string) *pb.UploadStarted {
		t.Helper()
		resp := call(cl, &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqBeginUpload{ReqBeginUpload: &pb.BeginUpload{Path: "/Videos/clip.bin", Size: size, Sha256: sha}}})
		started := resp.GetRespUploadStarted()
		if resp.Error || started == nil {
			t.Fatalf("BeginUpload: %+v", resp)
		}
		return started
	}
	chunk := func(cl *client, id string, from, to int) *pb.RespEnvelope {
		t.Helper()
		return call(cl, &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqUploadChunk{ReqUploadChunk: &pb.UploadChunk{UploadId: id, Offset: int64(from), Data: content[from:to]}}})
	}
	sent := func(what string, resp *pb.RespEnvelope, want int) {
		t.Helper()
		if resp.Error || resp.GetRespUploadProgress().GetReceived() != int64(want) {
			t.Fatalf("%s: %+v, want %d received", what, resp, want)
		}
	}

	// A begins, sends a part - a segment and a bit - and drops.
	a := dial()
	started := begin(a, hash)
	id := started.UploadId
	if started.Received != 0 {
		t.Fatalf("a new upload starts at %d", started.Received)
	}
	first := 1<<20 + 4321
	sent("A's part", chunk(a, id, 0, first), first)
	a.c.Close()
	gone("A")

	// B goes on from there.
	b := dial()
	if started = begin(b, hash); started.UploadId != id || started.Received != int64(first) {
		t.Fatalf("B resumed %s at %d, want %s at %d", started.UploadId, started.Received, id, first)
	}
	second := first + 2<<20
	sent("B's part", chunk(b, id, first, second), second)

	// C begins it while B is still open: C drives it now.
	c := dial()
	if started = begin(c, hash); started.UploadId != id || started.Received != int64(second) {
		t.Fatalf("C took over %s at %d, want %s at %d", started.UploadId, started.Received, id, second)
	}
	if resp := chunk(b, id, second, second+10); !resp.Error || resp.ErrorMessage != "error receiving the upload: unknown or expired upload" {
		t.Fatalf("B sent to an upload C drives: %+v", resp)
	}
	sent("C's part", chunk(c, id, second, len(content)), len(content))

	mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
		WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `files`").WillReturnResult(sqlmock.NewResult(1, 1))
	resp := call(c, &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqFinishUpload{ReqFinishUpload: &pb.FinishUpload{UploadId: id, Sha256: hash}}})
	if resp.Error || resp.GetRespFile().GetHash() != hash {
		t.Fatalf("FinishUpload: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if stored, err := blobstore.ReadAll(filepath.Join(storage, hash), ses); err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("stored: %d bytes, %v", len(stored), err)
	}

	// Without a sha256 nothing outlives the connection.
	temps := func() int {
		matches, _ := filepath.Glob(filepath.Join(storage, ".blob-*"))
		return len(matches)
	}
	before := temps()
	d := dial()
	keyless := begin(d, "")
	sent("D's part", chunk(d, keyless.UploadId, 0, 1000), 1000)
	if temps() != before+1 {
		t.Fatalf("%d temp files, want %d", temps(), before+1)
	}
	d.c.Close()
	gone("D")
	if temps() != before {
		t.Errorf("the keyless upload's temp file stayed")
	}
	e := dial()
	if started = begin(e, ""); started.UploadId == keyless.UploadId || started.Received != 0 {
		t.Errorf("a keyless upload resumed: %+v", started)
	}
}
