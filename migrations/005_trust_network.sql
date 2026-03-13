-- 005: Trust Network (Marketplace)

CREATE TABLE capability_listings (
    agent_id    TEXT PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    endpoint    TEXT NOT NULL,
    tools       JSONB NOT NULL DEFAULT '[]',
    trust_score NUMERIC(3,2) NOT NULL DEFAULT 0.50,
    pricing     JSONB NOT NULL DEFAULT '{"model":"free"}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_capability_org ON capability_listings(org_id);
CREATE INDEX idx_capability_search ON capability_listings USING gin(tools);

CREATE TABLE bilateral_agreements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    caller_org_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    provider_org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    caller_agent    TEXT NOT NULL,
    provider_agent  TEXT NOT NULL,
    caller_policy   JSONB NOT NULL DEFAULT '{}',
    provider_policy JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','revoked')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at     TIMESTAMPTZ
);

CREATE TABLE transactions (
    id              TEXT PRIMARY KEY,
    corr            TEXT NOT NULL,
    caller_org_id   UUID NOT NULL REFERENCES organizations(id),
    provider_org_id UUID NOT NULL REFERENCES organizations(id),
    caller_agent    TEXT NOT NULL,
    provider_agent  TEXT NOT NULL,
    tool            TEXT NOT NULL,
    duration_ms     INTEGER,
    price           NUMERIC(10,4),
    status          TEXT NOT NULL DEFAULT 'completed' CHECK (status IN ('completed','failed','disputed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transactions_caller ON transactions(caller_org_id, created_at DESC);
CREATE INDEX idx_transactions_provider ON transactions(provider_org_id, created_at DESC);
