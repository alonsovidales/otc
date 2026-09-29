// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/mediastream"
)

type streamKeys struct{ aead cipher.AEAD }

func (k streamKeys) AEAD() cipher.AEAD              { return k.aead }
func (k streamKeys) Decrypt([]byte) ([]byte, error) { return nil, errors.New("unused") }

// A stored (segmented, encrypted) video reaches ffmpeg over a loopback
// URL with range requests - the path processing takes instead of loading
// the video whole or writing it out in plaintext.
func TestStoredVideoIsProcessedThroughTheStream(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	gen := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=3:size=320x240:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-metadata", "location=+52.1+004.4/", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("making the test video: %v %s", err, out)
	}
	plain, _ := os.ReadFile(src)

	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	keys := streamKeys{aead}
	store := filepath.Join(dir, "store")
	os.Mkdir(store, 0o755)
	if err := blobstore.WriteBytes(filepath.Join(store, "vidhash"), keys, plain); err != nil {
		t.Fatal(err)
	}

	sv := mediastream.NewServer(mediastream.NewStore(), store, t.TempDir())
	token, _, _ := sv.Store().Issue(mediastream.Resource{Kind: mediastream.KindLibraryFile, Hash: "vidhash", Mime: "video/mp4", Keys: keys})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream, err := sv.Open(token) // what api.serveMedia does
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer stream.Close()
		http.ServeContent(w, r, "", time.Time{}, stream.ReadSeeker())
	}))
	defer srv.Close()

	frames, err := extractVideoFramesFrom(srv.URL+"/media/"+token, 3)
	if err != nil || len(frames) == 0 {
		t.Fatalf("frames over the stream: %d, %v", len(frames), err)
	}
	if b := frames[0].Bounds(); b.Dx() != 320 || b.Dy() != 240 {
		t.Errorf("frame is %v, want 320x240", b)
	}
	if _, err := exifinfo.FromVideoSource(srv.URL + "/media/" + token); err != nil {
		t.Errorf("metadata over the stream: %v", err)
	}
}
