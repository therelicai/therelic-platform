package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders_AllSeven(t *testing.T) {
	srv := httptest.NewTLSServer(SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	client := srv.Client()

	resp, err := client.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	wantHeaders := []string{
		"Strict-Transport-Security",
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Permissions-Policy",
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Resource-Policy",
	}
	for _, h := range wantHeaders {
		if got := resp.Header.Get(h); got == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

func TestSecurityHeaders_HSTSOnlyOverHTTPS(t *testing.T) {
	srv := httptest.NewServer(SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("HSTS set on plain HTTP response: %s", hsts)
	}
	// The other headers must still be present so a proxy stripping
	// TLS doesn't get a worse posture.
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options not set on HTTP")
	}
}

func TestSecurityHeaders_HSTSViaForwardedProto(t *testing.T) {
	srv := httptest.NewServer(SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts == "" {
		t.Errorf("HSTS missing when X-Forwarded-Proto=https")
	}
}
