// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStorage is an in-memory push.Storage for tests - a real *dao.Dao
// would need a live MySQL connection just to exercise Notify's pruning
// paths, which is the only thing these tests care about.
type fakeStorage struct {
	vapidPub, vapidPriv string
	subs                []*WebPushSubscription
	tokens              []string
	deletedEndpoints    []string
	deletedTokens       []string
	fcmTokens           []string
	deletedFcmTokens    []string
}

func (f *fakeStorage) ListFcmTokens() ([]string, error) { return f.fcmTokens, nil }
func (f *fakeStorage) DeleteFcmToken(token string) error {
	f.deletedFcmTokens = append(f.deletedFcmTokens, token)
	return nil
}

func (f *fakeStorage) GetVapidKeys() (string, string, error) { return f.vapidPub, f.vapidPriv, nil }
func (f *fakeStorage) SetVapidKeys(pub, priv string) error {
	f.vapidPub, f.vapidPriv = pub, priv
	return nil
}
func (f *fakeStorage) ListWebPushSubscriptions() ([]*WebPushSubscription, error) { return f.subs, nil }
func (f *fakeStorage) DeleteWebPushSubscription(endpoint string) error {
	f.deletedEndpoints = append(f.deletedEndpoints, endpoint)
	kept := f.subs[:0]
	for _, s := range f.subs {
		if s.Endpoint != endpoint {
			kept = append(kept, s)
		}
	}
	f.subs = kept
	return nil
}
func (f *fakeStorage) ListApnsTokens() ([]string, error) { return f.tokens, nil }
func (f *fakeStorage) DeleteApnsToken(token string) error {
	f.deletedTokens = append(f.deletedTokens, token)
	kept := f.tokens[:0]
	for _, t := range f.tokens {
		if t != token {
			kept = append(kept, t)
		}
	}
	f.tokens = kept
	return nil
}

// fakeClientSubscriptionKeys generates a syntactically valid Web Push
// client keypair (the P256dh/Auth a browser's PushSubscription would carry)
// so webpush-go's own encryption succeeds locally - only the HTTP POST to
// the (fake, local) push service endpoint needs to actually happen for
// these tests.
func fakeClientSubscriptionKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating fake client key: %v", err)
	}
	authBytes := make([]byte, 16)
	if _, err := rand.Read(authBytes); err != nil {
		t.Fatalf("generating fake auth secret: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(authBytes)
}

// A push service telling us a subscription is gone (404/410) must both
// prune it from storage and fire OnChange - issue #62 relies on OnChange to
// keep the bridge's mirrored copy of these registrations in sync whenever
// the device's own Push instance cleans one up.
func TestSendWebPushPrunesStaleSubscriptionAndFiresOnChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	p256dh, auth := fakeClientSubscriptionKeys(t)
	storage := &fakeStorage{subs: []*WebPushSubscription{{Endpoint: srv.URL, P256dh: p256dh, Auth: auth}}}

	p, err := Init(storage)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var onChangeCalls int32
	p.OnChange = func() { atomic.AddInt32(&onChangeCalls, 1) }

	p.Notify("title", "body", Target{Kind: TargetPost, PubUUID: "pub-1"})

	if len(storage.subs) != 0 {
		t.Errorf("expected the stale subscription to be pruned, still have: %+v", storage.subs)
	}
	if len(storage.deletedEndpoints) != 1 || storage.deletedEndpoints[0] != srv.URL {
		t.Errorf("expected exactly one delete for %q, got: %v", srv.URL, storage.deletedEndpoints)
	}
	if got := atomic.LoadInt32(&onChangeCalls); got != 1 {
		t.Errorf("OnChange called %d times, want exactly 1", got)
	}
}

// A live subscription (2xx from the push service) must never be pruned,
// and OnChange must never fire - the common case, that this doesn't
// regress into an always-firing hook.
func TestSendWebPushLeavesLiveSubscriptionAloneAndNeverFiresOnChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	p256dh, auth := fakeClientSubscriptionKeys(t)
	storage := &fakeStorage{subs: []*WebPushSubscription{{Endpoint: srv.URL, P256dh: p256dh, Auth: auth}}}

	p, err := Init(storage)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var onChangeCalls int32
	p.OnChange = func() { atomic.AddInt32(&onChangeCalls, 1) }

	p.Notify("title", "body", Target{Kind: TargetPost, PubUUID: "pub-1"})

	if len(storage.subs) != 1 {
		t.Errorf("expected the live subscription to survive, have: %+v", storage.subs)
	}
	if got := atomic.LoadInt32(&onChangeCalls); got != 0 {
		t.Errorf("OnChange called %d times, want 0 for a live subscription", got)
	}
}

// Init must load whatever keypair storage already reports rather than
// generating a fresh one - the bridge's per-domain adapter (issue #62)
// depends on this: it must send with exactly the keypair the browser's
// subscription was created against, never a substitute.
func TestInitLoadsExistingVapidKeysRatherThanGeneratingNew(t *testing.T) {
	storage := &fakeStorage{vapidPub: "existing-pub", vapidPriv: "existing-priv"}

	p, err := Init(storage)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if p.VapidPublicKey() != "existing-pub" {
		t.Errorf("VapidPublicKey() = %q, want the pre-existing key to be kept", p.VapidPublicKey())
	}
}

// What a tap opens travels in the payload as IDs only.
func TestTargetData(t *testing.T) {
	d := Target{Kind: TargetPost, PubUUID: "pub-1", CommentUUID: "c-1"}.data()
	if d["kind"] != "post" || d["pub_uuid"] != "pub-1" || d["comment_uuid"] != "c-1" || d["otc"] != "notification" {
		t.Errorf("post target data = %v", d)
	}
	d = Target{Kind: TargetFriends}.data()
	if _, ok := d["pub_uuid"]; ok || d["kind"] != "friends" {
		t.Errorf("friends target data = %v", d)
	}
}

// Started on the device: notifications are delivered by one worker in the
// order they came, and once its queue is full the caller sends inline -
// slower, never dropped.
func TestNotifyQueueKeepsOrderAndSendsInlineWhenFull(t *testing.T) {
	p, err := Init(&fakeStorage{vapidPub: "pub", vapidPriv: "priv", tokens: []string{"phone"}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	started, release := make(chan struct{}), make(chan struct{})
	p.RelayMobile = func(title, body string, t Target) bool {
		if title == "first" {
			close(started)
			<-release
		}
		mu.Lock()
		got = append(got, title)
		mu.Unlock()
		return true
	}
	p.StartAsync(1)

	p.Notify("first", "b", Target{}) // the worker takes it and blocks
	<-started
	p.Notify("second", "b", Target{}) // waits in the queue
	p.Notify("third", "b", Target{})  // queue full: sent before Notify returns
	mu.Lock()
	if len(got) != 1 || got[0] != "third" {
		t.Fatalf("a full queue should send inline, delivered so far: %v", got)
	}
	mu.Unlock()

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[1] != "first" || got[2] != "second" {
		t.Fatalf("queued notifications out of order or lost: %v", got)
	}
}

type noFcmTable struct{ *fakeStorage }

func (noFcmTable) ListFcmTokens() ([]string, error) { return nil, errors.New("no such table") }

// A device with no phone registered doesn't dial the bridge to deliver
// nothing; one whose token list can't be read still relays.
func TestSendMobileSkipsTheRelayWithoutPhones(t *testing.T) {
	relayed := 0
	relay := func(title, body string, t Target) bool { relayed++; return true }

	p, err := Init(&fakeStorage{vapidPub: "pub", vapidPriv: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	p.RelayMobile = relay
	p.Notify("t", "b", Target{})
	if relayed != 0 {
		t.Fatalf("relayed %d times with no phone registered", relayed)
	}

	p, err = Init(noFcmTable{&fakeStorage{vapidPub: "pub", vapidPriv: "priv"}})
	if err != nil {
		t.Fatal(err)
	}
	p.RelayMobile = relay
	p.Notify("t", "b", Target{})
	if relayed != 1 {
		t.Fatalf("an unreadable token list should still relay, relayed %d", relayed)
	}
}
