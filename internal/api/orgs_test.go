package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/therelicai/therelic-platform/internal/api/middleware"
)

// TestHandleCreateOrg_RejectsCallerWithExistingOrg pins F6: callers
// that already have an org context must not be able to create
// additional orgs through POST /v1/orgs or /v1/onboard. Without this
// guard, any authed user (or anyone with a stolen API key) could
// flood the orgs table or quietly stand up sibling tenants.
//
// We invoke the handler directly with a pre-populated context, which
// avoids exercising the auth middleware and the database. The 403
// path returns before ever touching s.db, so nil db is safe here.
func TestHandleCreateOrg_RejectsCallerWithExistingOrg(t *testing.T) {
	srv := &Server{} // db nil — guard returns before any db call

	body := strings.NewReader(`{"name":"sneaky","slug":"sneaky"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/orgs", body)
	req.Header.Set("Content-Type", "application/json")
	// Simulate the auth middleware having set the caller's org.
	ctx := context.WithValue(req.Context(), middleware.CtxOrgID, "existing-org-123")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	srv.handleCreateOrg(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already has an org") {
		t.Errorf("body=%s; expected explanatory error message", rec.Body.String())
	}
}

// TestHandleCreateOrg_RejectsOversizedBody pins the 4KB MaxBytesReader
// added alongside the F6 gate. A 16KB payload from an org-less caller
// (which would otherwise pass the gate) must still be refused so a
// callable-but-unauthorized client can't waste server time decoding
// arbitrarily-large junk.
func TestHandleCreateOrg_RejectsOversizedBody(t *testing.T) {
	srv := &Server{}
	big := `{"name":"` + strings.Repeat("A", 16*1024) + `","slug":"a"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/orgs", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	// No org in context — gate passes; body cap should still trip.

	rec := httptest.NewRecorder()
	srv.handleCreateOrg(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 for oversized body; body=%s", rec.Code, rec.Body.String())
	}
}
