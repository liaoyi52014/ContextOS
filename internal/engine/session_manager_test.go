package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

func newTestSessionManager() (*SessionManager, *mock.MemoryCacheStore, *mock.MemorySessionStore) {
	cache := mock.NewMemoryCacheStore()
	store := mock.NewMemorySessionStore()
	cfg := SessionConfig{
		MaxMessages:         10,
		LRUCacheSize:        100,
		LRUCacheTTLSec:      2,
		SyncQueueSize:       100,
		SyncBatchSize:       10,
		SyncFlushIntervalMs: 50,
	}
	sm := NewSessionManager(cache, store, cfg)
	return sm, cache, store
}

func TestGetOrCreate_NewSession(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s1"}

	sess, err := sm.GetOrCreate(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.ID != "s1" {
		t.Errorf("expected session ID s1, got %s", sess.ID)
	}
	if sess.TenantID != "t1" || sess.UserID != "u1" {
		t.Errorf("unexpected tenant/user: %s/%s", sess.TenantID, sess.UserID)
	}
	if sess.MaxMessages != 10 {
		t.Errorf("expected MaxMessages 10, got %d", sess.MaxMessages)
	}
}

func TestGetOrCreate_ExistingInPG(t *testing.T) {
	sm, _, store := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	// Pre-populate PG.
	existing := &types.Session{
		ID:          "s2",
		TenantID:    "t1",
		UserID:      "u1",
		Messages:    []types.Message{{Role: "user", Content: "hello"}},
		Metadata:    map[string]interface{}{},
		MaxMessages: 50,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	_ = store.Save(ctx, existing)

	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s2"}
	sess, err := sm.GetOrCreate(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.ID != "s2" {
		t.Errorf("expected session ID s2, got %s", sess.ID)
	}
	if len(sess.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(sess.Messages))
	}
}

func TestGetOrCreate_GeneratesIDWhenEmpty(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: ""}

	sess, err := sm.GetOrCreate(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.ID == "" {
		t.Error("expected generated session ID, got empty")
	}
}

func TestAddMessage_TrimsToMax(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s1"}
	sess, _ := sm.GetOrCreate(ctx, rc)

	// Add 12 messages (max is 10).
	for i := 0; i < 12; i++ {
		msg := types.Message{Role: "user", Content: "msg", Timestamp: time.Now()}
		if err := sm.AddMessage(ctx, sess, msg); err != nil {
			t.Fatalf("AddMessage failed: %v", err)
		}
	}

	if len(sess.Messages) != 10 {
		t.Errorf("expected 10 messages after trim, got %d", len(sess.Messages))
	}
}

func TestRecordUsage_UpdatesMetadata(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s1"}
	sess, _ := sm.GetOrCreate(ctx, rc)

	records := []types.UsageRecord{
		{URI: "mem://1", Timestamp: time.Now()},
		{SkillName: "code-review", Timestamp: time.Now()},
		{ToolName: "grep", Timestamp: time.Now()},
	}
	if err := sm.RecordUsage(ctx, sess, records); err != nil {
		t.Fatalf("RecordUsage failed: %v", err)
	}

	if toInt(sess.Metadata["contexts_used"]) != 1 {
		t.Errorf("expected contexts_used=1, got %v", sess.Metadata["contexts_used"])
	}
	if toInt(sess.Metadata["skills_used"]) != 1 {
		t.Errorf("expected skills_used=1, got %v", sess.Metadata["skills_used"])
	}
	if toInt(sess.Metadata["tools_used"]) != 1 {
		t.Errorf("expected tools_used=1, got %v", sess.Metadata["tools_used"])
	}
}

func TestClone_DeepCopy(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s1"}
	sess, _ := sm.GetOrCreate(ctx, rc)
	_ = sm.AddMessage(ctx, sess, types.Message{Role: "user", Content: "hello", Timestamp: time.Now()})

	cloned := sm.Clone(sess)
	if cloned == nil {
		t.Fatal("Clone returned nil")
	}
	if cloned.ID == sess.ID {
		t.Error("cloned session should have a different ID")
	}
	if len(cloned.Messages) != len(sess.Messages) {
		t.Errorf("cloned messages count mismatch: %d vs %d", len(cloned.Messages), len(sess.Messages))
	}

	// Mutating clone should not affect original.
	cloned.Messages = append(cloned.Messages, types.Message{Role: "assistant", Content: "hi"})
	if len(sess.Messages) == len(cloned.Messages) {
		t.Error("mutation of clone affected original")
	}
}

func TestCacheKey_Format(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	key := sm.cacheKey("tenant", "user", "session")
	expected := "tenant:user:session"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestSyncQueue_DLQLen(t *testing.T) {
	store := mock.NewMemorySessionStore()
	sq := NewSyncQueue(store, 10, 5, 50*time.Millisecond)
	if sq.DLQLen() != 0 {
		t.Errorf("expected DLQ length 0, got %d", sq.DLQLen())
	}
}

func TestGetOrCreate_LRUCacheHit(t *testing.T) {
	sm, _, _ := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s1"}

	sess1, _ := sm.GetOrCreate(ctx, rc)
	sess2, _ := sm.GetOrCreate(ctx, rc)

	if sess1.ID != sess2.ID {
		t.Errorf("expected same session from LRU cache, got %s vs %s", sess1.ID, sess2.ID)
	}
}

func TestAddMessage_PersistsFullHistoryBeyondMaxMessages(t *testing.T) {
	sm, _, store := newTestSessionManager()
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-overflow"}
	sess, err := sm.GetOrCreate(ctx, rc)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}

	for i := 0; i < 12; i++ {
		if err := sm.AddMessage(ctx, sess, types.Message{
			Role:      "user",
			Content:   "msg",
			Timestamp: time.Now(),
			Metadata:  map[string]interface{}{"index": i},
		}); err != nil {
			t.Fatalf("AddMessage(%d) failed: %v", i, err)
		}
	}

	time.Sleep(200 * time.Millisecond)

	persisted, err := store.Load(ctx, "t1", "u1", "s-overflow")
	if err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	if persisted == nil {
		t.Fatal("expected persisted session")
	}
	if got := len(persisted.Messages); got != 12 {
		t.Fatalf("expected all 12 messages to be persisted, got %d", got)
	}
}

type usageAwareSessionStore struct {
	*mock.MemorySessionStore
	usageRecords map[string][]types.UsageRecord
}

func newUsageAwareSessionStore() *usageAwareSessionStore {
	return &usageAwareSessionStore{
		MemorySessionStore: mock.NewMemorySessionStore(),
		usageRecords:       make(map[string][]types.UsageRecord),
	}
}

func (s *usageAwareSessionStore) SaveUsageRecords(_ context.Context, tenantID, userID, sessionID string, records []types.UsageRecord) error {
	key := tenantID + ":" + userID + ":" + sessionID
	cloned := make([]types.UsageRecord, len(records))
	data, _ := json.Marshal(records)
	_ = json.Unmarshal(data, &cloned)
	s.usageRecords[key] = append(s.usageRecords[key], cloned...)
	return nil
}

func TestRecordUsage_PersistsUsageRecordsToStore(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newUsageAwareSessionStore()
	sm := NewSessionManager(cache, store, SessionConfig{
		MaxMessages:         10,
		LRUCacheSize:        100,
		LRUCacheTTLSec:      2,
		SyncQueueSize:       100,
		SyncBatchSize:       10,
		SyncFlushIntervalMs: 50,
	})
	defer sm.Stop()

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-usage"}
	sess, err := sm.GetOrCreate(ctx, rc)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}

	records := []types.UsageRecord{
		{URI: "memory://1", Success: true, Timestamp: time.Now()},
		{SkillName: "planner", Success: true, Timestamp: time.Now()},
		{ToolName: "search", Success: true, Timestamp: time.Now()},
	}
	if err := sm.RecordUsage(ctx, sess, records); err != nil {
		t.Fatalf("RecordUsage failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	key := "t1:u1:s-usage"
	if got := len(store.usageRecords[key]); got != len(records) {
		t.Fatalf("expected %d usage records to be persisted, got %d", len(records), got)
	}
}
