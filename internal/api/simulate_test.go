package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSimulateRoutesRequireAuth pins the wire-level shape of the new
// slice 13 endpoints: both must be mounted under /v1 and rejected
// without auth. A regression here would mean either the routes
// disappeared or the auth middleware stopped covering /v1 — both
// would silently let unauthenticated callers run simulator jobs.
func TestSimulateRoutesRequireAuth(t *testing.T) {
	handler := setupTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/policy/simulate"},
		{http.MethodGet, "/v1/policy/simulate/job-id-doesnt-matter"},
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
