// SPDX-License-Identifier: AGPL-3.0-or-later

package mediastream

import (
	"bytes"
	"errors"
	"fmt"
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

// A library file is encrypted at rest, and decrypting it for every range
// a player asks for would re-decrypt the whole video on every seek.
func TestLibraryPlaintextIsDecryptedOncePerFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fmt.Sprintf("%s/%s", dir, "enc1"), []byte("ENCRYPTED"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	decrypts := 0
	sv := NewServer(NewStore(), dir, t.TempDir())
	token, _, _ := sv.Store().Issue(Resource{
		Kind: KindLibraryFile,
		Hash: "enc1",
		Decrypt: func(b []byte) ([]byte, error) {
			decrypts++
			return []byte("plaintext!"), nil
		},
	})

	for range 3 {
		if _, _, _, err := sv.Range(token, 0, 4); err != nil {
			t.Fatalf("Range: %v", err)
		}
	}
	if decrypts != 1 {
		t.Errorf("decrypted %d times, want exactly 1 - the rest must come from the cache", decrypts)
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
