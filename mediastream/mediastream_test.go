// SPDX-License-Identifier: AGPL-3.0-or-later

package mediastream

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/blobstore"
	"os"
	"testing"
	"time"
)

// A token is the only credential the /media/<token> endpoint has, so what
// it will and won't resolve to is the whole of this feature's access
// control.
func TestResolveReturnsOnlyTheResourceTheTokenWasMintedFor(t *testing.T) {
	store := NewStore()
	token, _, err := store.Issue(Resource{Kind: KindPublicationMedia, Hash: "abc", Mime: "video/mp4"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, ok := store.Resolve(token)
	if !ok {
		t.Fatal("a freshly minted token did not resolve")
	}
	if got.Hash != "abc" || got.Kind != KindPublicationMedia {
		t.Errorf("resolved %+v, want the resource it was minted for", got)
	}

	if _, ok := store.Resolve("not-a-token"); ok {
		t.Error("an unknown token resolved")
	}
}

// Unlike a session token (session/tokens.go), this one has to survive
// being used: a player sends it once per range it needs, which for a long
// video is hundreds of times.
func TestResolveIsNotSingleUse(t *testing.T) {
	store := NewStore()
	token, _, _ := store.Issue(Resource{Hash: "abc"})

	for i := range 5 {
		if _, ok := store.Resolve(token); !ok {
			t.Fatalf("token stopped resolving after %d uses", i)
		}
	}
}

func TestResolveRefusesAnExpiredToken(t *testing.T) {
	store := NewStore()
	token, _, _ := store.Issue(Resource{Hash: "abc"})

	// Reach in and age the entry rather than sleeping out a real TTL.
	store.mutex.Lock()
	store.tokens[token].expiresAt = time.Now().Add(-time.Second)
	store.mutex.Unlock()

	if _, ok := store.Resolve(token); ok {
		t.Error("an expired token still resolved")
	}
}

// Publication media is stored unencrypted, which is what lets a range be
// read straight off disk - the case that matters most, since it's every
// video in the social feed.
func TestRangeReadsASpanOfPublicationMedia(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "hash1"), content, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	sv := NewServer(NewStore(), t.TempDir(), dir)
	token, _, _ := sv.Store().Issue(Resource{
		Kind: KindPublicationMedia, Hash: "hash1", Mime: "video/mp4", Size: int64(len(content)),
	})

	got, total, mime, err := sv.Range(token, 10, 5)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if string(got) != "01234" {
		t.Errorf("Range(10, 5) = %q, want %q", got, "01234")
	}
	if total != 1000 {
		t.Errorf("total = %d, want 1000", total)
	}
	if mime != "video/mp4" {
		t.Errorf("mime = %q, want video/mp4", mime)
	}
}

// "bytes=0-" means "the rest of the file", and answering it literally is
// the download this whole feature exists to avoid.
func TestRangeIsCappedAtMaxRangeBytes(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("x"), int(MaxRangeBytes)+4096)
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "big"), content, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	sv := NewServer(NewStore(), t.TempDir(), dir)
	token, _, _ := sv.Store().Issue(Resource{Kind: KindPublicationMedia, Hash: "big"})

	got, total, _, err := sv.Range(token, 0, int64(len(content)))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if int64(len(got)) != MaxRangeBytes {
		t.Errorf("returned %d bytes, want the %d cap", len(got), MaxRangeBytes)
	}
	if total != int64(len(content)) {
		t.Errorf("total = %d, want %d - the cap must not change the reported size", total, len(content))
	}
}

// The last span of a file is shorter than the cap, and reporting the full
// cap there would tell a player bytes exist past the end of the file.
func TestRangeStopsAtTheEndOfTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "small"), []byte("abcdefghij"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	sv := NewServer(NewStore(), t.TempDir(), dir)
	token, _, _ := sv.Store().Issue(Resource{Kind: KindPublicationMedia, Hash: "small"})

	got, _, _, err := sv.Range(token, 7, MaxRangeBytes)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if string(got) != "hij" {
		t.Errorf("Range(7, max) = %q, want %q", got, "hij")
	}
}

func TestRangeRefusesAnUnknownToken(t *testing.T) {
	sv := NewServer(NewStore(), t.TempDir(), t.TempDir())
	if _, _, _, err := sv.Range("nope", 0, 100); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("Range with an unknown token: %v, want ErrUnknownToken", err)
	}
}

// testKeys is a session's data key for the tests.
type testKeys struct{ aead cipher.AEAD }

func (k testKeys) AEAD() cipher.AEAD { return k.aead }
func (k testKeys) Decrypt(b []byte) ([]byte, error) {
	return nil, errors.New("legacy format not used here")
}

// A library file is stored in segments: a range reads only the segments
// it covers, and what comes back is the plaintext at that offset - also
// across a segment boundary.
func TestLibraryRangesReadTheSegmentedFile(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	keys := testKeys{aead}
	plain := make([]byte, 3<<20+777)
	for i := range plain {
		plain[i] = byte(i * 7)
	}
	if err := blobstore.WriteBytes(fmt.Sprintf("%s/%s", dir, "vid"), keys, plain); err != nil {
		t.Fatal(err)
	}
	sv := NewServer(NewStore(), dir, t.TempDir())
	token, _, _ := sv.Store().Issue(Resource{Kind: KindLibraryFile, Hash: "vid", Keys: keys})

	off := int64(1<<20 - 100) // straddles the first boundary
	got, total, _, err := sv.Range(token, off, 300)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if total != int64(len(plain)) || !bytes.Equal(got, plain[off:off+300]) {
		t.Errorf("Range(%d, 300): total %d, bytes differ: %v", off, total, !bytes.Equal(got, plain[off:off+300]))
	}
}

// Without a session there is nothing to decrypt with, and serving the
// ciphertext as if it were the video would be worse than failing.
func TestLibraryFileWithoutADecrypterFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "enc2"), []byte("ENCRYPTED"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	sv := NewServer(NewStore(), dir, t.TempDir())
	token, _, _ := sv.Store().Issue(Resource{Kind: KindLibraryFile, Hash: "enc2"})

	if _, _, _, err := sv.Range(token, 0, 4); err == nil {
		t.Error("a library file with no way to decrypt it was served anyway")
	}
}

// Only video is worth streaming, and serving an image as a byte range
// actively breaks it: the normal fetch converts HEIC to JPEG on the way
// out, which a raw range bypasses. A client asking for a stream of a
// photo is a bug - this is what stops that bug reaching the person using
// it as a video player opening over their photo.
func TestOnlyVideoIsStreamable(t *testing.T) {
	for mime, want := range map[string]bool{
		"video/mp4":        true,
		"video/quicktime":  true,
		"image/jpeg":       false,
		"image/heic":       false,
		"application/pdf":  false,
		"":                 false,
		"videos/not-quite": false,
	} {
		if got := IsStreamable(mime); got != want {
			t.Errorf("IsStreamable(%q) = %v, want %v", mime, got, want)
		}
	}
}

// Anyone with a gallery link can ask for a video's stream over and over;
// each ask used to mint a new token, growing the store without bound and
// lengthening the sweep every /media request waits behind. The same
// resource gets the same token for ReuseWindow, then a fresh one, while
// the old one keeps working until it expires.
func TestIssueSharedReusesWithinTheWindow(t *testing.T) {
	store := NewStore()
	res := Resource{Kind: KindPublicationMedia, Hash: "abc", Mime: "video/mp4"}

	a, expA, err := store.IssueShared("pub:x/abc", res)
	if err != nil {
		t.Fatalf("IssueShared: %v", err)
	}
	b, expB, _ := store.IssueShared("pub:x/abc", res)
	if a != b || !expA.Equal(expB) {
		t.Fatal("the same resource within the window must get the same token")
	}
	if c, _, _ := store.IssueShared("pub:x/other", res); c == a {
		t.Fatal("another key must get its own token")
	}
	if d, _, _ := store.Issue(res); d == a {
		t.Fatal("Issue must always mint a new token")
	}

	store.mutex.Lock()
	store.tokens[a].expiresAt = time.Now().Add(TTL - ReuseWindow - time.Second)
	store.mutex.Unlock()
	fresh, _, _ := store.IssueShared("pub:x/abc", res)
	if fresh == a {
		t.Fatal("past the reuse window a new token must be minted")
	}
	if _, ok := store.Resolve(a); !ok {
		t.Fatal("the older token must keep working until it expires")
	}

	// Gone from the reuse index once revoked or expired.
	store.Revoke(fresh)
	store.mutex.Lock()
	_, indexed := store.byKey["pub:x/abc"]
	store.tokens[a].expiresAt = time.Now().Add(-time.Second)
	store.mutex.Unlock()
	if indexed {
		t.Fatal("a revoked token must leave the reuse index")
	}
	if again, _, _ := store.IssueShared("pub:x/abc", res); again == a || again == fresh {
		t.Fatal("a revoked or expired token must never be handed out again")
	}
}

// A stream carries its file's own name, what a download is saved as; a
// post's media, addressed by hash alone, has none.
func TestStreamIsNamedAfterItsPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "h"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	sv := NewServer(NewStore(), t.TempDir(), dir)
	for path, want := range map[string]string{
		"/Videos/Cumpleaños Leo.mov": "Cumpleaños Leo.mov",
		"clip.mp4":                   "clip.mp4",
		"":                           "",
		"/":                          "",
	} {
		token, _, _ := sv.Store().Issue(Resource{Kind: KindPublicationMedia, Path: path, Hash: "h", Mime: "video/mp4"})
		stream, err := sv.Open(token)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if stream.Name != want {
			t.Errorf("path %q: Name %q, want %q", path, stream.Name, want)
		}
		stream.Close()
	}
}

// Only media is ever served as itself, by /media and by ReqGetMediaRange
// alike (the bridge copies the latter into its Content-Type, on the app's
// own origin): anything else, and anything that isn't exactly one
// well-formed type, is bytes to save.
func TestServedTypeIsOnlyEverMedia(t *testing.T) {
	for _, m := range []string{"video/mp4", "video/webm", "audio/mpeg", "image/jpeg", "image/heic", "video/mp4; codecs=\"avc1.42E01E\""} {
		if ct, inline := ServedType(m); !inline || ct != m {
			t.Errorf("ServedType(%q) = %q, %v", m, ct, inline)
		}
	}
	for _, m := range []string{"text/html", "image/svg+xml", "application/xhtml+xml", "", "video/mp4,text/html", "video/mp4;,text/html", "video/mp4; x=\"", "text/plain"} {
		if ct, inline := ServedType(m); inline || ct != "application/octet-stream" {
			t.Errorf("ServedType(%q) = %q, %v", m, ct, inline)
		}
	}
}

// A range answers the served type, not the stored one: a token minted for
// "video/mp4,text/html" (IsStreamable lets it through) must not reach the
// bridge as a type Chrome renders as a page.
func TestRangeAnswersTheServedType(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "h"), []byte("<script>x</script>"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	sv := NewServer(NewStore(), t.TempDir(), dir)
	for stored, want := range map[string]string{
		"video/mp4,text/html": "application/octet-stream",
		"video/mp4":           "video/mp4",
	} {
		token, _, _ := sv.Store().Issue(Resource{Kind: KindPublicationMedia, Hash: "h", Mime: stored})
		_, _, ctype, err := sv.Range(token, 0, 4)
		if err != nil || ctype != want {
			t.Errorf("Range of %q: type %q, %v; want %q", stored, ctype, err, want)
		}
		// Also when the range itself fails.
		if _, _, ctype, err := sv.Range(token, 1000, 4); err == nil || ctype != want {
			t.Errorf("Range of %q past the end: type %q, %v; want %q and an error", stored, ctype, err, want)
		}
	}
}
