CREATE TABLE IF NOT EXISTS sessions (
    id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    commit_count INT NOT NULL DEFAULT 0,
    contexts_used INT NOT NULL DEFAULT 0,
    skills_used INT NOT NULL DEFAULT 0,
    tools_used INT NOT NULL DEFAULT 0,
    llm_token_usage JSONB NOT NULL DEFAULT '{}',
    embedding_token_usage JSONB NOT NULL DEFAULT '{}',
    max_messages INT NOT NULL DEFAULT 50,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, id)
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(tenant_id, user_id);

CREATE TABLE IF NOT EXISTS session_messages (
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    session_id VARCHAR(64) NOT NULL,
    seq INT NOT NULL,
    role VARCHAR(16) NOT NULL,
    content TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    tool_calls JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, session_id, seq),
    FOREIGN KEY (tenant_id, user_id, session_id) REFERENCES sessions(tenant_id, user_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_session_messages_recent ON session_messages(tenant_id, user_id, session_id, seq DESC);

CREATE TABLE IF NOT EXISTS session_usage_records (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    session_id VARCHAR(64) NOT NULL,
    uri VARCHAR(512) NOT NULL DEFAULT '',
    skill_name VARCHAR(128) NOT NULL DEFAULT '',
    tool_name VARCHAR(128) NOT NULL DEFAULT '',
    input_summary TEXT NOT NULL DEFAULT '',
    output_summary TEXT NOT NULL DEFAULT '',
    success BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_session_usage_records_session ON session_usage_records(tenant_id, user_id, session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS compact_checkpoints (
    id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    source_turn_start INT NOT NULL,
    source_turn_end INT NOT NULL,
    summary_content TEXT NOT NULL,
    extracted_memory_ids JSONB NOT NULL DEFAULT '[]',
    prompt_tokens_used INT NOT NULL DEFAULT 0,
    completion_tokens_used INT NOT NULL DEFAULT 0,
    FOREIGN KEY (tenant_id, user_id, session_id) REFERENCES sessions(tenant_id, user_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_compact_session ON compact_checkpoints(tenant_id, user_id, session_id);

CREATE TABLE IF NOT EXISTS memory_facts (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    content TEXT NOT NULL,
    category VARCHAR(32) NOT NULL,
    uri VARCHAR(512),
    source_session_id VARCHAR(64),
    source_turn_start INT,
    source_turn_end INT,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_memory_tenant ON memory_facts(tenant_id, user_id);

CREATE TABLE IF NOT EXISTS user_profiles (
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    summary TEXT NOT NULL,
    interests JSONB NOT NULL DEFAULT '[]',
    preferences JSONB NOT NULL DEFAULT '[]',
    goals JSONB NOT NULL DEFAULT '[]',
    constraints JSONB NOT NULL DEFAULT '[]',
    metadata JSONB NOT NULL DEFAULT '{}',
    source_session_id VARCHAR(64),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id)
);
