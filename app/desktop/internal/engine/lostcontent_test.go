// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #141: a file the device lists without a hash has lost its content
// there. The copy here goes up again (restoring it) - it used to be taken
// for a change on the device and downloaded, failing on every pass.
func TestTwoWayResendsContentTheDeviceLost(t *testing.T) {
	withConfigDir(t)
	content := []byte("the photo")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{},
		list: []*pb.File{{Path: "/pc/folder/lost.jpg", Mime: "image/jpeg"}}}
	conn := connectedEngine(t, d)

	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "lost.jpg"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := New(&config.Config{RemoteFolders: []config.RemoteFolder{f}}, "", nil)
	e.ws = conn.ws
	e.saveSynced(f.ID, map[string]string{"lost.jpg": hash}) // it was in sync once

	e.reconcileRemoteFolder(f)

	if d.reads != 0 {
		t.Fatalf("tried to download a file whose content the device lost (%d reads)", d.reads)
	}
	if !bytes.Equal(d.files["/pc/folder/lost.jpg"], content) {
		t.Fatal("the local copy was not sent again")
	}
	if _, err := os.Stat(filepath.Join(local, "lost.jpg")); err != nil {
		t.Fatalf("the local copy is gone: %v", err)
	}
}
