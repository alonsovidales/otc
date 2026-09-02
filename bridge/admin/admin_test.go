package admin

import (
	"errors"
	"testing"
	"time"
)

func TestSessionTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	token := newSessionToken(secret, "alice", now)

	username, ok := verifySessionToken(secret, token, now)
	if !ok {
		t.Fatal("expected a freshly issued token to verify")
	}
	if username != "alice" {
		t.Errorf("username = %q, want %q", username, "alice")
	}
}

func TestSessionTokenExpires(t *testing.T) {
	secret := []byte("test-secret")
	issued := time.Now()

	token := newSessionToken(secret, "alice", issued)

	if _, ok := verifySessionToken(secret, token, issued.Add(cSessionTTL-time.Minute)); !ok {
		t.Error("expected the token to still be valid just before its TTL elapses")
	}
	if _, ok := verifySessionToken(secret, token, issued.Add(cSessionTTL+time.Minute)); ok {
		t.Error("expected the token to be rejected once its TTL has elapsed")
	}
}

func TestSessionTokenRejectsTamperedPayload(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	token := newSessionToken(secret, "alice", now)
	// Swap the username but keep the original signature.
	forged := "bob|" + token[len("alice|"):]

	if _, ok := verifySessionToken(secret, forged, now); ok {
		t.Error("expected a token with a tampered payload to fail verification")
	}
}

func TestSessionTokenRejectsWrongSecret(t *testing.T) {
	now := time.Now()
	token := newSessionToken([]byte("secret-a"), "alice", now)

	if _, ok := verifySessionToken([]byte("secret-b"), token, now); ok {
		t.Error("expected a token signed with a different secret to fail verification")
	}
}

func TestSessionTokenRejectsMalformedInput(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	cases := []string{
		"",
		"no-dot-separator",
		"payload-with-no-pipe." + signToken(secret, "payload-with-no-pipe"),
		"alice|not-a-number." + signToken(secret, "alice|not-a-number"),
	}

	for _, tc := range cases {
		if _, ok := verifySessionToken(secret, tc, now); ok {
			t.Errorf("expected malformed token %q to fail verification", tc)
		}
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	if !checkPassword(hash, "correct horse battery staple") {
		t.Error("expected the correct password to check out against its own hash")
	}
	if checkPassword(hash, "wrong password") {
		t.Error("expected an incorrect password to fail the check")
	}
}

func TestIsDuplicateKeyErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"duplicate entry", errors.New("Error 1062: Duplicate entry 'x' for key 'domain'"), true},
		{"unrelated error", errors.New("connection refused"), false},
	}

	for _, c := range cases {
		if got := isDuplicateKeyErr(c.err); got != c.want {
			t.Errorf("%s: isDuplicateKeyErr() = %v, want %v", c.name, got, c.want)
		}
	}
}
