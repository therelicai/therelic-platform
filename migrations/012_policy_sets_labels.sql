-- 012: Policy sets, agent labels, applied-state tracking (Slice 15)
--
-- A policy_set is the new editing unit: a YAML body + a selector that
-- resolves to an agent set. When the set is written, the platform
-- publishes a notification on the agent-facing SSE channel for every
-- matched agent; each agent pulls the policy, hot-reloads, and reports
-- back via POST /v1/agents/:name/policy_applied. The dashboard reads
-- applied_policy_hash + applied_at off the agents row to render
-- "47/52 agents on hash abc123, 5 stale".
--
-- The selector grammar (RELIC.md §selector contract) supports two
-- arms: { "agent_name": "..." } (slice 13 single-agent) and
-- { "match": { "env": "prod", ... } } (slice 15 label-match, AND
-- across keys, equality on values). Both shapes are stored as JSONB
-- so future selector arms slot in without a migration.

CREATE TABLE policy_sets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    selector        JSONB NOT NULL,
    policy_yaml     TEXT NOT NULL,
    policy_hash     TEXT NOT NULL,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, name)
);

CREATE INDEX idx_policy_sets_org ON policy_sets (org_id);

CREATE TABLE agent_labels (
    agent_id        UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    value           TEXT NOT NULL,
    PRIMARY KEY (agent_id, key)
);

-- Index on (key, value) accelerates the platform's selector resolver
-- when matching { "match": { "env": "prod" } } across many agents.
-- The shape is small (most orgs have <20 labels per agent) so a
-- btree index is sufficient.
CREATE INDEX idx_agent_labels_key_value ON agent_labels (key, value);

-- applied_policy_hash captures the hash the agent most-recently
-- confirmed via POST /v1/agents/:name/policy_applied. applied_at is
-- the wall-clock time of that confirmation. Both are NULL for agents
-- that have never reported; the dashboard renders those as "stale".
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS applied_policy_hash TEXT,
    ADD COLUMN IF NOT EXISTS applied_at          TIMESTAMPTZ;
