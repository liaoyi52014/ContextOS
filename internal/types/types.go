package types

import "time"

// RequestContext carries per-request isolation identifiers.
type RequestContext struct {
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
}

// Message represents a single chat message in a session.
type Message struct {
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	Timestamp time.Time              `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	ToolCalls []ToolCall             `json:"tool_calls,omitempty"`
}

// ToolCall represents a tool invocation within a message.
type ToolCall struct {
	ID        string                 `json:"id"`
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
	Result    string                 `json:"result,omitempty"`
	Status    string                 `json:"status"`
}

// UsageRecord tracks resource usage within a session.
type UsageRecord struct {
	URI           string    `json:"uri,omitempty"`
	SkillName     string    `json:"skill_name,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	InputSummary  string    `json:"input_summary,omitempty"`
	OutputSummary string    `json:"output_summary,omitempty"`
	Success       bool      `json:"success"`
	Timestamp     time.Time `json:"timestamp"`
}

// MemoryFact represents an extracted memory fact from conversations.
type MemoryFact struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"tenant_id"`
	UserID          string            `json:"user_id"`
	Content         string            `json:"content"`
	Category        string            `json:"category"`
	URI             string            `json:"uri"`
	Vector          []float32         `json:"vector,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	SourceSessionID string            `json:"source_session_id"`
	SourceTurnRange [2]int            `json:"source_turn_range"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// UserProfile stores long-term user preferences and traits.
type UserProfile struct {
	TenantID        string            `json:"tenant_id"`
	UserID          string            `json:"user_id"`
	Summary         string            `json:"summary"`
	Interests       []string          `json:"interests"`
	Preferences     []string          `json:"preferences"`
	Goals           []string          `json:"goals"`
	Constraints     []string          `json:"constraints"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	SourceSessionID string            `json:"source_session_id"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// SessionMeta holds aggregated metadata for a session.
type SessionMeta struct {
	ID                  string         `json:"id"`
	TenantID            string         `json:"tenant_id"`
	UserID              string         `json:"user_id"`
	MessageCount        int            `json:"message_count"`
	CommitCount         int            `json:"commit_count"`
	ContextsUsed        int            `json:"contexts_used"`
	SkillsUsed          int            `json:"skills_used"`
	ToolsUsed           int            `json:"tools_used"`
	LLMTokenUsage       map[string]int `json:"llm_token_usage"`
	EmbeddingTokenUsage map[string]int `json:"embedding_token_usage"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// ContentLevel represents the progressive loading level of content.
type ContentLevel int

const (
	ContentL0 ContentLevel = iota // Summary/Index
	ContentL1                     // Overview
	ContentL2                     // Full content
)

// ContentBlock represents a piece of content at a specific level.
type ContentBlock struct {
	URI     string       `json:"uri"`
	Level   ContentLevel `json:"level"`
	Content string       `json:"content"`
	Source  string       `json:"source"` // memory / skill / profile / history
	Score   float64      `json:"score"`
	Tokens  int          `json:"tokens"`
}

// Session represents a conversation session.
type Session struct {
	ID          string                 `json:"id"`
	TenantID    string                 `json:"tenant_id"`
	UserID      string                 `json:"user_id"`
	Messages    []Message              `json:"messages"`
	Usage       []UsageRecord          `json:"usage,omitempty"`
	Metadata    map[string]interface{} `json:"metadata"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	MaxMessages int                    `json:"max_messages"`
}

// CompactCheckpoint records a compaction checkpoint for a session.
type CompactCheckpoint struct {
	ID                   string    `json:"id"`
	SessionID            string    `json:"session_id"`
	TenantID             string    `json:"tenant_id"`
	UserID               string    `json:"user_id"`
	CommittedAt          time.Time `json:"committed_at"`
	SourceTurnStart      int       `json:"source_turn_start"`
	SourceTurnEnd        int       `json:"source_turn_end"`
	SummaryContent       string    `json:"summary_content"`
	ExtractedMemoryIDs   []string  `json:"extracted_memory_ids"`
	PromptTokensUsed     int       `json:"prompt_tokens_used"`
	CompletionTokensUsed int       `json:"completion_tokens_used"`
}

// ModelType distinguishes between LLM and embedding models.
type ModelType string

const (
	ModelTypeLLM       ModelType = "llm"
	ModelTypeEmbedding ModelType = "embedding"
)

// ModelProvider represents an AI model provider configuration.
type ModelProvider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	APIBase   string    `json:"api_base"`
	APIKey    string    `json:"api_key"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ModelConfig represents a specific model configuration.
type ModelConfig struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ProviderID string    `json:"provider_id"`
	ModelID    string    `json:"model_id"`
	Type       ModelType `json:"type"`
	Dimension  int       `json:"dimension"`
	IsDefault  bool      `json:"is_default"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
