package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCSRF_GETIssuesCookie(t *testing.T) {
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/v1/version")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	found := false
	for _, ck := range resp.Cookies() {
		if ck.Name == "relic_csrf" {
			found = true
			if ck.Value == "" {
				t.Errorf("empty csrf cookie")
			}
			if ck.SameSite != http.SameSiteStrictMode {
				t.Errorf("SameSite=%v want Strict", ck.SameSite)
			}
		}
	}
	if !found {
		t.Errorf("no relic_csrf cookie issued on GET")
	}
}

func TestCSRF_POSTWithSessionCookieAndCSRFCookieMatchingHeaderAccepted(t *testing.T) {
	// Browser-style request: session cookie present, CSRF cookie set
	// by an earlier GET, header mirrored on the POST.
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/orgs", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "relic_session", Value: "sess"})
	req.AddCookie(&http.Cookie{Name: "relic_csrf", Value: "abc"})
	req.Header.Set("X-CSRF-Token", "abc")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST with full csrf set got %d, want 200", resp.StatusCode)
	}
}

func TestCSRF_POSTWithMatchingTokenAccepted(t *testing.T) {
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	// Step 1: GET to seed the cookie.
	getResp, err := srv.Client().Get(srv.URL + "/v1/version")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	getResp.Body.Close()
	var token string
	for _, ck := range getResp.Cookies() {
		if ck.Name == "relic_csrf" {
			token = ck.Value
		}
	}
	if token == "" {
		t.Fatalf("no csrf token from GET")
	}
	// Step 2: POST with session + matching cookie + header.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/orgs", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "relic_session", Value: "sess"})
	req.AddCookie(&http.Cookie{Name: "relic_csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST with valid csrf got %d, want 200", resp.StatusCode)
	}
}

func TestCSRF_MismatchRejected(t *testing.T) {
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/orgs", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "relic_session", Value: "sess"})
	req.AddCookie(&http.Cookie{Name: "relic_csrf", Value: "aaaa"})
	req.Header.Set("X-CSRF-Token", "bbbb")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("mismatch got %d, want 403", resp.StatusCode)
	}
}

func TestCSRF_NoSessionCookieSkipped(t *testing.T) {
	// Stateless callers without any session cookie shouldn't be
	// gated by CSRF — the protection only matters for browsers
	// that have an ambient session.
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/orgs", strings.NewReader(""))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no-cookie POST got %d, want 200 (CSRF should be skipped)", resp.StatusCode)
	}
}

func TestCSRF_SessionCookieWithoutCSRFRejected(t *testing.T) {
	// Browser case: there IS a session cookie, but no CSRF cookie
	// or header. Must 403.
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/orgs", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "relic_session", Value: "session-jwt"})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("session-cookie POST without csrf got %d, want 403", resp.StatusCode)
	}
}

func TestCSRF_BearerAPIKeyExempt(t *testing.T) {
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer rk_test123")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("API key POST got %d, want 200 (CSRF should be exempt)", resp.StatusCode)
	}
}

func TestCSRF_PathExempt(t *testing.T) {
	c := NewCSRF(false, "/v1/auth/oidc/callback")
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/oidc/callback", strings.NewReader(""))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("exempt path got %d, want 200", resp.StatusCode)
	}
}

func TestCSRF_GETSkipsValidation(t *testing.T) {
	c := NewCSRF(false)
	srv := httptest.NewServer(c.Middleware(okHandler()))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET got %d, want 200", resp.StatusCode)
	}
}
