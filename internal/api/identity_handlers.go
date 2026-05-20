package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
	"github.com/therelicai/therelic-platform/internal/storage"
)

// Identity HTTP handlers for the /settings/identity SPA page.
//
// All endpoints require an authenticated user whose org matches the
// :orgID path param. We don't have role-based authorization yet (the
// only checks today are "member of the org or not"), so any member
// can call these. Phase 2 adds role gates.
//
// Audit logging is mandatory per the cross-cutting concerns in the
// build plan: every mutation records an audit_events row.

// --- DTOs ---

type ssoConfigDTO struct {
	Provider     string   `json:"provider"`
	DiscoveryURL string   `json:"discovery_url,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	HasSecret    bool     `json:"has_secret"`
	RedirectURL  string   `json:"redirect_url,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	DefaultRole  string   `json:"default_role,omitempty"`
}

type ssoConfigInput struct {
	Provider     string   `json:"provider"`
	DiscoveryURL string   `json:"discovery_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"` // empty = leave existing untouched
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
	DefaultRole  string   `json:"default_role"`
}

type scimTokenDTO struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type scimTokenCreateInput struct {
	Name string `json:"name"`
}

type scimTokenCreateResponse struct {
	scimTokenDTO
	Plaintext string `json:"token"` // one-time reveal
}

type inviteDTO struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	AcceptToken string    `json:"accept_token,omitempty"` // returned only on create
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type inviteCreateInput struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type sessionDTO struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	UserAgent  string    `json:"user_agent,omitempty"`
	IP         string    `json:"ip,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// --- handlers ---

func (s *Server) handleGetSSO(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	cfg, err := s.db.GetSSOConfig(r.Context(), orgID)
	if err != nil {
		s.logger.Error("get sso config", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		writeJSON(w, http.StatusOK, ssoConfigDTO{})
		return
	}
	writeJSON(w, http.StatusOK, ssoConfigDTO{
		Provider:     cfg.Provider,
		DiscoveryURL: cfg.DiscoveryURL,
		ClientID:     cfg.ClientID,
		HasSecret:    cfg.HasSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
		DefaultRole:  cfg.DefaultRole,
	})
}

func (s *Server) handlePutSSO(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	var in ssoConfigInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if in.Provider == "" {
		writeJSONError(w, http.StatusBadRequest, "provider is required")
		return
	}
	cfg := &storage.SSOConfig{
		OrgID:        orgID,
		Provider:     in.Provider,
		DiscoveryURL: in.DiscoveryURL,
		ClientID:     in.ClientID,
		ClientSecret: in.ClientSecret,
		RedirectURL:  in.RedirectURL,
		Scopes:       in.Scopes,
		DefaultRole:  in.DefaultRole,
	}
	if err := s.db.UpsertSSOConfig(r.Context(), cfg); err != nil {
		s.logger.Error("upsert sso config", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditLog(r.Context(), auditSSOUpdate, "sso_config", orgID, nil)
	writeJSON(w, http.StatusOK, ssoConfigDTO{
		Provider:     cfg.Provider,
		DiscoveryURL: cfg.DiscoveryURL,
		ClientID:     cfg.ClientID,
		HasSecret:    in.ClientSecret != "" || cfg.HasSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
		DefaultRole:  cfg.DefaultRole,
	})
}

func (s *Server) handleListSCIMTokens(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	toks, err := s.db.ListSCIMTokens(r.Context(), orgID)
	if err != nil {
		s.logger.Error("list scim tokens", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]scimTokenDTO, 0, len(toks))
	for _, t := range toks {
		out = append(out, scimTokenDTO{
			ID: t.ID, Name: t.Name, Prefix: t.Prefix,
			CreatedAt: t.CreatedAt, RevokedAt: t.RevokedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateSCIMToken(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	var in scimTokenCreateInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	plain, rec, err := s.db.CreateSCIMToken(r.Context(), orgID, in.Name)
	if err != nil {
		s.logger.Error("create scim token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditLog(r.Context(), auditSCIMTokenCreate, "scim_token", rec.ID, nil)
	writeJSON(w, http.StatusCreated, scimTokenCreateResponse{
		scimTokenDTO: scimTokenDTO{
			ID: rec.ID, Name: rec.Name, Prefix: rec.Prefix, CreatedAt: rec.CreatedAt,
		},
		Plaintext: plain,
	})
}

func (s *Server) handleRevokeSCIMToken(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	tokID := chi.URLParam(r, "tokenID")
	if err := s.db.RevokeSCIMToken(r.Context(), orgID, tokID); err != nil {
		s.logger.Error("revoke scim token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditLog(r.Context(), auditSCIMTokenRevoke, "scim_token", tokID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	invs, err := s.db.ListInvites(r.Context(), orgID)
	if err != nil {
		s.logger.Error("list invites", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]inviteDTO, 0, len(invs))
	for _, inv := range invs {
		out = append(out, inviteDTO{
			ID: inv.ID, Email: inv.Email, Role: inv.Role,
			ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	var in inviteCreateInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Email == "" {
		writeJSONError(w, http.StatusBadRequest, "email is required")
		return
	}
	if in.Role == "" {
		in.Role = "member"
	}
	inv, err := s.db.CreateInvite(r.Context(), orgID, in.Email, in.Role, 0)
	if err != nil {
		s.logger.Error("create invite", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditLog(r.Context(), auditInviteCreate, "invite", inv.ID, nil)
	writeJSON(w, http.StatusCreated, inviteDTO{
		ID: inv.ID, Email: inv.Email, Role: inv.Role,
		AcceptToken: inv.AcceptToken,
		ExpiresAt:   inv.ExpiresAt, CreatedAt: inv.CreatedAt,
	})
}

func (s *Server) handleCancelInvite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	inviteID := chi.URLParam(r, "inviteID")
	if err := s.db.CancelInvite(r.Context(), orgID, inviteID); err != nil {
		s.logger.Error("cancel invite", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditLog(r.Context(), auditInviteCancel, "invite", inviteID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	sess, err := s.db.ListSessionsForOrg(r.Context(), orgID)
	if err != nil {
		s.logger.Error("list sessions", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]sessionDTO, 0, len(sess))
	for _, ss := range sess {
		out = append(out, sessionDTO{
			ID: ss.ID, UserID: ss.UserID,
			UserAgent: ss.UserAgent, IP: ss.IP,
			CreatedAt: ss.CreatedAt, LastSeenAt: ss.LastSeenAt, ExpiresAt: ss.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireMatchingOrg(w, r)
	if !ok {
		return
	}
	// Two shapes: DELETE /sessions/:sid OR DELETE /sessions?user=:uid
	if sid := chi.URLParam(r, "sessionID"); sid != "" {
		if err := s.db.RevokeSession(r.Context(), orgID, sid); err != nil {
			s.logger.Error("revoke session", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		s.auditLog(r.Context(), auditSessionRevoke, "session", sid, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	userID := r.URL.Query().Get("user")
	if userID == "" {
		writeJSONError(w, http.StatusBadRequest, "session id or ?user= required")
		return
	}
	if err := s.db.RevokeAllSessionsForUser(r.Context(), orgID, userID); err != nil {
		s.logger.Error("revoke sessions for user", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditLog(r.Context(), auditSessionRevokeAll, "user", userID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// requireMatchingOrg pulls :orgID from the URL and verifies the
// caller's session/key is for that same org. Returns the orgID + true
// on success, "" + false (writing an error response) on failure.
func (s *Server) requireMatchingOrg(w http.ResponseWriter, r *http.Request) (string, bool) {
	pathOrg := chi.URLParam(r, "orgID")
	ctxOrg := middleware.OrgIDFromContext(r.Context())
	if ctxOrg == "" {
		writeJSONError(w, http.StatusUnauthorized, "no org in context")
		return "", false
	}
	if pathOrg != "" && pathOrg != ctxOrg {
		writeJSONError(w, http.StatusForbidden, "cross-org access denied")
		return "", false
	}
	return ctxOrg, true
}
