// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/bridge/fleet"
)

func getFleet(t *testing.T, a *Admin) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.Fleet(rec, httptest.NewRequest(http.MethodGet, "/admin/api/fleet", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %q", rec.Body.String())
	}
	return rec.Code, body, rec.Header().Get("Cache-Control")
}

// Without a cluster there is nothing to read: an empty, disabled fleet.
func TestFleetWithoutCluster(t *testing.T) {
	code, body, cc := getFleet(t, &Admin{})
	if code != http.StatusOK || body["enabled"] != false || cc != "no-store" {
		t.Fatalf("%d %v %q", code, body, cc)
	}
	if hosts, ok := body["hosts"].([]any); !ok || len(hosts) != 0 {
		t.Fatalf("hosts must be an empty list: %v", body["hosts"])
	}
}

func TestFleetAnswersTheView(t *testing.T) {
	now := time.Now()
	hv := fleet.HostView{Name: "bridge1", Host: &fleet.HostSnapshot{Name: "bridge1", Time: now, Interval: 30},
		Bridge: &fleet.BridgeSnapshot{Node: "bridge1", Time: now, Interval: 30, Clients: 4, Devices: 2, FDs: -1, MaxFDs: -1}, HostSeen: now, BridgeSeen: now}
	fleet.Evaluate(&hv, now)
	a := &Admin{FleetView: func(ctx context.Context) (*fleet.View, error) {
		return &fleet.View{Time: now, Hosts: []fleet.HostView{hv}}, nil
	}}
	code, body, _ := getFleet(t, a)
	if code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("%d %v", code, body)
	}
	h := body["hosts"].([]any)[0].(map[string]any)
	if h["name"] != "bridge1" || h["bridge"].(map[string]any)["clients"].(float64) != 4 || len(h["checks"].([]any)) == 0 {
		t.Fatalf("%v", h)
	}
}

// Redis down: a 503 with a message, never the error's details.
func TestFleetRedisDown(t *testing.T) {
	a := &Admin{FleetView: func(ctx context.Context) (*fleet.View, error) {
		return nil, errors.New("dial tcp 10.10.0.1:6379: connect: connection refused")
	}}
	code, body, _ := getFleet(t, a)
	if code != http.StatusServiceUnavailable || strings.Contains(body["error"].(string), "10.10.0.1") {
		t.Fatalf("%d %v", code, body)
	}
}

// The route is behind the admin session like every other one.
func TestFleetNeedsASession(t *testing.T) {
	a := &Admin{sessionSecret: []byte("k"), FleetView: func(ctx context.Context) (*fleet.View, error) {
		t.Fatal("must not be read without a session")
		return nil, nil
	}}
	rec := httptest.NewRecorder()
	a.RequireAuth(a.Fleet)(rec, httptest.NewRequest(http.MethodGet, "/admin/api/fleet", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
}
