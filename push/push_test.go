// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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

	p.Notify("title", "body")

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

	p.Notify("title", "body")

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
