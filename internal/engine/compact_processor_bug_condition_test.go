package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

// ── helpers shared across bug-condition tests ──

// failingSessionStore wraps MemorySessionStore and makes Save always fail.
type failingSessionStore struct {
	*mock.MemorySessionStore
}

func (f *failingSessionStore) Save(_ context.Context, _ *types.Session) error {
	return fmt.Errorf("simulated store failure")
}

// countingEmbeddingProvider counts how many times Embed is called.
type countingEmbeddingProvider struct {
	mock.MockEmbeddingProvider
	mu    sync.Mutex
	calls int
}

func newCountingEmbeddingProvider(dim int) *countingEmbeddingProvider {
	return &countingEmbeddingProvider{
		MockEmbeddingProvider: *mock.NewMockEmbeddingProvider(dim),
	}
}

func (c *countingEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.MockEmbeddingProvider.Embed(ctx, texts)
}

func (c *countingEmbeddingProvider) CallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// panicTaskTracker panics on Start to test panic recovery.
type panicTaskTracker struct {
	memoryTaskTracker
}

func newPanicTaskTracker() *panicTaskTracker {
	return &panicTaskTracker{memoryTaskTracker: *newMemoryTaskTracker()}
}

func (p *panicTaskTracker) Start(_ context.Context, _ string) error {
	panic("simulated panic in tasks.Start")
}

// newBugConditionCompactProcessor creates a CompactProcessor for bug condition tests.
func newBugConditionCompactProcessor(
	llm types.LLMClient,
	sessions *SessionManager,
	profiles types.ProfileStore,
	cache types.CacheStore,
	vectorStore types.VectorStore,
	embedding types.EmbeddingProvider,
	tasks types.TaskTracker,
) *CompactProcessor {
	return NewCompactProcessor(
		llm, sessions, profiles, cache, vectorStore, embedding,
		tasks, nil, nil, nil,
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
}


// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.1: External calls have no context timeout
// Expected behavior: executeCompact uses a context with deadline
// Current bug: uses context.Background() with no deadline
// **Validates: Requirements 1.1**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_1_NoContextTimeout(t *testing.T) {
	// We use a mock LLM that captures the context passed to Complete.
	// The expected (correct) behavior is that the context has a deadline.
	// The current buggy code passes context.Background() with no deadline.
	var capturedCtx context.Context
	llm := &mock.MockLLMClient{
		CompleteFunc: func(ctx context.Context, req types.LLMRequest) (*types.LLMResponse, error) {
			capturedCtx = ctx
			return &types.LLMResponse{
				Content:          "Summary of conversation.",
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
				Model:            "mock",
			}, nil
		},
	}

	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, nil)

	snapshot := &types.Session{
		ID: "s-timeout", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{{Role: "user", Content: "hello world"}},
		Metadata: map[string]interface{}{},
	}
	// Pre-fill semaphore and activeLock so executeCompact can release them.
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)

	err := processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-timeout",
	}, snapshot)
	if err != nil {
		t.Fatalf("executeCompact failed: %v", err)
	}

	if capturedCtx == nil {
		t.Fatal("LLM Complete was never called")
	}

	// EXPECTED (correct) behavior: the context should have a deadline.
	// BUG: context.Background() has no deadline, so this assertion FAILS on unfixed code.
	_, hasDeadline := capturedCtx.Deadline()
	if !hasDeadline {
		t.Error("Bug 1.1 confirmed: LLM call context has no deadline — external calls lack timeout protection")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.3: Compact goroutine has no panic recovery
// Expected behavior: panic is recovered, semaphore is released
// Current bug: panic causes goroutine crash, semaphore leaks
// **Validates: Requirements 1.3**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_3_NoPanicRecovery(t *testing.T) {
	// Bug 1.3: The compact goroutine in EvaluateAndTrigger has no panic recovery.
	// We test this by using a panicTaskTracker that panics on Start, which is
	// called inside the goroutine BEFORE executeCompact. If the goroutine has
	// proper panic recovery, the semaphore and activeLock will be released.
	// Without recovery, the goroutine crashes and resources leak permanently.

	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			return &types.LLMResponse{Content: "Summary.", Model: "mock"}, nil
		},
	}

	// Use panicTaskTracker — panics on Start, which is called inside the goroutine.
	panicTasks := newPanicTaskTracker()

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, panicTasks)

	// Create a session that will trigger compact.
	session := &types.Session{
		ID: "s-panic", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{{Role: "user", Content: "trigger compact with enough content to pass threshold"}},
		Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), session)

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-panic"}

	// Call EvaluateAndTrigger — this launches the goroutine that will panic.
	triggered, _, err := processor.EvaluateAndTrigger(ctx, rc, session)
	if err != nil {
		t.Fatalf("EvaluateAndTrigger failed: %v", err)
	}
	if !triggered {
		t.Fatal("compact was not triggered")
	}

	// Wait for the goroutine to complete (panic + recovery or crash).
	time.Sleep(500 * time.Millisecond)

	// EXPECTED (correct) behavior: after panic recovery, semaphore should be empty
	// (the slot was released) and activeLock should be deleted.
	// BUG: without recovery, semaphore slot leaks (len > 0) and activeLock persists.

	semLen := len(processor.semaphore)
	if semLen != 0 {
		t.Errorf("Bug 1.3 confirmed: semaphore has %d slots occupied after panic — expected 0 (slot should be released by recovery)", semLen)
	}

	_, lockHeld := processor.activeLocks.Load("s-panic")
	if lockHeld {
		t.Error("Bug 1.3 confirmed: activeLock still held after panic — should be released by recovery")
	}
}


// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.7: executeCompact never replaces messages with summary
// Expected behavior: after compact, message count should decrease
// Current bug: messages remain unchanged
// **Validates: Requirements 1.7**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_7_CompactDoesNotReduceMessages(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			return &types.LLMResponse{
				Content: "User discussed Go programming and prefers concise answers.",
				Model:   "mock",
			}, nil
		},
	}

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, nil)

	// Create a session with 20 messages.
	originalMsgCount := 20
	msgs := make([]types.Message, originalMsgCount)
	for i := range msgs {
		msgs[i] = types.Message{Role: "user", Content: fmt.Sprintf("message %d with some content", i)}
	}
	snapshot := &types.Session{
		ID: "s-compact", TenantID: "t1", UserID: "u1",
		Messages: msgs, Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), snapshot)

	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)

	err := processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-compact",
	}, snapshot)
	if err != nil {
		t.Fatalf("executeCompact failed: %v", err)
	}

	// Reload from store to see what was persisted.
	saved, _ := store.Load(context.Background(), "t1", "u1", "s-compact")
	if saved == nil {
		t.Fatal("session not found in store after compact")
	}

	// EXPECTED (correct) behavior: message count should be less than original
	// (1 summary + RecentRawTurnCount recent messages).
	// BUG: messages are never replaced, so count stays the same.
	if len(saved.Messages) >= originalMsgCount {
		t.Errorf("Bug 1.7 confirmed: message count after compact is %d (was %d) — compact did not reduce messages",
			len(saved.Messages), originalMsgCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.8: SourceTurnStart is always 0
// Expected behavior: second compact has SourceTurnStart > 0
// Current bug: hardcoded to 0
// **Validates: Requirements 1.8**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_8_SourceTurnStartAlwaysZero(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			return &types.LLMResponse{Content: "Summary.", Model: "mock"}, nil
		},
	}

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, nil)

	// First compact.
	snapshot1 := &types.Session{
		ID: "s-turn", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{
			{Role: "user", Content: "msg 1"},
			{Role: "assistant", Content: "reply 1"},
			{Role: "user", Content: "msg 2"},
		},
		Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), snapshot1)
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot1.ID, true)
	_ = processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-turn",
	}, snapshot1)

	// Reload session after first compact to get updated metadata.
	saved, _ := store.Load(context.Background(), "t1", "u1", "s-turn")
	if saved == nil {
		t.Fatal("session not found after first compact")
	}

	// Add more messages for second compact.
	saved.Messages = append(saved.Messages,
		types.Message{Role: "user", Content: "msg 3"},
		types.Message{Role: "assistant", Content: "reply 3"},
	)
	_ = store.Save(context.Background(), saved)

	// Second compact.
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(saved.ID, true)
	_ = processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-turn",
	}, saved)

	// Check the second checkpoint's SourceTurnStart.
	if store.CheckpointCount() < 2 {
		t.Fatalf("expected at least 2 checkpoints, got %d", store.CheckpointCount())
	}

	secondCheckpoint := store.checkpoints[1]
	// EXPECTED (correct) behavior: SourceTurnStart > 0 for second compact.
	// BUG: always hardcoded to 0.
	if secondCheckpoint.SourceTurnStart == 0 {
		t.Error("Bug 1.8 confirmed: second compact SourceTurnStart is 0 — should be > 0 for incremental compaction")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.9: extractFacts splits poorly on structured text
// Expected behavior: each numbered item is a separate fact
// Current bug: splits only on sentence-ending punctuation
// **Validates: Requirements 1.9**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_9_ExtractFactsStructuredText(t *testing.T) {
	input := "Key findings:\n1. User prefers Go over Python\n2. Team uses microservices architecture\n- Deadline is next Friday"

	facts := extractFacts(input)

	// EXPECTED (correct) behavior: each list item should be a separate, complete fact.
	// We expect at least 3 distinct facts from the 3 list items.
	// BUG: splitSentences only splits on .!? so the entire input may become
	// fragmented or merged incorrectly.

	// Check that we get meaningful facts (not fragments).
	foundGo := false
	foundMicroservices := false
	foundDeadline := false
	for _, fact := range facts {
		if strings.Contains(fact, "Go over Python") || strings.Contains(fact, "prefers Go") {
			foundGo = true
		}
		if strings.Contains(fact, "microservices") {
			foundMicroservices = true
		}
		if strings.Contains(fact, "Deadline") || strings.Contains(fact, "Friday") {
			foundDeadline = true
		}
	}

	// The "- Deadline is next Friday" has no sentence-ending punctuation,
	// so it gets merged with the previous item or lost.
	// Also "Key findings:" prefix may contaminate the first fact.
	if !foundGo || !foundMicroservices || !foundDeadline {
		t.Errorf("Bug 1.9 confirmed: extractFacts produced fragmented results from structured text.\nGot %d facts: %v\nExpected separate facts for Go preference, microservices, and deadline",
			len(facts), facts)
	}

	// Additionally verify each fact is a complete semantic unit (not a fragment).
	for _, fact := range facts {
		// A fact should not start with a number prefix like "1." if it's properly parsed.
		// But more importantly, it should not contain multiple list items merged together.
		if strings.Contains(fact, "\n") {
			t.Errorf("Bug 1.9 confirmed: fact contains newline (merged items): %q", fact)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.10: mergeProfile overwrites summary
// Expected behavior: after two compacts, profile contains both summaries
// Current bug: second summary overwrites first
// **Validates: Requirements 1.10**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_10_ProfileSummaryOverwrite(t *testing.T) {
	profileStore := &memoryProfileStore{profiles: make(map[string]*types.UserProfile)}

	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	callCount := 0
	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.LLMResponse{Content: "User is a Go developer who prefers concise code.", Model: "mock"}, nil
			}
			return &types.LLMResponse{Content: "User works on distributed systems and uses Kubernetes.", Model: "mock"}, nil
		},
	}

	processor := NewCompactProcessor(llm, sessions, profileStore, cache, nil, nil, nil, nil, nil, nil,
		&CompactConfig{
			CompactBudgetRatio: 1, CompactTokenThreshold: 1, CompactTurnThreshold: 1,
			CompactIntervalMin: 60, CompactTimeoutSec: 5, MaxConcurrentCompacts: 2, TokenBudget: 32000,
		}, nil)

	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-profile"}

	// First compact.
	snap1 := &types.Session{
		ID: "s-profile", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{{Role: "user", Content: "I am a Go developer."}},
		Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), snap1)
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snap1.ID, true)
	_ = processor.executeCompact(context.Background(), rc, snap1)

	// Second compact.
	snap2 := &types.Session{
		ID: "s-profile", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{{Role: "user", Content: "I work on distributed systems."}},
		Metadata: map[string]interface{}{},
	}
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snap2.ID, true)
	_ = processor.executeCompact(context.Background(), rc, snap2)

	// Check profile.
	profile, _ := profileStore.Load(context.Background(), "t1", "u1")
	if profile == nil {
		t.Fatal("profile not found")
	}

	// EXPECTED (correct) behavior: profile.Summary contains content from BOTH compacts.
	// BUG: second compact overwrites first summary.
	if !strings.Contains(profile.Summary, "Go") {
		t.Error("Bug 1.10 confirmed: profile summary lost first compact content ('Go developer') — overwritten by second compact")
	}
	if !strings.Contains(profile.Summary, "Kubernetes") || !strings.Contains(profile.Summary, "distributed") {
		t.Error("Bug 1.10: profile summary missing second compact content")
	}
}


// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.11: Preference extraction matches negations
// Expected behavior: "I don't like X" is NOT stored as preference
// Current bug: strings.Contains("like") matches negations
// **Validates: Requirements 1.11**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_11_PreferenceMatchesNegation(t *testing.T) {
	profileStore := &memoryProfileStore{profiles: make(map[string]*types.UserProfile)}

	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			// The summary contains negation and false-match phrases.
			return &types.LLMResponse{
				Content: "I don't like spicy food. Likewise, the team agreed. I prefer dark mode.",
				Model:   "mock",
			}, nil
		},
	}

	processor := NewCompactProcessor(llm, sessions, profileStore, cache, nil, nil, nil, nil, nil, nil,
		&CompactConfig{
			CompactBudgetRatio: 1, CompactTokenThreshold: 1, CompactTurnThreshold: 1,
			CompactIntervalMin: 60, CompactTimeoutSec: 5, MaxConcurrentCompacts: 2, TokenBudget: 32000,
		}, nil)

	snapshot := &types.Session{
		ID: "s-pref", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{{Role: "user", Content: "test"}},
		Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), snapshot)
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)
	_ = processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-pref",
	}, snapshot)

	profile, _ := profileStore.Load(context.Background(), "t1", "u1")
	if profile == nil {
		t.Fatal("profile not found")
	}

	// EXPECTED (correct) behavior:
	// - "I don't like spicy food" should NOT be a preference (negation)
	// - "Likewise, the team agreed" should NOT be a preference (false match on "like")
	// - "I prefer dark mode" SHOULD be a preference
	// BUG: strings.Contains("like") matches all three.

	for _, pref := range profile.Preferences {
		lower := strings.ToLower(pref)
		if strings.Contains(lower, "don't like") || strings.Contains(lower, "spicy") {
			t.Errorf("Bug 1.11 confirmed: negation stored as preference: %q", pref)
		}
		if strings.Contains(lower, "likewise") {
			t.Errorf("Bug 1.11 confirmed: 'likewise' false-match stored as preference: %q", pref)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.12: Snapshot overwrites live session
// Expected behavior: messages added during compact are preserved
// Current bug: p.sessions.store.Save(ctx, snapshot) overwrites live session
// **Validates: Requirements 1.12**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_12_SnapshotOverwritesLiveSession(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	// LLM that takes a moment, during which we add messages to the live session.
	var addDuringCompact func()
	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			// Simulate work and add messages to the live session during compact.
			if addDuringCompact != nil {
				addDuringCompact()
			}
			return &types.LLMResponse{Content: "Summary.", Model: "mock"}, nil
		},
	}

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, nil)

	// Create initial session with messages.
	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-overwrite"}
	sess, _ := sessions.GetOrCreate(ctx, rc)
	for i := 0; i < 5; i++ {
		_ = sessions.AddMessage(ctx, sess, types.Message{
			Role: "user", Content: fmt.Sprintf("original msg %d", i),
		})
	}
	// Save to store.
	_ = store.Save(ctx, sess)

	// Clone for snapshot (simulating what EvaluateAndTrigger does).
	snapshot := sessions.Clone(sess)
	snapshot.ID = sess.ID

	// Set up the function to add messages during compact.
	addDuringCompact = func() {
		// Add a new message directly to the store (simulating concurrent AddMessage).
		liveSession, _ := store.Load(ctx, "t1", "u1", "s-overwrite")
		if liveSession != nil {
			liveSession.Messages = append(liveSession.Messages, types.Message{
				Role: "user", Content: "message added during compact",
			})
			_ = store.Save(ctx, liveSession)
		}
	}

	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)
	err := processor.executeCompact(ctx, rc, snapshot)
	if err != nil {
		t.Fatalf("executeCompact failed: %v", err)
	}

	// Check if the message added during compact is preserved.
	saved, _ := store.Load(ctx, "t1", "u1", "s-overwrite")
	if saved == nil {
		t.Fatal("session not found after compact")
	}

	// EXPECTED (correct) behavior: the message added during compact should be preserved.
	// BUG: snapshot.Save overwrites the live session, losing the new message.
	found := false
	for _, msg := range saved.Messages {
		if strings.Contains(msg.Content, "message added during compact") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Bug 1.12 confirmed: message added during compact was lost — snapshot overwrote live session")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// memoryProfileStore — in-memory ProfileStore for tests
// ═══════════════════════════════════════════════════════════════════════════════

type memoryProfileStore struct {
	mu       sync.Mutex
	profiles map[string]*types.UserProfile
}

func (m *memoryProfileStore) Load(_ context.Context, tenantID, userID string) (*types.UserProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.profiles[tenantID+":"+userID], nil
}

func (m *memoryProfileStore) Upsert(_ context.Context, profile *types.UserProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profiles[profile.TenantID+":"+profile.UserID] = profile
	return nil
}

func (m *memoryProfileStore) Search(_ context.Context, _, _, _ string, _ int) ([]types.ContentBlock, error) {
	return nil, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.13 (CJK Token estimation) — tested in context_builder_bug_condition_test.go
// Bug 1.14 (N+1 embedding) — tested in context_builder_bug_condition_test.go
// Bug 1.15 (hardcoded budget) — tested in context_builder_bug_condition_test.go
// Bug 1.16 (UTF-8 truncation) — tested in context_builder_bug_condition_test.go
// Bug 1.17 (role loss) — tested in context_builder_bug_condition_test.go
// Bug 1.18 (L0 skip) — tested in context_builder_bug_condition_test.go
// ═══════════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════════
// Verify extractFacts handles input without sentence-ending punctuation
// This is a supplementary check for Bug 1.9
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_9_ExtractFactsBulletList(t *testing.T) {
	// Input with NO sentence-ending punctuation — just list items.
	input := "Key points\n1) User prefers Go\n2) Team uses Docker\n- Deploy on Friday"
	facts := extractFacts(input)

	// EXPECTED: at least 3 facts, one per list item.
	// BUG: no sentence-ending punctuation means the whole thing becomes one "fact"
	// or gets split incorrectly at newlines.
	if len(facts) < 3 {
		t.Errorf("Bug 1.9 confirmed: extractFacts returned %d facts from structured list (expected >= 3): %v",
			len(facts), facts)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.16 supplementary: demoteBlock truncates by bytes, breaking UTF-8
// **Validates: Requirements 1.16**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_16_DemoteBlockUTF8Truncation(t *testing.T) {
	// Create a string of 300 CJK characters (each 3 bytes in UTF-8 = 900 bytes).
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteRune('中')
	}
	cjkContent := sb.String()

	blk := types.ContentBlock{
		URI:     "test://cjk",
		Level:   types.ContentL2,
		Content: cjkContent,
		Source:  "memory",
		Score:   0.5,
		Tokens:  estimateTokens(cjkContent),
	}

	// Demote to L0 (truncates to 200).
	demoted := demoteBlock(blk, types.ContentL0)

	// EXPECTED (correct) behavior: truncated content is valid UTF-8.
	// BUG: content[:200] truncates at byte 200, which is in the middle of a
	// 3-byte CJK character, producing invalid UTF-8.
	if !utf8.ValidString(demoted.Content) {
		t.Error("Bug 1.16 confirmed: demoteBlock produced invalid UTF-8 after truncating CJK text")
	}
}
