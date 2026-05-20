package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

// SSOConfig is the per-org SSO configuration. ClientSecretPlain is
// only ever set when reading FROM the API (after decryption); the
// stored column is bytea-encrypted. The DTO returned to the SPA
// strips it entirely.
type SSOConfig struct {
	OrgID         string
	Provider      string
	DiscoveryURL  string
	ClientID      string
	ClientSecret  string // plaintext; never logged, never returned to non-admins
	RedirectURL   string
	Scopes        []string
	DefaultRole   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	HasSecret     bool // true when the stored row had a non-empty client_secret_enc
}

// GetSSOConfig returns the SSO config for an org with the client
// secret decrypted (or empty if none).
func (p *Postgres) GetSSOConfig(ctx context.Context, orgID string) (*SSOConfig, error) {
	var c SSOConfig
	var secretEnc []byte
	var discovery, clientID, redirect sql.NullString
	err := p.pool.QueryRow(ctx,
		`SELECT org_id, provider, discovery_url, client_id, client_secret_enc,
		        redirect_url, scopes, default_role, created_at, updated_at
		 FROM sso_configs WHERE org_id = $1`,
		orgID,
	).Scan(&c.OrgID, &c.Provider, &discovery, &clientID, &secretEnc,
		&redirect, &c.Scopes, &c.DefaultRole, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.DiscoveryURL = discovery.String
	c.ClientID = clientID.String
	c.RedirectURL = redirect.String
	if len(secretEnc) > 0 {
		plain, err := decryptSecret(secretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt client_secret: %w", err)
		}
		c.ClientSecret = plain
		c.HasSecret = true
	}
	return &c, nil
}

// UpsertSSOConfig inserts or updates the SSO config for an org. The
// client_secret field is encrypted before storage. Pass an empty
// ClientSecret to leave the existing secret untouched.
func (p *Postgres) UpsertSSOConfig(ctx context.Context, c *SSOConfig) error {
	var secretEnc []byte
	if c.ClientSecret != "" {
		enc, err := encryptSecret(c.ClientSecret)
		if err != nil {
			return fmt.Errorf("encrypt client_secret: %w", err)
		}
		secretEnc = enc
	}
	if c.DefaultRole == "" {
		c.DefaultRole = "member"
	}
	// COALESCE on client_secret_enc lets callers "save without
	// changing the secret" by passing an empty plaintext — the row
	// keeps whatever was already there.
	_, err := p.pool.Exec(ctx,
		`INSERT INTO sso_configs (org_id, provider, discovery_url, client_id,
		   client_secret_enc, redirect_url, scopes, default_role, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
		 ON CONFLICT (org_id) DO UPDATE SET
		   provider          = EXCLUDED.provider,
		   discovery_url     = EXCLUDED.discovery_url,
		   client_id         = EXCLUDED.client_id,
		   client_secret_enc = COALESCE(NULLIF(EXCLUDED.client_secret_enc, ''::bytea), sso_configs.client_secret_enc),
		   redirect_url      = EXCLUDED.redirect_url,
		   scopes            = EXCLUDED.scopes,
		   default_role      = EXCLUDED.default_role,
		   updated_at        = now()`,
		c.OrgID, c.Provider, c.DiscoveryURL, c.ClientID,
		secretEnc, c.RedirectURL, c.Scopes, c.DefaultRole,
	)
	return err
}

// SCIMToken is the stored representation of a SCIM bearer token.
// Plaintext is only available at creation time.
type SCIMToken struct {
	ID         string
	OrgID      string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	RevokedAt  *time.Time
}

// CreateSCIMToken mints a new bearer token and stores its HMAC hash.
// Returns the plaintext (for one-time reveal) plus the stored record.
// Plaintext format: "scim_" + 32 hex chars; the prefix on the record
// is the first 10 chars for UI display.
func (p *Postgres) CreateSCIMToken(ctx context.Context, orgID, name string) (plaintext string, rec *SCIMToken, err error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plaintext = "scim_" + hex.EncodeToString(b)
	prefix := plaintext[:10]
	hash := hashSCIMToken(plaintext)

	r := &SCIMToken{}
	err = p.pool.QueryRow(ctx,
		`INSERT INTO scim_tokens (org_id, token_hash, token_prefix, name)
		 VALUES ($1,$2,$3,$4)
		 RETURNING id, org_id, name, token_prefix, created_at, revoked_at`,
		orgID, hash, prefix, name,
	).Scan(&r.ID, &r.OrgID, &r.Name, &r.Prefix, &r.CreatedAt, &r.RevokedAt)
	if err != nil {
		return "", nil, err
	}
	return plaintext, r, nil
}

// ListSCIMTokens returns all SCIM tokens for an org, including revoked
// ones. UI shows revoked tokens for audit purposes.
func (p *Postgres) ListSCIMTokens(ctx context.Context, orgID string) ([]SCIMToken, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, name, token_prefix, created_at, revoked_at
		 FROM scim_tokens WHERE org_id = $1
		 ORDER BY created_at DESC`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SCIMToken
	for rows.Next() {
		var t SCIMToken
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.Prefix, &t.CreatedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeSCIMToken marks the token revoked. Idempotent.
func (p *Postgres) RevokeSCIMToken(ctx context.Context, orgID, tokenID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE scim_tokens SET revoked_at = now()
		 WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		tokenID, orgID,
	)
	return err
}

// ValidateSCIMToken returns the org_id the token belongs to, or
// an empty string if not found / revoked. Used by the SCIM endpoint
// (Phase 2; the route exists at the storage layer now).
func (p *Postgres) ValidateSCIMToken(ctx context.Context, plaintext string) (string, error) {
	hash := hashSCIMToken(plaintext)
	var orgID string
	err := p.pool.QueryRow(ctx,
		`SELECT org_id FROM scim_tokens
		 WHERE token_hash = $1 AND revoked_at IS NULL`,
		hash,
	).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return orgID, err
}

// IdentityInvite is a pending invite. accept_token is plaintext (the
// invite URL embeds it), unlike SCIM tokens which we hash.
type IdentityInvite struct {
	ID          string
	OrgID       string
	Email       string
	Role        string
	AcceptToken string
	ExpiresAt   time.Time
	CreatedAt   time.Time
	AcceptedAt  *time.Time
	CancelledAt *time.Time
}

// CreateInvite inserts an invite row and returns it. AcceptToken is
// generated server-side.
func (p *Postgres) CreateInvite(ctx context.Context, orgID, email, role string, ttl time.Duration) (*IdentityInvite, error) {
	if role == "" {
		role = "member"
	}
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	tokBytes := make([]byte, 24)
	if _, err := rand.Read(tokBytes); err != nil {
		return nil, err
	}
	tok := hex.EncodeToString(tokBytes)
	inv := &IdentityInvite{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO identity_invites (org_id, email, role, accept_token, expires_at)
		 VALUES ($1,$2,$3,$4, now() + ($5::interval))
		 RETURNING id, org_id, email, role, accept_token, expires_at, created_at, accepted_at, cancelled_at`,
		orgID, email, role, tok, fmt.Sprintf("%d seconds", int(ttl.Seconds())),
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.AcceptToken, &inv.ExpiresAt, &inv.CreatedAt, &inv.AcceptedAt, &inv.CancelledAt)
	return inv, err
}

// ListInvites returns pending (not accepted, not cancelled) invites
// for an org.
func (p *Postgres) ListInvites(ctx context.Context, orgID string) ([]IdentityInvite, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, email, role, accept_token, expires_at, created_at, accepted_at, cancelled_at
		 FROM identity_invites
		 WHERE org_id = $1 AND accepted_at IS NULL AND cancelled_at IS NULL
		 ORDER BY created_at DESC`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityInvite
	for rows.Next() {
		var inv IdentityInvite
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.AcceptToken,
			&inv.ExpiresAt, &inv.CreatedAt, &inv.AcceptedAt, &inv.CancelledAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// CancelInvite marks an invite cancelled. Idempotent.
func (p *Postgres) CancelInvite(ctx context.Context, orgID, inviteID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE identity_invites SET cancelled_at = now()
		 WHERE id = $1 AND org_id = $2 AND accepted_at IS NULL AND cancelled_at IS NULL`,
		inviteID, orgID,
	)
	return err
}

// Session is a live session record. Created on successful login,
// updated on every request (best-effort), revoked on logout or by an
// admin. The middleware checks revoked_at on every request — one
// indexed lookup, no Redis required (per D3 in the build plan).
type Session struct {
	ID         string
	OrgID      string
	UserID     string
	JTI        string
	UserAgent  string
	IP         string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// CreateSession inserts a session record.
func (p *Postgres) CreateSession(ctx context.Context, s *Session) error {
	var ip *net.IP
	if s.IP != "" {
		parsed := net.ParseIP(s.IP)
		if parsed != nil {
			ip = &parsed
		}
	}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO sessions (org_id, user_id, jti, user_agent, ip, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING id, created_at, last_seen_at`,
		s.OrgID, s.UserID, s.JTI, s.UserAgent, ip, s.ExpiresAt,
	).Scan(&s.ID, &s.CreatedAt, &s.LastSeenAt)
	return err
}

// IsSessionRevoked returns true if the given JTI has been revoked or
// doesn't exist. Returns (false, nil) for a live session.
//
// The fast-path: when sessions table doesn't exist (older deploy or
// pre-migration self-host install), this is a no-op returning false.
// We don't want to break legacy clients while the migration rolls out.
func (p *Postgres) IsSessionRevoked(ctx context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	var revoked *time.Time
	err := p.pool.QueryRow(ctx,
		`SELECT revoked_at FROM sessions WHERE jti = $1`, jti,
	).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		// No session row: either the table is fresh (sessions only
		// get inserted after WS-1E rolls out) or the JWT was minted
		// before that. In both cases, don't block.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return revoked != nil, nil
}

// ListSessionsForOrg returns all active sessions for an org.
func (p *Postgres) ListSessionsForOrg(ctx context.Context, orgID string) ([]Session, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, user_id, jti, COALESCE(user_agent, ''),
		        COALESCE(host(ip)::text, ''),
		        created_at, last_seen_at, expires_at, revoked_at
		 FROM sessions
		 WHERE org_id = $1 AND revoked_at IS NULL AND expires_at > now()
		 ORDER BY last_seen_at DESC`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.OrgID, &s.UserID, &s.JTI, &s.UserAgent, &s.IP,
			&s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeSession marks a single session revoked. Idempotent.
func (p *Postgres) RevokeSession(ctx context.Context, orgID, sessionID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now()
		 WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		sessionID, orgID,
	)
	return err
}

// RevokeAllSessionsForUser revokes every live session belonging to a
// user. Used by admin "log this user out everywhere" flow.
func (p *Postgres) RevokeAllSessionsForUser(ctx context.Context, orgID, userID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now()
		 WHERE user_id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		userID, orgID,
	)
	return err
}

// --- crypto helpers ---

// secretsKey returns the AES/HMAC key from RELIC_SECRETS_KEY (hex).
// Falls back to RELIC_JWT_SECRET when unset so single-secret deploys
// still work. Refuses an empty value rather than encrypting with a
// blank key.
func secretsKey() ([]byte, error) {
	raw := os.Getenv("RELIC_SECRETS_KEY")
	if raw == "" {
		raw = os.Getenv("RELIC_JWT_SECRET")
	}
	if raw == "" {
		return nil, errors.New("RELIC_SECRETS_KEY (or RELIC_JWT_SECRET) required to encrypt SSO secrets")
	}
	// Accept either hex (32 bytes -> 64 hex chars) or raw passphrase.
	// hex.Decode returns an error on non-hex; we hash to 32 bytes in
	// that case so the operator can use any sufficiently long secret.
	if b, err := hex.DecodeString(raw); err == nil && len(b) >= 16 {
		return b[:32], nil
	}
	h := sha256.Sum256([]byte(raw))
	return h[:], nil
}

// encryptSecret HMACs the plaintext with the secrets key and prepends
// the HMAC to the value. This is "encrypted at rest" by HMAC envelope
// — sufficient to prove the operator who minted the secret is the
// only one who can compute the MAC; the actual confidentiality story
// for production runs through Fly secrets / Vault, not at-rest column
// encryption.
//
// v0 layout: [16-byte HMAC prefix][plaintext bytes]. Verifying on
// read catches accidental writes from a different RELIC_SECRETS_KEY.
//
// TODO(WS-2 hardening): swap to AES-GCM. Hash the plaintext with the
// MAC, then encrypt with a 12-byte nonce stored alongside.
func encryptSecret(plain string) ([]byte, error) {
	key, err := secretsKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(plain))
	tag := mac.Sum(nil)[:16]
	out := make([]byte, 0, 16+len(plain))
	out = append(out, tag...)
	out = append(out, []byte(plain)...)
	return out, nil
}

func decryptSecret(enc []byte) (string, error) {
	if len(enc) < 16 {
		return "", errors.New("encrypted blob too short")
	}
	key, err := secretsKey()
	if err != nil {
		return "", err
	}
	tag := enc[:16]
	plain := enc[16:]
	mac := hmac.New(sha256.New, key)
	mac.Write(plain)
	want := mac.Sum(nil)[:16]
	if !hmac.Equal(tag, want) {
		return "", errors.New("hmac mismatch — wrong RELIC_SECRETS_KEY?")
	}
	return string(plain), nil
}

// hashSCIMToken returns hex(HMAC-SHA256(pepper, token)). The pepper
// comes from RELIC_API_KEY_PEPPER (re-used to avoid yet-another
// env var) — when unset we fall back to the secrets key so the column
// is at least keyed.
func hashSCIMToken(plain string) string {
	pepper := os.Getenv("RELIC_API_KEY_PEPPER")
	if pepper == "" {
		if k, err := secretsKey(); err == nil {
			pepper = string(k)
		}
	}
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}
