CREATE TABLE IF NOT EXISTS episodic_summaries (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id     VARCHAR(255) NOT NULL DEFAULT '',
    session_key TEXT NOT NULL,

    summary      TEXT NOT NULL,
    l0_abstract  TEXT NOT NULL DEFAULT '',
    key_topics   TEXT[] NOT NULL DEFAULT '{}',
    embedding    vector(1536),
    source_type  TEXT NOT NULL DEFAULT 'session',
    source_id    TEXT,
    turn_count   INT NOT NULL DEFAULT 0,
    token_count  INT NOT NULL DEFAULT 0,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_episodic_agent_user
    ON episodic_summaries(agent_id, user_id);

CREATE INDEX IF NOT EXISTS idx_episodic_tenant
    ON episodic_summaries(tenant_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_episodic_source_dedup
    ON episodic_summaries(agent_id, user_id, source_id)
    WHERE source_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_episodic_expires
    ON episodic_summaries(expires_at)
    WHERE expires_at IS NOT NULL;
