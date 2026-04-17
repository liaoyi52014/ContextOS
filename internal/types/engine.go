package types

import (
	"context"
	"time"
)

// Engine defines the core context engine operations.
type Engine interface {
	Assemble(ctx context.Context, rc RequestContext, req AssembleRequest) (*AssembleResponse, error)
	Ingest(ctx context.Context, rc RequestContext, req IngestRequest) (*IngestResponse, error)
	SearchMemory(ctx context.Context, rc RequestContext, query string, limit int) ([]SearchResult, error)
	StoreMemory(ctx context.Context, rc RequestContext, content string, metadata map[string]string) error
	ForgetMemory(ctx context.Context, rc RequestContext, memoryID string) error
	GetSessionSummary(ctx context.Context, rc RequestContext) (string, error)
	ExecuteTool(ctx context.Context, rc RequestContext, toolName string, params map[string]interface{}) (string, error)
}

// AssembleRequest defines parameters for context assembly.
type AssembleRequest struct {
	SessionID   string           `json:"session_id"`
	Query       string           `json:"query"`
	TokenBudget int              `json:"token_budget,omitempty"`
	Telemetry   *TelemetryOption `json:"telemetry,omitempty"`
}

// AssembleResponse holds the result of context assembly.
type AssembleResponse struct {
	SystemPrompt    string              `json:"system_prompt"`
	Messages        []Message           `json:"messages"`
	EstimatedTokens int                 `json:"estimated_tokens"`
	Sources         []ContentBlock      `json:"sources"`
	Telemetry       *OperationTelemetry `json:"telemetry,omitempty"`
}

// IngestRequest defines parameters for ingesting messages into a session.
type IngestRequest struct {
	SessionID    string           `json:"session_id"`
	Messages     []Message        `json:"messages"`
	UsedContexts []string         `json:"used_contexts,omitempty"`
	UsedSkills   []string         `json:"used_skills,omitempty"`
	ToolCalls    []UsageRecord    `json:"tool_calls,omitempty"`
	Telemetry    *TelemetryOption `json:"telemetry,omitempty"`
}

// IngestResponse holds the result of an ingest operation.
type IngestResponse struct {
	Written          int                 `json:"written"`
	CompactTriggered bool                `json:"compact_triggered"`
	CompactTaskID    string              `json:"compact_task_id,omitempty"`
	Telemetry        *OperationTelemetry `json:"telemetry,omitempty"`
}

// MemorySearchRequest defines parameters for searching memories.
type MemorySearchRequest struct {
	Query     string           `json:"query"`
	Limit     int              `json:"limit,omitempty"`
	Telemetry *TelemetryOption `json:"telemetry,omitempty"`
}

// MemoryStoreRequest defines parameters for storing a memory.
type MemoryStoreRequest struct {
	Content   string            `json:"content"`
	Category  string            `json:"category,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Telemetry *TelemetryOption  `json:"telemetry,omitempty"`
}

// ToolExecuteRequest defines parameters for executing a tool.
type ToolExecuteRequest struct {
	SessionID string                 `json:"session_id"`
	ToolName  string                 `json:"tool_name"`
	Params    map[string]interface{} `json:"params"`
	Telemetry *TelemetryOption       `json:"telemetry,omitempty"`
}

// AuthVerifyResponse holds the result of admin session verification.
type AuthVerifyResponse struct {
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TaskGetResponse wraps a task record for API responses.
type TaskGetResponse struct {
	Task *TaskRecord `json:"task"`
}

// TelemetryOption controls per-request telemetry output.
type TelemetryOption struct {
	Summary bool `json:"summary,omitempty"`
}

// TelemetrySummary holds a structured execution summary for a request.
type TelemetrySummary struct {
	Operation  string                 `json:"operation"`
	Status     string                 `json:"status"`
	DurationMS float64                `json:"duration_ms"`
	Tokens     map[string]interface{} `json:"tokens,omitempty"`
	Vector     map[string]interface{} `json:"vector,omitempty"`
	Skill      map[string]interface{} `json:"skill,omitempty"`
	Compact    map[string]interface{} `json:"compact,omitempty"`
	Profile    map[string]interface{} `json:"profile,omitempty"`
	Errors     []map[string]string    `json:"errors,omitempty"`
}

// OperationTelemetry pairs a telemetry ID with its summary.
type OperationTelemetry struct {
	ID      string           `json:"id"`
	Summary TelemetrySummary `json:"summary"`
}

// WebhookEvent represents an event delivered via webhook.
type WebhookEvent struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	TenantID   string                 `json:"tenant_id"`
	UserID     string                 `json:"user_id"`
	SessionID  string                 `json:"session_id"`
	Payload    map[string]interface{} `json:"payload"`
	OccurredAt time.Time              `json:"occurred_at"`
}

// WebhookSubscription represents a registered webhook endpoint.
type WebhookSubscription struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
