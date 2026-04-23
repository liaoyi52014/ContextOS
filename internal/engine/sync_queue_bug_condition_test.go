package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.4: DLQ items are never consumed
// Expected behavior: DLQ has a consumer that retries failed items
// Current bug: DLQ channel is write-only, items accumulate forever
// **Validates: Requirements 1.4**
// ═══════════════════════════════════════════════════════════════════════════════

// alwaysFailingStore is a SessionStore that always fails on Save.
type alwaysFailingStore struct {
	loadSession *types.Session
}

func (s *alwaysFailingStore) Load(_ context.Context, _, _, _ string) (*types.Session, error) {
	if s.loadSession != nil {
		return s.loadSession, nil
	}
	return &types.Session{
		ID:       "s-dlq",
		TenantID: "t1",
		UserID:   "u1",
		Messages: []types.Message{},
		Metadata: map[string]interface{}{},
	}, nil
}

func (s *alwaysFailingStore) Save(_ context.Context, _ *types.Session) error {
	return fmt.Errorf("simulated persistent store failure")
}

func (s *alwaysFailingStore) Delete(_ context.Context, _, _, _ string) error {
	return nil
}

func (s *alwaysFailingStore) List(_ context.Context, _, _ string) ([]*types.SessionMeta, error) {
	return nil, nil
}

func TestBugCondition_1_4_DLQNeverConsumed(t *testing.T) {
	failStore := &alwaysFailingStore{}
	sq := NewSyncQueue(failStore, 100, 5, 50*time.Millisecond)
	sq.SetDLQRetryInterval(3 * time.Second)
	sq.SetMaxDLQRetries(0)
	sq.Start()

	// Enqueue several items that will fail to persist and end up in DLQ.
	for i := 0; i < 10; i++ {
		_ = sq.Enqueue(&SyncItem{
			SessionID: "s-dlq",
			TenantID:  "t1",
			UserID:    "u1",
			Message:   types.Message{Role: "user", Content: fmt.Sprintf("msg %d", i)},
			EnqueueAt: time.Now(),
		})
	}

	// Wait for flush cycles to process items and move them to DLQ.
	// saveWithRetry retries 3 times with exponential backoff (100ms, 200ms, 400ms),
	// so we need to wait long enough for all retries to complete.
	time.Sleep(5 * time.Second)

	dlqLen := sq.DLQLen()
	if dlqLen == 0 {
		t.Fatal("expected items in DLQ after persistent failures")
	}

	// Record initial DLQ length.
	initialDLQLen := dlqLen

	// Wait more time to see if DLQ is being consumed.
	time.Sleep(2 * time.Second)

	finalDLQLen := sq.DLQLen()

	// EXPECTED (correct) behavior: DLQ should have a consumer that retries items,
	// so DLQ length should decrease over time (or items should be processed).
	// BUG: DLQ has no consumer, so length only grows or stays the same.
	if finalDLQLen >= initialDLQLen {
		t.Errorf("Bug 1.4 confirmed: DLQ length did not decrease over time (initial=%d, final=%d) — no DLQ consumer exists",
			initialDLQLen, finalDLQLen)
	}

	sq.Stop()
}
