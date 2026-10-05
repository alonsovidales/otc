// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestFcmTokenGone(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"unregistered", 404, `{"error":{"code":404,"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`, true},
		{"bad token", 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The registration token is not a valid FCM registration token","details":[{"errorCode":"INVALID_ARGUMENT"}]}}`, true},
		{"other bad argument", 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Invalid JSON payload","details":[{"errorCode":"INVALID_ARGUMENT"}]}}`, false},
		{"quota", 429, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"errorCode":"QUOTA_EXCEEDED"}]}}`, false},
		{"server error", 503, `unavailable`, false},
	}
	for _, c := range cases {
		if got := fcmTokenGone(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: fcmTokenGone = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNewFcmSenderReadsServiceAccount(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	raw, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "test-project",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "sender@test-project.iam.gserviceaccount.com",
	})
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := newFcmSender(path, "")
	if err != nil {
		t.Fatalf("newFcmSender: %v", err)
	}
	if s.projectID != "test-project" || s.tokenURI != cGoogleToken {
		t.Errorf("got project %q, token uri %q", s.projectID, s.tokenURI)
	}
	if s, _ := newFcmSender(path, "override"); s == nil || s.projectID != "override" {
		t.Errorf("project-id from the config should win")
	}

	// The Android app's own google-services.json is not a sending key.
	client := filepath.Join(t.TempDir(), "google-services.json")
	_ = os.WriteFile(client, []byte(`{"project_info":{"project_id":"x"}}`), 0o600)
	if _, err := newFcmSender(client, ""); err == nil {
		t.Error("a client config must be refused")
	}
}

// The bridge calls Init for every notification it relays: the phone
// senders loaded once are reused, not rebuilt (and reconnected) per push.
func TestInitReusesTheLoadedMobileSenders(t *testing.T) {
	shared := &fcmSender{projectID: "p"}
	mobileMu.Lock()
	fcmShared, apnsShared = shared, &apnsSenders{topic: "cloud.off-the.test"}
	mobileMu.Unlock()
	t.Cleanup(func() {
		mobileMu.Lock()
		fcmShared, apnsShared = nil, nil
		mobileMu.Unlock()
	})

	for i := 0; i < 2; i++ {
		p, err := Init(&fakeStorage{vapidPub: "pub", vapidPriv: "priv"})
		if err != nil {
			t.Fatal(err)
		}
		if p.fcm != shared || p.apnsTopic != "cloud.off-the.test" {
			t.Fatalf("Init %d built its own senders: %+v", i, p)
		}
	}
}
