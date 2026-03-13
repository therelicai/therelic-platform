-- 004: Governance Proposals

CREATE TABLE proposals (
    id            TEXT PRIMARY KEY,
    org_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    agent_name    TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','dismissed')),
    trigger_type  TEXT NOT NULL DEFAULT 'denial_pattern',
    evidence      JSONB NOT NULL DEFAULT '{}',
    proposed_rule JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at    TIMESTAMPTZ,
    decided_by    TEXT
);

CREATE INDEX idx_proposals_org_status ON proposals(org_id, status);
CREATE INDEX idx_proposals_agent ON proposals(org_id, agent_name);
