package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPolicySetRoutesRequireAuth pins the wire shape of the slice-15
// endpoints. Losing auth on policy_sets would let any caller push
// policy to any tenant's agents — far worse than a leaked live feed.
func TestPolicySetRoutesRequireAuth(t *testing.T) {
	handler := setupTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/policy_sets"},
		{http.MethodPut, "/v1/policy_sets/some-id"},
		{http.MethodGet, "/v1/policy_sets/some-id"},
		{http.MethodPost, "/v1/policy_sets/resolve"},
		{http.MethodPost, "/v1/agents/some-name/labels"},
		{http.MethodPost, "/v1/agents/some-name/policy_applied"},
		{http.MethodGet, "/v1/agents/some-name/policy_updates"},
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
