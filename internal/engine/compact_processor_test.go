package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

type recordingWebhookManager struct {
	mu     sync.Mutex
	events []types.WebhookEvent
}

func (r *recordingWebhookManager) Notify(_ context.Context, event types.WebhookEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}
func (r *recordingWebhookManager) Subscribe(context.Context, string, string, []string) (string, error) {
	return "", nil
}
func (r *recordingWebhookManager) Unsubscribe(context.Context, string) error { return nil }
func (r *recordingWebhookManager) List(context.Context, string) ([]types.WebhookSubscription, error) {
	return nil, nil
}

func TestCompactProcessor_ExecuteCompact_NotifiesWebhookEvents(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages:         50,
		LRUCacheSize:        100,
		LRUCacheTTLSec:      2,
		SyncQueueSize:       100,
		SyncBatchSize:       10,
		SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	webhooks := &recordingWebhookManager{}
	vectorStore := mock.NewMemoryVectorStore()
	embedding := mock.NewMockEmbeddingProvider(16)
	processor := NewCompactProcessor(
		mock.NewMockLLMClient(),
		sessions,
		nil,
		cache,
		vectorStore,
		embedding,
		nil,
		nil,
		webhooks,
		nil,
		&CompactConfig{
			CompactBudgetRatio:    1,
			CompactTokenThreshold: 1,
			CompactTurnThreshold:  1,
			CompactIntervalMin:    60,
			CompactTimeoutSec:     5,
			MaxConcurrentCompacts: 2,
			TokenBudget:           32000,
		},
		nil,
	)

	snapshot := &types.Session{
		ID:       "s1",
		TenantID: "t1",
		UserID:   "u1",
		Messages: []types.Message{
			{Role: "user", Content: "Remember that I prefer concise answers."},
		},
		Metadata:    map[string]interface{}{},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		MaxMessages: 50,
	}
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)

	if err := processor.executeCompact(context.Background(), types.RequestContext{
		TenantID:  "t1",
		UserID:    "u1",
		SessionID: "s1",
	}, snapshot); err != nil {
		t.Fatalf("executeCompact failed: %v", err)
	}

	webhooks.mu.Lock()
	defer webhooks.mu.Unlock()
	if len(webhooks.events) < 2 {
		t.Fatalf("expected at least 2 webhook events, got %d", len(webhooks.events))
	}
}
