package api_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/therelicai/therelic-platform/internal/api"
)

// setupTestServer creates a test server with nil db/s3 for testing endpoints
// that don't require database access (health, CORS, auth rejection).
func setupTestServer(t *testing.T) http.Handler {
	t.Helper()
	srv := api.NewServer(nil, nil, "test-jwt-secret", slog.Default())
	return srv.Router()
}

func TestHealthEndpoint(t *testing.T) {
	handler := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health: want status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body != `{"status":"ok"}` {
		t.Errorf("GET /health: want body %q, got %q", `{"status":"ok"}`, body)
	}
}

func TestCORSHeaders(t *testing.T) {
	handler := setupTestServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// CORS middleware should add these headers on OPTIONS
	if v := rec.Header().Get("Access-Control-Allow-Origin"); v == "" {
		t.Error("OPTIONS: expected Access-Control-Allow-Origin header")
	}
	if v := rec.Header().Get("Access-Control-Allow-Methods"); v == "" {
		t.Error("OPTIONS: expected Access-Control-Allow-Methods header")
	}
}

func TestUnauthorized(t *testing.T) {
	handler := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/traces", nil)
	// No Authorization header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/traces without auth: want status 401, got %d", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	handler := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: want 200, got %d", rec.Code)
	}
	// /metrics should contain at least one of our namespaced metrics.
	if !strings.Contains(rec.Body.String(), "relic_api_") {
		t.Errorf("GET /metrics: body should contain relic_api_ metrics, got:\n%s", rec.Body.String())
	}
}

func TestRequestIDHeader(t *testing.T) {
	handler := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Error("expected X-Request-ID response header to be set")
	}
}
