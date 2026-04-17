package log

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenUsageEntry represents a single token usage record.
type TokenUsageEntry struct {
	ID               int64     `json:"id"`
	Timestamp        time.Time `json:"ts"`
	TenantID         string    `json:"tenant_id"`
	UserID           string    `json:"user_id"`
	CallType         string    `json:"call_type"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	SessionID        string    `json:"session_id"`
	TraceID          string    `json:"trace_id"`
}

// TokenAuditor records and queries token usage entries.
type TokenAuditor struct {
	db *pgxpool.Pool
}

// NewTokenAuditor creates a new TokenAuditor backed by the given database pool.
func NewTokenAuditor(db *pgxpool.Pool) *TokenAuditor {
	return &TokenAuditor{db: db}
}

// Record inserts a token usage entry into the token_usage table.
func (t *TokenAuditor) Record(ctx context.Context, tenantID, userID, callType, model string, promptTokens, completionTokens, totalTokens int, sessionID, traceID string) error {
	_, err := t.db.Exec(ctx,
		`INSERT INTO token_usage (tenant_id, user_id, call_type, model, prompt_tokens, completion_tokens, total_tokens, session_id, trace_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		tenantID, userID, callType, model, promptTokens, completionTokens, totalTokens, sessionID, traceID,
	)
	return err
}

// Query retrieves token usage entries for a tenant within a time range,
// optionally filtered by call type.
func (t *TokenAuditor) Query(ctx context.Context, tenantID string, from, to time.Time, callType string, limit int) ([]TokenUsageEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	var rows pgx.Rows
	var err error

	if callType != "" {
		rows, err = t.db.Query(ctx,
			`SELECT id, ts, tenant_id, user_id, call_type, model, prompt_tokens, completion_tokens, total_tokens, session_id, trace_id
			 FROM token_usage
			 WHERE tenant_id = $1 AND ts >= $2 AND ts <= $3 AND call_type = $4
			 ORDER BY ts DESC
			 LIMIT $5`,
			tenantID, from, to, callType, limit,
		)
	} else {
		rows, err = t.db.Query(ctx,
			`SELECT id, ts, tenant_id, user_id, call_type, model, prompt_tokens, completion_tokens, total_tokens, session_id, trace_id
			 FROM token_usage
			 WHERE tenant_id = $1 AND ts >= $2 AND ts <= $3
			 ORDER BY ts DESC
			 LIMIT $4`,
			tenantID, from, to, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TokenUsageEntry
	for rows.Next() {
		var e TokenUsageEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.TenantID, &e.UserID, &e.CallType, &e.Model, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens, &e.SessionID, &e.TraceID); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Aggregate returns aggregated token usage grouped by call_type for a tenant
// within a time range. Returns a map with call_type keys and sum values.
func (t *TokenAuditor) Aggregate(ctx context.Context, tenantID string, from, to time.Time) (map[string]interface{}, error) {
	rows, err := t.db.Query(ctx,
		`SELECT call_type, SUM(prompt_tokens) AS sum_prompt, SUM(completion_tokens) AS sum_completion, SUM(total_tokens) AS sum_total
		 FROM token_usage
		 WHERE tenant_id = $1 AND ts >= $2 AND ts <= $3
		 GROUP BY call_type`,
		tenantID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]interface{})
	for rows.Next() {
		var callType string
		var sumPrompt, sumCompletion, sumTotal int64
		if err := rows.Scan(&callType, &sumPrompt, &sumCompletion, &sumTotal); err != nil {
			return nil, err
		}
		result[callType] = map[string]interface{}{
			"prompt_tokens":     sumPrompt,
			"completion_tokens": sumCompletion,
			"total_tokens":      sumTotal,
		}
	}
	return result, rows.Err()
}
