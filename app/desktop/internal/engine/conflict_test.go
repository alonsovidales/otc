// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// conflictFiles: the "(conflict …)" copies next to name in dir.
func conflictFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), "(conflict") {
			out = append(out, e.Name())
		}
	}
	return out
}

// Both sides changed doc.txt since the last sync. Whichever version is
// older loses, and is kept as a "(conflict …)" copy instead of being
// overwritten (it used to be lost).
func TestConflictKeepsTheLosingVersion(t *testing.T) {
	for _, remoteNewer := range []bool{true, false} {
		withConfigDir(t)
		base, mine, theirs := []byte("base"), []byte("edited here"), []byte("edited elsewhere")
		now := time.Now()
		remoteTime, localTime := now, now.Add(-time.Hour)
		if !remoteNewer {
			remoteTime, localTime = now.Add(-time.Hour), now
		}
		d := &fakeDevice{files: map[string][]byte{"/pc/folder/doc.txt": theirs}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{},
			list: []*pb.File{{Path: "/pc/folder/doc.txt", Hash: sha(theirs), Size: int32(len(theirs)), Modified: timestamppb.New(remoteTime)}}}
		conn := connectedEngine(t, d)
		local := t.TempDir()
		p := filepath.Join(local, "doc.txt")
		if err := os.WriteFile(p, mine, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, localTime, localTime)
		f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
		e := New(&config.Config{RemoteFolders: []config.RemoteFolder{f}}, "", nil)
		e.ws = conn.ws
		e.saveSynced(f.ID, map[string]string{"doc.txt": sha(base)})

		e.reconcileRemoteFolder(f)

		winner, loser := theirs, mine
		if !remoteNewer {
			winner, loser = mine, theirs
		}
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, winner) {
			t.Errorf("remoteNewer=%v: doc.txt is %q, want %q", remoteNewer, got, winner)
		}
		copies := conflictFiles(t, local)
		if len(copies) != 1 {
			t.Fatalf("remoteNewer=%v: want one conflict copy, got %v", remoteNewer, copies)
		}
		kept, _ := os.ReadFile(filepath.Join(local, copies[0]))
		if !bytes.Equal(kept, loser) {
			t.Errorf("remoteNewer=%v: the copy holds %q, want the losing version %q", remoteNewer, kept, loser)
		}
		if !remoteNewer && !bytes.Equal(d.files["/pc/folder/doc.txt"], mine) {
			t.Errorf("the newer local version was not uploaded")
		}
	}
}

func TestConflictPathNames(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 4, 20, 15, 0, 0, time.Local)
	p := filepath.Join(dir, "report.final.txt")
	want := filepath.Join(dir, "report.final (conflict from laptop 2026-10-04 20.15).txt")
	if got := conflictPath(p, "laptop", at); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	_ = os.WriteFile(want, nil, 0o644)
	if got := conflictPath(p, "laptop", at); !strings.HasSuffix(got, "(conflict from laptop 2026-10-04 20.15 2).txt") {
		t.Fatalf("a taken name was reused: %s", got)
	}
	if got := conflictPath(filepath.Join(dir, ".bashrc"), "", at); filepath.Base(got) != ".bashrc (conflict 2026-10-04 20.15)" {
		t.Fatalf("dotfile: %s", filepath.Base(got))
	}
}
