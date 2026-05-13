package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLiveRoutesRequireAuth pins the wire-level shape of the new
// slice 14 endpoints. Both must be mounted under /v1 and rejected
// without auth — losing the auth requirement on the live channel
// would let any caller subscribe to another tenant's intent stream.
func TestLiveRoutesRequireAuth(t *testing.T) {
	handler := setupTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/intents"},
		{http.MethodGet, "/v1/orgs/some-org/live"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without auth: want 401, got %d", c.method, c.path, rec.Code)
		}
	}
}
