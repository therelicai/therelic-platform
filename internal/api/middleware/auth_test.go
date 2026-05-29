package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/therelicai/therelic-platform/internal/auth"
)

// fakeProvider lets us exercise the JWT branch of Auth.Middleware
// without standing up Postgres or signing real tokens.
type fakeProvider struct {
	mode   auth.Mode
	claims auth.Claims
	err    error
}

func (f *fakeProvider) Mode() auth.Mode { return f.mode }
func (f *fakeProvider) Verify(_ context.Context, _ string) (auth.Claims, error) {
	return f.claims, f.err
}
func (f *fakeProvider) IssueToken(_ context.Context, _ auth.Claims) (string, error) {
	return "", auth.ErrIssueUnsupported
}

func nextSpy(seen *bool, capture *http.Request) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = true
		if capture != nil {
			*capture = *r
		}
		w.WriteHeader(http.StatusOK)
	})
}

// TestAuth_MissingHeader: the request never reaches the inner handler.
func TestAuth_MissingHeader(t *testing.T) {
	a := &Auth{db: nil, provider: &fakeProvider{mode: auth.ModeLocal}}
	called := false
	srv := httptest.NewServer(a.Middleware(nextSpy(&called, nil)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
	if called {
		t.Error("inner handler called despite missing Authorization header")
	}
}

// TestAuth_MalformedHeader: not Bearer, or missing token half.
func TestAuth_MalformedHeader(t *testing.T) {
	a := &Auth{db: nil, provider: &fakeProvider{mode: auth.ModeLocal}}
	called := false
	h := a.Middleware(nextSpy(&called, nil))

	cases := []string{
		"Basic abc",
		"Bearer",  // single token
		"justhex", // no scheme
	}
	for _, hdr := range cases {
		called = false
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", hdr)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q: got %d want 401", hdr, rr.Code)
		}
		if called {
			t.Errorf("Authorization=%q: inner handler called", hdr)
		}
	}
}

// TestAuth_InvalidJWT: Verify returns ErrInvalidToken; middleware must
// 401 and NOT propagate to the inner handler. This pins the behavior of
// the err-handling branch in auth.go so the redundant `|| err != nil`
// can be safely simplified later without regressing.
func TestAuth_InvalidJWT(t *testing.T) {
	a := &Auth{db: nil, provider: &fakeProvider{
		mode: auth.ModeLocal,
		err:  auth.ErrInvalidToken,
	}}
	called := false
	srv := httptest.NewServer(a.Middleware(nextSpy(&called, nil)))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/x", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid credentials") {
		t.Errorf("body should use uniform 'invalid credentials' message, got %q", body)
	}
	if called {
		t.Error("inner handler invoked despite invalid token")
	}
}

// TestAuth_InvalidJWT_NonErrInvalidToken: an unexpected error class
// from Verify (e.g. transient JWKS lookup failure) must STILL stop the
// request. This is the case the redundant `|| err != nil` is doing
// real work for today. Test pins this so a simplification of the
// branch preserves the invariant.
func TestAuth_InvalidJWT_NonErrInvalidToken(t *testing.T) {
	a := &Auth{db: nil, provider: &fakeProvider{
		mode: auth.ModeLocal,
		err:  errors.New("jwks fetch failed"),
	}}
	called := false
	captured := http.Request{}
	srv := httptest.NewServer(a.Middleware(nextSpy(&called, &captured)))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/x", nil)
	req.Header.Set("Authorization", "Bearer something")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401 for non-ErrInvalidToken error", resp.StatusCode)
	}
	if called {
		t.Fatal("inner handler invoked with a non-ErrInvalidToken auth failure — middleware fail-open!")
	}
}

// TestAuth_ValidJWT_PassesClaimsToContext: happy path. Verifies
// OrgID + UserID are placed in the request context for downstream
// handlers to call OrgIDFromContext / UserIDFromContext.
func TestAuth_ValidJWT_PassesClaimsToContext(t *testing.T) {
	a := &Auth{db: nil, provider: &fakeProvider{
		mode: auth.ModeLocal,
		claims: auth.Claims{
			UserID: "user-1",
			OrgID:  "org-1",
			Email:  "ada@example.com",
		},
	}}
	var gotOrg, gotUser string
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg = OrgIDFromContext(r.Context())
		gotUser = UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
	if gotOrg != "org-1" {
		t.Errorf("OrgIDFromContext = %q, want org-1", gotOrg)
	}
	if gotUser != "user-1" {
		t.Errorf("UserIDFromContext = %q, want user-1", gotUser)
	}
}

// TestAuth_RejectsLowerCaseBearer_No: RFC 7235 says Bearer is case-
// insensitive; the middleware uses EqualFold so "bearer x" must work.
func TestAuth_BearerCaseInsensitive(t *testing.T) {
	a := &Auth{db: nil, provider: &fakeProvider{
		mode:   auth.ModeLocal,
		claims: auth.Claims{OrgID: "o"},
	}}
	called := false
	h := a.Middleware(nextSpy(&called, nil))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "bearer abc")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("lower-case bearer rejected: got %d want 200", rr.Code)
	}
	if !called {
		t.Error("inner handler not reached for valid lower-case bearer")
	}
}
