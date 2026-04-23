package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.2: AddMessage has race condition
// Expected behavior: concurrent AddMessage calls are safe (no data race)
// Current bug: after mu.Unlock(), shared session pointer is used in putRedis
// and syncQueue.Enqueue, causing data races
// **Validates: Requirements 1.2**
//
// NOTE: This test must be run with -race flag to detect the data race.
// The race detector will report a DATA RACE on session.Messages if the bug exists.
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_2_AddMessageRaceCondition(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-race"}
	sess, err := sm.GetOrCreate(ctx, rc)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}

	// Launch 100 concurrent AddMessage calls.
	// With -race flag, this will detect data races on session.Messages
	// because AddMessage unlocks the mutex before using the shared session
	// pointer in putRedis and syncQueue.Enqueue.
	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			msg := types.Message{
				Role:      "user",
				Content:   fmt.Sprintf("concurrent message %d", idx),
				Timestamp: time.Now(),
			}
			if err := sm.AddMessage(ctx, sess, msg); err != nil {
				errCh <- fmt.Errorf("AddMessage(%d) failed: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	// The real test is the -race detector. If it reports a race, Bug 1.2 is confirmed.
	// As a secondary check, verify all messages were added (no corruption).
	// With MaxMessages=10, we should have exactly 10 messages after 100 adds.
	if len(sess.Messages) != 10 {
		t.Errorf("Bug 1.2 secondary check: expected 10 messages (MaxMessages), got %d — possible corruption from race condition",
			len(sess.Messages))
	}

	// Verify no duplicate or corrupted messages.
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			t.Errorf("Bug 1.2: corrupted message role: %q", msg.Role)
		}
		if msg.Content == "" {
			t.Error("Bug 1.2: empty message content — possible corruption")
		}
	}
}
