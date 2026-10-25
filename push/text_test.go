// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/i18n"
)

// relayed records what the relay was handed: what reached the phones.
type relayed struct {
	mu   sync.Mutex
	sent [][2]string
}

func (r *relayed) relay(title, body string, _ Target) bool {
	r.mu.Lock()
	r.sent = append(r.sent, [2]string{title, body})
	r.mu.Unlock()
	return true
}

func (r *relayed) last(t *testing.T) (string, string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		t.Fatal("nothing was relayed")
	}
	s := r.sent[len(r.sent)-1]
	return s[0], s[1]
}

func newRelayPush(t *testing.T, lang string) (*Push, *relayed) {
	t.Helper()
	p, err := Init(&fakeStorage{vapidPub: "pub", vapidPriv: "priv", tokens: []string{"phone"}})
	if err != nil {
		t.Fatal(err)
	}
	r := &relayed{}
	p.RelayMobile = r.relay
	if lang != "" {
		p.Language = func() string { return lang }
	}
	return p, r
}

// What a friend did reaches the phones in the same English as before: the
// friend's name as the title and the same body, whatever the push
// language while English is the only one shipping (a language this build
// has no text for renders English).
func TestFriendPushesEnglishUnchanged(t *testing.T) {
	for _, lang := range []string{"", "en", "ja", "qq"} { // languages no build carries get the English
		p, r := newRelayPush(t, lang)
		check := func(what, wantBody string) {
			t.Helper()
			title, body := r.last(t)
			if title != "Ana ⟨Bob⟩" || body != wantBody {
				t.Errorf("lang %q, %s: got %q / %q, want the name / %q", lang, what, title, body, wantBody)
			}
		}
		p.NotifyNewPost("Ana ⟨Bob⟩", "pub")
		check("new post", "posted something new")
		p.NotifyFriendshipRequest("Ana ⟨Bob⟩")
		check("friend request", "sent you a friend request")
		p.NotifyFriendshipAccepted("Ana ⟨Bob⟩")
		check("accepted", "accepted your friend request")
		// social's likes and comments.
		p.NotifyText(FromFriend("Ana ⟨Bob⟩", i18n.PushPostLiked()), Target{Kind: TargetPost})
		check("liked post", "liked your post")
		p.NotifyText(FromFriend("Ana ⟨Bob⟩", i18n.PushPostCommented()), Target{Kind: TargetPost})
		check("commented", "commented on your post")
		p.NotifyText(FromFriend("Ana ⟨Bob⟩", i18n.PushCommentLiked()), Target{Kind: TargetPost})
		check("liked comment", "liked your comment")
	}
}

// The critical-update push says in English exactly what it said when it
// was built in websocket's updateAlert: the title, and the release's
// summary followed by where to install it. "ja" is a language no build
// carries, so it gets the English too.
func TestCriticalUpdateEnglishUnchanged(t *testing.T) {
	for _, lang := range []string{"", "en", "ja"} {
		title, body := CriticalUpdate("2.4", "Fixes the sign-in on iOS 27.")(lang)
		if title != "Critical update 2.4 - please install it soon" || body != "Fixes the sign-in on iOS 27. Install it from Settings." {
			t.Errorf("lang %q: %q / %q", lang, title, body)
		}
	}
	// An empty summary kept its leading space too.
	if _, body := CriticalUpdate("3.0", "")(""); body != " Install it from Settings." {
		t.Errorf("empty summary: %q", body)
	}
}

// The critical-update push of a device in a container says how it is
// installed there: the summary and the commands in English, the commands'
// sentence alone in another language, under the same title.
func TestCriticalImageUpdate(t *testing.T) {
	title, body := CriticalImageUpdate("2.4", "Fixes the sign-in.")("")
	if title != "Critical update 2.4 - please install it soon" ||
		body != "Fixes the sign-in. To install it, run docker compose pull and docker compose up -d on your server." {
		t.Errorf("English: %q / %q", title, body)
	}
	title, body = CriticalImageUpdate("2.4", "Fixes the sign-in.")("es")
	if title != "Actualización crítica 2.4: instálala pronto" ||
		body != "Para instalarla, ejecuta docker compose pull y docker compose up -d en tu servidor." {
		t.Errorf("Spanish: %q / %q", title, body)
	}
}

// raidwatch's pushes, word for word: one disk per port, one with no known
// port, two (English only, from raidwatch's label), and the mirror whole
// again.
func TestStoragePushesEnglishUnchanged(t *testing.T) {
	cases := []struct {
		ports []string
		label string
		body  string
	}{
		{[]string{"top"}, "the disk in the top blue USB port", "The disk in the top blue USB port failed. Your files are safe on the other one: replace it soon."},
		{[]string{"bottom"}, "the disk in the bottom blue USB port", "The disk in the bottom blue USB port failed. Your files are safe on the other one: replace it soon."},
		{[]string{"unknown"}, "one of the two storage disks", "One of the two storage disks failed. Your files are safe on the other one: replace it soon."},
		{[]string{"top", "bottom"}, "the disk in the top blue USB port and the disk in the bottom blue USB port",
			"The disk in the top blue USB port and the disk in the bottom blue USB port failed. Your files are safe on the other one: replace it soon."},
	}
	for _, c := range cases {
		for _, lang := range []string{"", "en", "ja"} {
			title, body := StorageDiskStopped(c.ports, c.label, "unknown")(lang)
			if title != "Storage disk stopped working" || body != c.body {
				t.Errorf("lang %q, ports %v: %q / %q", lang, c.ports, title, body)
			}
		}
	}
	// The missing disk's port unknown, the working one's known.
	for working, want := range map[string]string{
		"top":     "One of the two storage disks failed. Your files are safe on the one in the top blue USB port: replace the other one soon.",
		"bottom":  "One of the two storage disks failed. Your files are safe on the one in the bottom blue USB port: replace the other one soon.",
		"unknown": "One of the two storage disks failed. Your files are safe on the other one: replace it soon.",
	} {
		if _, body := StorageDiskStopped([]string{"unknown"}, "one of the two storage disks", working)(""); body != want {
			t.Errorf("working %q: %q", working, body)
		}
	}
	// A missing disk whose port is known is named, whatever works.
	if _, body := StorageDiskStopped([]string{"top"}, "the disk in the top blue USB port", "bottom")(""); body != cases[0].body {
		t.Errorf("named disk and working: %q", body)
	}
	title, body := StorageDisksWorkingAgain()("")
	if title != "Storage disks working again" || body != "The mirror is complete: every file is on both disks again." {
		t.Errorf("working again: %q / %q", title, body)
	}
}

// The bridge's offline alert, as bridge/websocket's offlineAlertText
// writes it.
func TestOfflineAlertEnglishUnchanged(t *testing.T) {
	for _, lang := range []string{"", "en", "ja"} {
		if title, body := OfflineAlert(lang); title != "Off The Cloud" || body != "Your device appears to have gone offline" {
			t.Errorf("lang %q: %q / %q", lang, title, body)
		}
	}
}

// Notify's literal title and body go out as they are.
func TestNotifyLiteral(t *testing.T) {
	p, r := newRelayPush(t, "es")
	p.Notify("Off The Cloud", "anything", Target{})
	if title, body := r.last(t); title != "Off The Cloud" || body != "anything" {
		t.Errorf("got %q / %q", title, body)
	}
}

// A queued push is rendered in the language in effect when it goes out,
// not when it was queued: a language changed meanwhile is the one used.
func TestQueuedPushRendersInTheLanguageAtDelivery(t *testing.T) {
	p, r := newRelayPush(t, "")
	var mu sync.Mutex
	lang := "en"
	p.Language = func() string { mu.Lock(); defer mu.Unlock(); return lang }
	started, release := make(chan struct{}), make(chan struct{})
	p.StartAsync(4)

	var once sync.Once
	p.NotifyText(func(string) (string, string) {
		once.Do(func() { close(started); <-release })
		return "first", "b"
	}, Target{})
	<-started
	var seen string
	p.NotifyText(func(l string) (string, string) { seen = l; return "second", "b" }, Target{})
	mu.Lock()
	lang = "fr"
	mu.Unlock()
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.sent)
		r.mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if title, _ := r.last(t); title != "second" || seen != "fr" {
		t.Fatalf("the queued push went out as %q rendered in %q, want second in fr", title, seen)
	}
}
