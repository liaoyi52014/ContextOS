package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

type compactAwareSessionStore struct {
	*mock.MemorySessionStore
	mu          sync.Mutex
	checkpoints []types.CompactCheckpoint
}

func newCompactAwareSessionStore() *compactAwareSessionStore {
	return &compactAwareSessionStore{
		MemorySessionStore: mock.NewMemorySessionStore(),
	}
}

func (s *compactAwareSessionStore) SaveCompactCheckpoint(_ context.Context, checkpoint types.CompactCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var cloned types.CompactCheckpoint
	data, _ := json.Marshal(checkpoint)
	_ = json.Unmarshal(data, &cloned)
	s.checkpoints = append(s.checkpoints, cloned)
	return nil
}

func (s *compactAwareSessionStore) CheckpointCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.checkpoints)
}

type memoryTaskTracker struct {
	mu    sync.Mutex
	tasks map[string]*types.TaskRecord
}

func newMemoryTaskTracker() *memoryTaskTracker {
	return &memoryTaskTracker{tasks: make(map[string]*types.TaskRecord)}
}

func (t *memoryTaskTracker) Create(_ context.Context, taskType string, _ map[string]interface{}) (*types.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	record := &types.TaskRecord{
		ID:        "task-1",
		Type:      taskType,
		Status:    types.TaskPending,
		TraceID:   "task-1",
		CreatedAt: time.Now(),
	}
	t.tasks[record.ID] = record
	return record, nil
}

func (t *memoryTaskTracker) Start(_ context.Context, taskID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID].Status = types.TaskRunning
	t.tasks[taskID].StartedAt = time.Now()
	return nil
}

func (t *memoryTaskTracker) Complete(_ context.Context, taskID string, result map[string]interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID].Status = types.TaskCompleted
	t.tasks[taskID].FinishedAt = time.Now()
	t.tasks[taskID].ResultSummary = result
	return nil
}

func (t *memoryTaskTracker) Fail(_ context.Context, taskID string, err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID].Status = types.TaskFailed
	t.tasks[taskID].FinishedAt = time.Now()
	if err != nil {
		t.tasks[taskID].Error = err.Error()
	}
	return nil
}

func (t *memoryTaskTracker) Get(_ context.Context, taskID string) (*types.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	record := *t.tasks[taskID]
	return &record, nil
}

func (t *memoryTaskTracker) QueueStats(_ context.Context) (map[string]interface{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	stats := map[string]interface{}{}
	for _, task := range t.tasks {
		stats[string(task.Status)] = 1
	}
	return stats, nil
}

func TestCoreEngineIngest_TriggersCompactTaskAndPersistsCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newCompactAwareSessionStore()
	cache := mock.NewMemoryCacheStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages:         50,
		LRUCacheSize:        100,
		LRUCacheTTLSec:      2,
		SyncQueueSize:       100,
		SyncBatchSize:       10,
		SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	vectorStore := mock.NewMemoryVectorStore()
	embedding := mock.NewMockEmbeddingProvider(16)
	retrieval := NewRetrievalEngine(vectorStore, embedding, RetrievalConfig{
		RecallScoreThreshold: 0,
		RecallMaxResults:     10,
		PatternMaxResults:    10,
	}, nil)
	skillManager := &stubSkillCatalog{}
	builder := NewContextBuilder(vectorStore, embedding, sessions, nil, cache, skillManager, retrieval, ContextConfig{
		TokenBudget:            32000,
		MaxMessages:            50,
		RecentRawTurnCount:     8,
		SkillBodyLoadThreshold: 0.9,
		MaxLoadedSkillBodies:   2,
	})
	tasks := newMemoryTaskTracker()
	compact := NewCompactProcessor(
		mock.NewMockLLMClient(),
		sessions,
		nil,
		cache,
		vectorStore,
		embedding,
		tasks,
		nil,
		nil,
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

	engine := NewCoreEngine(CoreEngineDeps{
		Sessions:  sessions,
		Builder:   builder,
		Retrieval: retrieval,
		Vector:    vectorStore,
		Embedding: embedding,
		Compact:   compact,
		Tools:     NewToolRegistry(),
		Tasks:     tasks,
	})

	resp, err := engine.Ingest(ctx, types.RequestContext{
		TenantID:  "t1",
		UserID:    "u1",
		SessionID: "s1",
	}, types.IngestRequest{
		SessionID: "s1",
		Messages: []types.Message{
			{Role: "user", Content: "Remember that I prefer concise answers and we decided to use Go."},
		},
	})
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}
	if !resp.CompactTriggered {
		t.Fatal("expected compact to be triggered")
	}
	if resp.CompactTaskID == "" {
		t.Fatal("expected compact task id")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := tasks.Get(ctx, resp.CompactTaskID)
		if task != nil && task.Status == types.TaskCompleted && store.CheckpointCount() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	task, _ := tasks.Get(ctx, resp.CompactTaskID)
	if task == nil {
		t.Fatal("expected compact task to exist")
	}
	t.Fatalf("expected completed compact task and persisted checkpoint, got status=%s checkpoints=%d", task.Status, store.CheckpointCount())
}

func TestCoreEngineIngest_ReturnsTelemetryWhenRequested(t *testing.T) {
	ctx := context.Background()
	store := mock.NewMemorySessionStore()
	cache := mock.NewMemoryCacheStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages:         50,
		LRUCacheSize:        100,
		LRUCacheTTLSec:      2,
		SyncQueueSize:       100,
		SyncBatchSize:       10,
		SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	engine := NewCoreEngine(CoreEngineDeps{
		Sessions: sessions,
		Tools:    NewToolRegistry(),
	})

	resp, err := engine.Ingest(ctx, types.RequestContext{
		TenantID:  "t1",
		UserID:    "u1",
		SessionID: "s1",
	}, types.IngestRequest{
		SessionID: "s1",
		Messages:  []types.Message{{Role: "user", Content: "hello"}},
		Telemetry: &types.TelemetryOption{Summary: true},
	})
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}
	if resp.Telemetry == nil {
		t.Fatal("expected telemetry in response")
	}
	if resp.Telemetry.Summary.Operation != "ingest" {
		t.Fatalf("expected ingest operation, got %q", resp.Telemetry.Summary.Operation)
	}
}

type stubSkillCatalog struct{}

func (s *stubSkillCatalog) LoadCatalog(context.Context) ([]types.SkillMeta, error) { return nil, nil }
func (s *stubSkillCatalog) LoadBody(context.Context, string) (string, error)       { return "", nil }
