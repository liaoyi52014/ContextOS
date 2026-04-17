CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    action VARCHAR(64) NOT NULL,
    target_type VARCHAR(32) NOT NULL,
    target_id VARCHAR(128),
    detail JSONB NOT NULL DEFAULT '{}',
    trace_id VARCHAR(64)
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);
CREATE INDEX IF NOT EXISTS idx_audit_tenant ON audit_logs(tenant_id, user_id);

CREATE TABLE IF NOT EXISTS token_usage (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    call_type VARCHAR(32) NOT NULL,
    model VARCHAR(128) NOT NULL,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    total_tokens INT NOT NULL DEFAULT 0,
    session_id VARCHAR(64),
    trace_id VARCHAR(64)
);
CREATE INDEX IF NOT EXISTS idx_token_usage_ts ON token_usage(ts);
CREATE INDEX IF NOT EXISTS idx_token_usage_tenant ON token_usage(tenant_id, user_id);
