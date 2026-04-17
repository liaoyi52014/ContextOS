CREATE TABLE IF NOT EXISTS model_providers (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL UNIQUE,
    api_base VARCHAR(512) NOT NULL,
    api_key VARCHAR(512) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS models (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL UNIQUE,
    provider_id VARCHAR(64) NOT NULL REFERENCES model_providers(id),
    model_id VARCHAR(256) NOT NULL,
    type VARCHAR(16) NOT NULL,
    dimension INT NOT NULL DEFAULT 0,
    is_default BOOLEAN NOT NULL DEFAULT false,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_models_type ON models(type);

CREATE TABLE IF NOT EXISTS skills (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'enabled',
    tool_bindings JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    url VARCHAR(1024) NOT NULL,
    events JSONB NOT NULL DEFAULT '[]',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_webhook_tenant ON webhook_subscriptions(tenant_id);

CREATE TABLE IF NOT EXISTS tasks (
    id VARCHAR(64) PRIMARY KEY,
    type VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL,
    trace_id VARCHAR(64) NOT NULL DEFAULT '',
    result_summary JSONB NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status, created_at DESC);
