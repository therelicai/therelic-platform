package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() {
	p.pool.Close()
}

// --- Organizations ---

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

func (p *Postgres) CreateOrg(ctx context.Context, name, slug string) (*Organization, error) {
	org := &Organization{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id, name, slug, plan, created_at`,
		name, slug,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.Plan, &org.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}
	return org, nil
}

func (p *Postgres) GetOrg(ctx context.Context, id string) (*Organization, error) {
	org := &Organization{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, name, slug, plan, created_at FROM organizations WHERE id = $1`, id,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.Plan, &org.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org: %w", err)
	}
	return org, nil
}

// --- API Keys ---

type APIKey struct {
	ID        string     `json:"id"`
	OrgID     string     `json:"org_id"`
	KeyPrefix string     `json:"key_prefix"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func (p *Postgres) CreateAPIKey(ctx context.Context, orgID, name string) (*APIKey, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate key: %w", err)
	}
	plaintext := "rk_" + hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(plaintext))
	keyHash := hex.EncodeToString(hash[:])
	prefix := plaintext[:10]

	key := &APIKey{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO api_keys (org_id, key_hash, key_prefix, name) VALUES ($1, $2, $3, $4)
		 RETURNING id, org_id, key_prefix, name, created_at`,
		orgID, keyHash, prefix, name,
	).Scan(&key.ID, &key.OrgID, &key.KeyPrefix, &key.Name, &key.CreatedAt)
	if err != nil {
		return nil, "", fmt.Errorf("create api key: %w", err)
	}
	return key, plaintext, nil
}

func (p *Postgres) ValidateAPIKey(ctx context.Context, plaintext string) (orgID string, err error) {
	hash := sha256.Sum256([]byte(plaintext))
	keyHash := hex.EncodeToString(hash[:])
	err = p.pool.QueryRow(ctx,
		`SELECT org_id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, keyHash,
	).Scan(&orgID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("invalid or revoked API key")
	}
	if err != nil {
		return "", fmt.Errorf("validate key: %w", err)
	}
	return orgID, nil
}

func (p *Postgres) RevokeAPIKey(ctx context.Context, orgID, keyID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND org_id = $2`, keyID, orgID,
	)
	return err
}

// --- Runs ---

type Run struct {
	ID             string    `json:"id"`
	OrgID          string    `json:"org_id"`
	AgentName      string    `json:"agent_name"`
	AgentVersion   string    `json:"agent_version"`
	PolicyHash     string    `json:"policy_hash"`
	Environment    string    `json:"environment"`
	StartedAt      time.Time `json:"started_at"`
	DurationMs     *int      `json:"duration_ms,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	ActionsTotal   int       `json:"actions_total"`
	ActionsAllowed int       `json:"actions_allowed"`
	ActionsDenied  int       `json:"actions_denied"`
	StorageKey     string    `json:"storage_key"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (p *Postgres) InsertRun(ctx context.Context, r *Run) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO runs (id, org_id, agent_name, agent_version, policy_hash, environment,
		  started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
		  storage_key, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		r.ID, r.OrgID, r.AgentName, r.AgentVersion, r.PolicyHash, r.Environment,
		r.StartedAt, r.DurationMs, r.ExitCode, r.ActionsTotal, r.ActionsAllowed, r.ActionsDenied,
		r.StorageKey, r.ExpiresAt,
	)
	return err
}

func (p *Postgres) ListRuns(ctx context.Context, orgID string, agentName string, limit, offset int) ([]Run, error) {
	query := `SELECT id, org_id, agent_name, agent_version, policy_hash, environment,
	           started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
	           storage_key, expires_at
	          FROM runs WHERE org_id = $1`
	args := []any{orgID}
	argIdx := 2

	if agentName != "" {
		query += fmt.Sprintf(" AND agent_name = $%d", argIdx)
		args = append(args, agentName)
		argIdx++
	}
	query += " ORDER BY started_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.OrgID, &r.AgentName, &r.AgentVersion, &r.PolicyHash,
			&r.Environment, &r.StartedAt, &r.DurationMs, &r.ExitCode,
			&r.ActionsTotal, &r.ActionsAllowed, &r.ActionsDenied, &r.StorageKey, &r.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func (p *Postgres) GetRun(ctx context.Context, orgID, runID string) (*Run, error) {
	r := &Run{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, agent_name, agent_version, policy_hash, environment,
		  started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
		  storage_key, expires_at
		 FROM runs WHERE org_id = $1 AND id = $2`, orgID, runID,
	).Scan(&r.ID, &r.OrgID, &r.AgentName, &r.AgentVersion, &r.PolicyHash,
		&r.Environment, &r.StartedAt, &r.DurationMs, &r.ExitCode,
		&r.ActionsTotal, &r.ActionsAllowed, &r.ActionsDenied, &r.StorageKey, &r.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return r, nil
}

func (p *Postgres) DeleteRun(ctx context.Context, orgID, runID string) (storageKey string, err error) {
	err = p.pool.QueryRow(ctx,
		`DELETE FROM runs WHERE org_id = $1 AND id = $2 RETURNING storage_key`, orgID, runID,
	).Scan(&storageKey)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return storageKey, err
}

// --- Agents ---

type Agent struct {
	ID               string    `json:"id"`
	OrgID            string    `json:"org_id"`
	Name             string    `json:"name"`
	Version          string    `json:"version"`
	IdentityManifest []byte    `json:"identity_manifest"`
	CapabilitiesHash string    `json:"capabilities_hash"`
	PolicyHash       string    `json:"policy_hash"`
	RegisteredAt     time.Time `json:"registered_at"`
	LastSeen         time.Time `json:"last_seen"`
}

type AgentBaseline struct {
	AgentID          string    `json:"agent_id"`
	ComputedAt       time.Time `json:"computed_at"`
	WindowDays       int       `json:"window_days"`
	AvgActionsPerRun float64   `json:"avg_actions_per_run"`
	AvgDenialsPerRun float64   `json:"avg_denials_per_run"`
	ToolDistribution []byte    `json:"tool_distribution"`
}

type AuditEvent struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	UserID     string    `json:"user_id"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	ResourceID string    `json:"resource_id"`
	Metadata   []byte    `json:"metadata"`
	CreatedAt  time.Time `json:"created_at"`
}

func (p *Postgres) UpsertAgent(ctx context.Context, a *Agent) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO agents (org_id, name, version, identity_manifest, capabilities_hash, policy_hash)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (org_id, name) DO UPDATE SET
		   version = EXCLUDED.version,
		   identity_manifest = EXCLUDED.identity_manifest,
		   capabilities_hash = EXCLUDED.capabilities_hash,
		   policy_hash = EXCLUDED.policy_hash,
		   last_seen = now()`,
		a.OrgID, a.Name, a.Version, a.IdentityManifest, a.CapabilitiesHash, a.PolicyHash,
	)
	return err
}

func (p *Postgres) ListAgents(ctx context.Context, orgID string) ([]Agent, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, name, version, identity_manifest, capabilities_hash, policy_hash, registered_at, last_seen
		 FROM agents WHERE org_id = $1 ORDER BY name`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
			&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, nil
}

func (p *Postgres) GetAgent(ctx context.Context, orgID, name string) (*Agent, error) {
	a := &Agent{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, name, version, identity_manifest, capabilities_hash, policy_hash, registered_at, last_seen
		 FROM agents WHERE org_id = $1 AND name = $2`, orgID, name,
	).Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
		&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (p *Postgres) GetAgentPolicy(ctx context.Context, orgID, agentName string) (string, error) {
	var policyYAML string
	err := p.pool.QueryRow(ctx,
		`SELECT COALESCE(policy_yaml, '') FROM agents WHERE org_id = $1 AND name = $2`, orgID, agentName,
	).Scan(&policyYAML)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get agent policy: %w", err)
	}
	return policyYAML, nil
}

func (p *Postgres) UpdateAgentPolicy(ctx context.Context, orgID, agentName, policyYAML string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET policy_yaml = $1, policy_updated_at = now() WHERE org_id = $2 AND name = $3`,
		policyYAML, orgID, agentName,
	)
	if err != nil {
		return fmt.Errorf("update agent policy: %w", err)
	}
	return nil
}

func (p *Postgres) GetAgentBaseline(ctx context.Context, agentID string) (*AgentBaseline, error) {
	b := &AgentBaseline{}
	err := p.pool.QueryRow(ctx,
		`SELECT agent_id, computed_at, window_days, avg_actions_per_run, avg_denials_per_run, tool_distribution
		 FROM agent_baselines WHERE agent_id = $1 ORDER BY computed_at DESC LIMIT 1`, agentID,
	).Scan(&b.AgentID, &b.ComputedAt, &b.WindowDays, &b.AvgActionsPerRun, &b.AvgDenialsPerRun, &b.ToolDistribution)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent baseline: %w", err)
	}
	return b, nil
}

func (p *Postgres) InsertBaseline(ctx context.Context, baseline *AgentBaseline) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO agent_baselines (agent_id, computed_at, window_days, avg_actions_per_run, avg_denials_per_run, tool_distribution)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		baseline.AgentID, baseline.ComputedAt, baseline.WindowDays,
		baseline.AvgActionsPerRun, baseline.AvgDenialsPerRun, baseline.ToolDistribution,
	)
	if err != nil {
		return fmt.Errorf("insert baseline: %w", err)
	}
	return nil
}

func (p *Postgres) ComputeBaseline(ctx context.Context, agentID string, windowDays int) (*AgentBaseline, error) {
	agent, err := p.GetAgentByID(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("agent not found")
	}

	var avgActions, avgDenials float64
	var runCount int
	err = p.pool.QueryRow(ctx,
		`SELECT COALESCE(AVG(actions_total::float), 0), COALESCE(AVG(actions_denied::float), 0), COUNT(*)
		 FROM runs
		 WHERE org_id = $1 AND agent_name = $2
		   AND started_at >= now() - ($3 * interval '1 day')`,
		agent.OrgID, agent.Name, windowDays,
	).Scan(&avgActions, &avgDenials, &runCount)
	if err != nil {
		return nil, fmt.Errorf("compute baseline: %w", err)
	}

	emptyToolDist, _ := json.Marshal(map[string]any{})
	baseline := &AgentBaseline{
		AgentID:          agentID,
		ComputedAt:       time.Now(),
		WindowDays:       windowDays,
		AvgActionsPerRun: avgActions,
		AvgDenialsPerRun: avgDenials,
		ToolDistribution: emptyToolDist,
	}
	return baseline, nil
}

func (p *Postgres) GetAgentByID(ctx context.Context, agentID string) (*Agent, error) {
	a := &Agent{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, name, version, identity_manifest, capabilities_hash, policy_hash, registered_at, last_seen
		 FROM agents WHERE id = $1`, agentID,
	).Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
		&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent by id: %w", err)
	}
	return a, nil
}

func (p *Postgres) InsertAuditEvent(ctx context.Context, orgID, userID, action, resource, resourceID string, metadata []byte) error {
	if metadata == nil {
		metadata = []byte("{}")
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO audit_events (org_id, user_id, action, resource, resource_id, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		orgID, userID, action, resource, resourceID, metadata,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func (p *Postgres) ListAuditEvents(ctx context.Context, orgID string, limit, offset int) ([]AuditEvent, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, user_id, action, resource, resource_id, metadata, created_at
		 FROM audit_events WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		orgID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.OrgID, &e.UserID, &e.Action, &e.Resource, &e.ResourceID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}

func (p *Postgres) ListOrgs(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM organizations ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()

	var orgIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan org id: %w", err)
		}
		orgIDs = append(orgIDs, id)
	}
	return orgIDs, nil
}

func (p *Postgres) GetUserByID(ctx context.Context, userID string) (*User, error) {
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, email, role, created_at FROM users WHERE id = $1`, userID,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// --- Proposals ---

type Proposal struct {
	ID           string     `json:"id"`
	OrgID        string     `json:"org_id"`
	AgentName    string     `json:"agent_name"`
	Status       string     `json:"status"`
	TriggerType  string     `json:"trigger_type"`
	Evidence     []byte     `json:"evidence"`
	ProposedRule []byte     `json:"proposed_rule"`
	CreatedAt    time.Time  `json:"created_at"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	DecidedBy    *string    `json:"decided_by,omitempty"`
}

func (p *Postgres) InsertProposal(ctx context.Context, pr *Proposal) error {
	pr.ID = ulid.Make().String()
	_, err := p.pool.Exec(ctx,
		`INSERT INTO proposals (id, org_id, agent_name, status, trigger_type, evidence, proposed_rule)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		pr.ID, pr.OrgID, pr.AgentName, "pending", pr.TriggerType, pr.Evidence, pr.ProposedRule,
	)
	return err
}

func (p *Postgres) ListProposals(ctx context.Context, orgID, status string) ([]Proposal, error) {
	query := `SELECT id, org_id, agent_name, status, trigger_type, evidence, proposed_rule, created_at, decided_at, decided_by
	          FROM proposals WHERE org_id = $1`
	args := []any{orgID}
	if status != "" {
		query += " AND status = $2"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC"

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var proposals []Proposal
	for rows.Next() {
		var pr Proposal
		if err := rows.Scan(&pr.ID, &pr.OrgID, &pr.AgentName, &pr.Status, &pr.TriggerType,
			&pr.Evidence, &pr.ProposedRule, &pr.CreatedAt, &pr.DecidedAt, &pr.DecidedBy); err != nil {
			return nil, err
		}
		proposals = append(proposals, pr)
	}
	return proposals, nil
}

func (p *Postgres) UpdateProposalStatus(ctx context.Context, orgID, proposalID, status, userID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE proposals SET status = $1, decided_at = now(), decided_by = $2
		 WHERE id = $3 AND org_id = $4`,
		status, userID, proposalID, orgID,
	)
	return err
}

// --- Users ---

type User struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func (p *Postgres) CreateUser(ctx context.Context, orgID, email, role string) (*User, error) {
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO users (org_id, email, role) VALUES ($1, $2, $3)
		 RETURNING id, org_id, email, role, created_at`,
		orgID, email, role,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt)
	return u, err
}

func (p *Postgres) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, email, role, created_at FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return u, err
}
