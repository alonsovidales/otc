// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// The offline alert posts to endpoints a device stored: never to the
// bridge itself, the WireGuard network or anything else internal.
func TestDenyNonPublic(t *testing.T) {
	for _, a := range []string{"127.0.0.1:443", "10.10.0.3:8444", "[::1]:443", "169.254.169.254:80",
		"100.100.100.100:443", "[::ffff:10.0.0.1]:443", "0.0.0.0:80", "[fe80::1]:443", "192.168.1.1:443",
		"[fd7a:115c:a1e0::1]:443", "224.0.0.1:443"} {
		if denyNonPublic("tcp", a, nil) == nil {
			t.Errorf("%s allowed", a)
		}
	}
	for _, a := range []string{"8.8.8.8:443", "[2001:4860:4860::8888]:443", "142.250.0.1:443"} {
		if err := denyNonPublic("tcp", a, nil); err != nil {
			t.Errorf("%s refused: %v", a, err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the client reached a loopback server")
	}))
	defer srv.Close()
	if _, err := bridgeWebPushClient.Post(srv.URL, "text/plain", strings.NewReader("x")); err == nil {
		t.Error("posted to a loopback address")
	}
	if err := bridgeWebPushClient.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("redirects are followed: %v", err)
	}
}

// What a device stores is filtered, never refused: an older device with
// one odd entry keeps the rest, and its alerts.
func TestFilterPushRegistrations(t *testing.T) {
	req := &pb.UpdatePushRegistrations{
		ApnsTokens: []string{"a", "", "a", strings.Repeat("x", 256), "b"},
		WebPushSubs: []*pb.WebPushSub{
			{Endpoint: "https://fcm.googleapis.com/fcm/send/1", P256Dh: "k", Auth: "a"},
			{Endpoint: "http://10.10.0.1:6379/", P256Dh: "k", Auth: "a"},
			{Endpoint: "javascript:alert(1)", P256Dh: "k", Auth: "a"},
			{Endpoint: "https://fcm.googleapis.com/fcm/send/1", P256Dh: "k", Auth: "a"},
			{Endpoint: "https://x/" + strings.Repeat("y", 1000), P256Dh: "k", Auth: "a"},
		},
	}
	for i := 0; i < cMaxPushRegistrations+5; i++ {
		req.FcmTokens = append(req.FcmTokens, fmt.Sprint("t", i))
	}
	apns, fcm, web, dropped := filterPushRegistrations(req)
	if strings.Join(apns, ",") != "a,b" {
		t.Errorf("apns %v", apns)
	}
	if len(fcm) != cMaxPushRegistrations || fcm[0] != "t0" {
		t.Errorf("%d fcm tokens kept", len(fcm))
	}
	if len(web) != 1 || web[0].Endpoint != "https://fcm.googleapis.com/fcm/send/1" {
		t.Errorf("web %v", web)
	}
	if dropped != 3+5+4 {
		t.Errorf("dropped %d", dropped)
	}
}
