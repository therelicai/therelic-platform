-- 002: Runs (trace metadata index)

CREATE TABLE runs (
    id              TEXT NOT NULL,
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    agent_name      TEXT NOT NULL,
    agent_version   TEXT NOT NULL DEFAULT '',
    policy_hash     TEXT NOT NULL DEFAULT '',
    environment     TEXT NOT NULL DEFAULT 'default',
    started_at      TIMESTAMPTZ NOT NULL,
    duration_ms     INTEGER,
    exit_code       INTEGER,
    actions_total   INTEGER NOT NULL DEFAULT 0,
    actions_allowed INTEGER NOT NULL DEFAULT 0,
    actions_denied  INTEGER NOT NULL DEFAULT 0,
    storage_key     TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, id)
);

CREATE INDEX idx_runs_lookup ON runs(org_id, agent_name, started_at DESC);
CREATE INDEX idx_runs_expiry ON runs(expires_at) WHERE expires_at IS NOT NULL;
