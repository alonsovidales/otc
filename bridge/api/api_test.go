package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	// registerAPIs also wires up api.websocket.Listen on a nil *websocket.Manager;
	// that's fine as long as nothing exercises the websocket endpoint here.
	api := &API{muxHTTPServer: http.NewServeMux()}
	api.registerAPIs()

	req := httptest.NewRequest(http.MethodGet, cHealtyPath, nil)
	rec := httptest.NewRecorder()
	api.muxHTTPServer.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "OK" {
		t.Errorf("expected body %q, got %q", "OK", rec.Body.String())
	}
}
