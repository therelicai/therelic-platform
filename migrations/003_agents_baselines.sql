-- 003: Agent Registry and Behavioral Baselines

CREATE TABLE agents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    version             TEXT NOT NULL DEFAULT '',
    identity_manifest   JSONB NOT NULL DEFAULT '{}',
    capabilities_hash   TEXT NOT NULL DEFAULT '',
    policy_hash         TEXT NOT NULL DEFAULT '',
    registered_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, name)
);

CREATE TABLE agent_baselines (
    agent_id            UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    computed_at         TIMESTAMPTZ NOT NULL,
    window_days         INTEGER NOT NULL DEFAULT 30,
    avg_actions_per_run NUMERIC,
    avg_denials_per_run NUMERIC,
    tool_distribution   JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY (agent_id, computed_at)
);
