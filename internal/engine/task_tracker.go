package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check that DefaultTaskTracker implements types.TaskTracker.
var _ types.TaskTracker = (*DefaultTaskTracker)(nil)

// DefaultTaskTracker manages background task lifecycle using PostgreSQL
// for persistence and an optional cache for fast lookups.
type DefaultTaskTracker struct {
	cache types.CacheStore
	store *pgxpool.Pool
}

// NewDefaultTaskTracker creates a new DefaultTaskTracker.
func NewDefaultTaskTracker(cache types.CacheStore, store *pgxpool.Pool) *DefaultTaskTracker {
	return &DefaultTaskTracker{cache: cache, store: store}
}

// Create inserts a new task with status=pending and returns the TaskRecord.
func (t *DefaultTaskTracker) Create(ctx context.Context, taskType string, payload map[string]interface{}) (*types.TaskRecord, error) {
	id := generateTaskID()
	traceID := id // use task ID as trace ID for simplicity

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		payloadJSON = []byte("{}")
	}

	now := time.Now().UTC()
	_, err = t.store.Exec(ctx,
		`INSERT INTO tasks (id, type, status, trace_id, result_summary, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, taskType, string(types.TaskPending), traceID, payloadJSON, now,
	)
	if err != nil {
		return nil, fmt.Errorf("creating task: %w", err)
	}

	return &types.TaskRecord{
		ID:        id,
		Type:      taskType,
		Status:    types.TaskPending,
		TraceID:   traceID,
		CreatedAt: now,
	}, nil
}

// Start transitions a task from pending to running.
func (t *DefaultTaskTracker) Start(ctx context.Context, taskID string) error {
	return t.transition(ctx, taskID, types.TaskPending, types.TaskRunning)
}

// Complete transitions a task from running to completed with a result summary.
func (t *DefaultTaskTracker) Complete(ctx context.Context, taskID string, result map[string]interface{}) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		resultJSON = []byte("{}")
	}

	now := time.Now().UTC()
	tag, err := t.store.Exec(ctx,
		`UPDATE tasks SET status = $1, result_summary = $2, finished_at = $3
		 WHERE id = $4 AND status = $5`,
		string(types.TaskCompleted), resultJSON, now, taskID, string(types.TaskRunning),
	)
	if err != nil {
		return fmt.Errorf("completing task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s cannot transition to completed: not in running state", taskID)
	}
	return nil
}

// Fail transitions a task from running to failed with an error message.
func (t *DefaultTaskTracker) Fail(ctx context.Context, taskID string, taskErr error) error {
	errMsg := ""
	if taskErr != nil {
		errMsg = taskErr.Error()
	}

	now := time.Now().UTC()
	tag, err := t.store.Exec(ctx,
		`UPDATE tasks SET status = $1, error = $2, finished_at = $3
		 WHERE id = $4 AND status = $5`,
		string(types.TaskFailed), errMsg, now, taskID, string(types.TaskRunning),
	)
	if err != nil {
		return fmt.Errorf("failing task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s cannot transition to failed: not in running state", taskID)
	}
	return nil
}

// Get retrieves a task record by ID.
func (t *DefaultTaskTracker) Get(ctx context.Context, taskID string) (*types.TaskRecord, error) {
	row := t.store.QueryRow(ctx,
		`SELECT id, type, status, trace_id, result_summary, error, created_at, started_at, finished_at
		 FROM tasks WHERE id = $1`,
		taskID,
	)

	var rec types.TaskRecord
	var resultJSON []byte
	var startedAt, finishedAt *time.Time

	err := row.Scan(&rec.ID, &rec.Type, &rec.Status, &rec.TraceID, &resultJSON, &rec.Error, &rec.CreatedAt, &startedAt, &finishedAt)
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}

	if len(resultJSON) > 0 {
		_ = json.Unmarshal(resultJSON, &rec.ResultSummary)
	}
	if startedAt != nil {
		rec.StartedAt = *startedAt
	}
	if finishedAt != nil {
		rec.FinishedAt = *finishedAt
	}
	return &rec, nil
}

// QueueStats returns counts of tasks grouped by status.
func (t *DefaultTaskTracker) QueueStats(ctx context.Context) (map[string]interface{}, error) {
	rows, err := t.store.Query(ctx,
		`SELECT status, COUNT(*) FROM tasks GROUP BY status`,
	)
	if err != nil {
		return nil, fmt.Errorf("querying task stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]interface{})
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats[status] = count
	}
	return stats, rows.Err()
}

// transition performs a monotonic state transition for a task.
func (t *DefaultTaskTracker) transition(ctx context.Context, taskID string, from, to types.TaskStatus) error {
	now := time.Now().UTC()

	var query string
	if to == types.TaskRunning {
		query = `UPDATE tasks SET status = $1, started_at = $2 WHERE id = $3 AND status = $4`
	} else {
		query = `UPDATE tasks SET status = $1, finished_at = $2 WHERE id = $3 AND status = $4`
	}

	tag, err := t.store.Exec(ctx, query, string(to), now, taskID, string(from))
	if err != nil {
		return fmt.Errorf("transitioning task %s to %s: %w", taskID, to, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s cannot transition from %s to %s", taskID, from, to)
	}
	return nil
}

// generateTaskID produces a random task ID.
func generateTaskID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "task_" + hex.EncodeToString(b)
}
