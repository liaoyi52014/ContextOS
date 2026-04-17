package types

import (
	"context"
	"time"
)

// VectorItem represents a single item stored in the vector database.
type VectorItem struct {
	ID       string            `json:"id"`
	Vector   []float32         `json:"vector"`
	Content  string            `json:"content"`
	URI      string            `json:"uri"`
	TenantID string            `json:"tenant_id"`
	UserID   string            `json:"user_id"`
	Metadata map[string]string `json:"metadata"`
}

// Filter specifies tenant/user filtering for vector queries.
type Filter struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id,omitempty"`
}

// SearchQuery defines parameters for a vector similarity search.
type SearchQuery struct {
	Vector    []float32 `json:"vector"`
	TopK      int       `json:"top_k"`
	Filter    *Filter   `json:"filter,omitempty"`
	Threshold float64   `json:"threshold"`
}

// SearchResult pairs a vector item with its similarity score.
type SearchResult struct {
	Item  VectorItem `json:"item"`
	Score float64    `json:"score"`
}

// VectorStore abstracts vector database operations.
type VectorStore interface {
	Init(ctx context.Context, dimension int) error
	Upsert(ctx context.Context, items []VectorItem) error
	Search(ctx context.Context, query SearchQuery) ([]SearchResult, error)
	Delete(ctx context.Context, ids []string) error
}

// EmbeddingProvider abstracts text embedding generation.
type EmbeddingProvider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
}

// LLMRequest defines a request to a language model.
type LLMRequest struct {
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Model       string    `json:"model,omitempty"`
}

// LLMResponse holds the result from a language model call.
type LLMResponse struct {
	Content          string `json:"content"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	Model            string `json:"model"`
}

// LLMClient abstracts language model completions.
type LLMClient interface {
	Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

// CacheStore abstracts cache operations (e.g., Redis).
type CacheStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

// PubSubCache extends CacheStore with best-effort publish/subscribe support.
type PubSubCache interface {
	CacheStore
	Publish(ctx context.Context, channel string, value []byte) error
	Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error)
}

// SessionStore abstracts session persistence operations.
type SessionStore interface {
	Load(ctx context.Context, tenantID, userID, sessionID string) (*Session, error)
	Save(ctx context.Context, session *Session) error
	Delete(ctx context.Context, tenantID, userID, sessionID string) error
	List(ctx context.Context, tenantID, userID string) ([]*SessionMeta, error)
}

// SessionScope identifies a tenant/user pair with persisted sessions.
type SessionScope struct {
	TenantID string
	UserID   string
}

// SessionScopeLister enumerates tenant/user scopes that contain sessions.
type SessionScopeLister interface {
	ListScopes(ctx context.Context) ([]SessionScope, error)
}

// MessageAppender appends a single message to session storage without relying
// on an in-memory session window.
type MessageAppender interface {
	AppendMessage(ctx context.Context, tenantID, userID, sessionID string, msg Message) error
}

// UsageRecordStore persists per-turn usage records for a session.
type UsageRecordStore interface {
	SaveUsageRecords(ctx context.Context, tenantID, userID, sessionID string, records []UsageRecord) error
}

// CompactCheckpointStore persists compaction checkpoints.
type CompactCheckpointStore interface {
	SaveCompactCheckpoint(ctx context.Context, checkpoint CompactCheckpoint) error
}

// ProfileStore abstracts user profile persistence operations.
type ProfileStore interface {
	Load(ctx context.Context, tenantID, userID string) (*UserProfile, error)
	Upsert(ctx context.Context, profile *UserProfile) error
	Search(ctx context.Context, tenantID, userID, query string, limit int) ([]ContentBlock, error)
}

// TaskStatus represents the state of a background task.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
)

// TaskRecord holds the state and metadata of a background task.
type TaskRecord struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	Status        TaskStatus             `json:"status"`
	TraceID       string                 `json:"trace_id"`
	ResultSummary map[string]interface{} `json:"result_summary,omitempty"`
	Error         string                 `json:"error,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	StartedAt     time.Time              `json:"started_at,omitempty"`
	FinishedAt    time.Time              `json:"finished_at,omitempty"`
}

// TaskTracker abstracts background task lifecycle management.
type TaskTracker interface {
	Create(ctx context.Context, taskType string, payload map[string]interface{}) (*TaskRecord, error)
	Start(ctx context.Context, taskID string) error
	Complete(ctx context.Context, taskID string, result map[string]interface{}) error
	Fail(ctx context.Context, taskID string, err error) error
	Get(ctx context.Context, taskID string) (*TaskRecord, error)
	QueueStats(ctx context.Context) (map[string]interface{}, error)
}

// ToolType classifies the kind of tool.
type ToolType string

const (
	ToolTypeSkill   ToolType = "skill"
	ToolTypeCommand ToolType = "command"
	ToolTypeMCP     ToolType = "mcp"
)

// Tool abstracts a callable tool in the registry.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]interface{}
	Execute(ctx context.Context, rc RequestContext, params map[string]interface{}) (string, error)
	Type() ToolType
}

// HookEvent identifies a lifecycle event for hooks.
type HookEvent string

const (
	HookAfterTurn    HookEvent = "afterTurn"
	HookBeforePrompt HookEvent = "beforePrompt"
	HookCompact      HookEvent = "compact"
	HookToolPostCall HookEvent = "tool.post_call"
)

// HookContext carries event data passed to hook handlers.
type HookContext struct {
	Event     HookEvent              `json:"event"`
	TenantID  string                 `json:"tenant_id"`
	UserID    string                 `json:"user_id"`
	SessionID string                 `json:"session_id"`
	Data      map[string]interface{} `json:"data"`
}

// Hook abstracts a lifecycle hook handler.
type Hook interface {
	Name() string
	Events() []HookEvent
	Execute(ctx context.Context, hookCtx HookContext) error
}

// WebhookManager abstracts webhook subscription management and delivery.
type WebhookManager interface {
	Notify(ctx context.Context, event WebhookEvent) error
	Subscribe(ctx context.Context, tenantID, url string, events []string) (string, error)
	Unsubscribe(ctx context.Context, id string) error
	List(ctx context.Context, tenantID string) ([]WebhookSubscription, error)
}
