package log

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditEntry represents a single audit log record.
type AuditEntry struct {
	ID         int64                  `json:"id"`
	Timestamp  time.Time              `json:"ts"`
	TenantID   string                 `json:"tenant_id"`
	UserID     string                 `json:"user_id"`
	Action     string                 `json:"action"`
	TargetType string                 `json:"target_type"`
	TargetID   string                 `json:"target_id"`
	Detail     map[string]interface{} `json:"detail"`
	TraceID    string                 `json:"trace_id"`
}

// AuditLogger records and queries audit log entries.
type AuditLogger struct {
	db *pgxpool.Pool
}

// NewAuditLogger creates a new AuditLogger backed by the given database pool.
func NewAuditLogger(db *pgxpool.Pool) *AuditLogger {
	return &AuditLogger{db: db}
}

// Log records an audit event into the audit_logs table.
func (a *AuditLogger) Log(ctx context.Context, tenantID, userID, action, targetType, targetID string, detail map[string]interface{}, traceID string) error {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		detailJSON = []byte("{}")
	}

	_, err = a.db.Exec(ctx,
		`INSERT INTO audit_logs (tenant_id, user_id, action, target_type, target_id, detail, trace_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tenantID, userID, action, targetType, targetID, detailJSON, traceID,
	)
	return err
}

// Query retrieves audit log entries for a tenant within a time range.
func (a *AuditLogger) Query(ctx context.Context, tenantID string, from, to time.Time, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := a.db.Query(ctx,
		`SELECT id, ts, tenant_id, user_id, action, target_type, target_id, detail, trace_id
		 FROM audit_logs
		 WHERE tenant_id = $1 AND ts >= $2 AND ts <= $3
		 ORDER BY ts DESC
		 LIMIT $4`,
		tenantID, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var detailJSON []byte
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.TenantID, &e.UserID, &e.Action, &e.TargetType, &e.TargetID, &detailJSON, &e.TraceID); err != nil {
			return nil, err
		}
		if len(detailJSON) > 0 {
			_ = json.Unmarshal(detailJSON, &e.Detail)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
