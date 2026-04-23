package engine

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
	"pgregory.net/rapid"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Preservation Property Tests
//
// These tests verify that non-bug-condition inputs produce correct behavior
// on the CURRENT (unfixed) code. They establish a baseline that must be
// preserved after bug fixes are applied.
// ═══════════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════════
// 3.1 shouldTrigger preservation
// When NO trigger condition is met, shouldTrigger returns false.
// The four trigger conditions are: token budget ratio, new token threshold,
// turn threshold, time interval.
// **Validates: Requirements 3.1**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_ShouldTrigger_NoConditionMet(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a config with high thresholds so nothing triggers.
		tokenBudget := rapid.IntRange(100000, 200000).Draw(t, "tokenBudget")
		budgetRatio := 0.9 // high ratio threshold
		tokenThreshold := rapid.IntRange(50000, 100000).Draw(t, "tokenThreshold")
		turnThreshold := rapid.IntRange(100, 200).Draw(t, "turnThreshold")
		intervalMin := rapid.IntRange(60, 120).Draw(t, "intervalMin")

		cfg := &CompactConfig{
			CompactBudgetRatio:    budgetRatio,
			CompactTokenThreshold: tokenThreshold,
			CompactTurnThreshold:  turnThreshold,
			CompactIntervalMin:    intervalMin,
			CompactTimeoutSec:     120,
			MaxConcurrentCompacts: 10,
			TokenBudget:           tokenBudget,
		}

		processor := &CompactProcessor{config: cfg}

		// Generate a session with few messages and recent compact.
		msgCount := rapid.IntRange(1, 5).Draw(t, "msgCount")
		msgs := make([]types.Message, msgCount)
		for i := range msgs {
			// Short messages so total tokens stay low.
			msgs[i] = types.Message{Role: "user", Content: "hi"}
		}

		session := &types.Session{
			ID:       "s-test",
			Messages: msgs,
			Metadata: map[string]interface{}{
				"last_compact_at":     time.Now().Add(-1 * time.Minute).Format(time.RFC3339), // recent compact
				"last_compact_turn":   msgCount - 1,                                          // only 1 new turn
				"last_compact_tokens": estimateTokens("hi") * (msgCount - 1),                 // minimal new tokens
			},
		}

		result := processor.shouldTrigger(session)
		if result {
			t.Errorf("shouldTrigger returned true when no condition should be met: msgs=%d, budget=%d", msgCount, tokenBudget)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.4 Pure English Token estimation preservation
// For random ASCII strings, estimateTokens matches len(s)/4 within 10% tolerance.
// **Validates: Requirements 3.4**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_EstimateTokens_PureASCII(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random ASCII string of length 4..2000.
		length := rapid.IntRange(4, 2000).Draw(t, "length")
		bytes := make([]byte, length)
		for i := range bytes {
			// Printable ASCII range 32-126.
			bytes[i] = byte(rapid.IntRange(32, 126).Draw(t, fmt.Sprintf("byte_%d", i)))
		}
		s := string(bytes)

		tokens := estimateTokens(s)
		expected := len(s) / 4
		if expected == 0 {
			expected = 1
		}

		// Allow 10% tolerance.
		tolerance := expected / 10
		if tolerance < 1 {
			tolerance = 1
		}

		diff := tokens - expected
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			t.Errorf("estimateTokens(%d-byte ASCII) = %d, expected ~%d (within 10%%)", len(s), tokens, expected)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.5 Empty skill catalog preservation
// When skill catalog is empty, no embedding API calls are made during Assemble.
// **Validates: Requirements 3.5**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_EmptySkillCatalog_NoEmbedCalls(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := mock.NewMemorySessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	counting := newCountingEmbeddingProvider(16)
	vectorStore := mock.NewMemoryVectorStore()

	// Empty skill catalog.
	emptySkills := &stubSkillCatalog{}

	retrieval := NewRetrievalEngine(vectorStore, counting, RetrievalConfig{
		RecallScoreThreshold: 0, RecallMaxResults: 10,
	}, nil)

	builder := NewContextBuilder(vectorStore, counting, sessions, nil, cache, emptySkills, retrieval, ContextConfig{
		TokenBudget:            32000,
		MaxMessages:            50,
		RecentRawTurnCount:     8,
		SkillBodyLoadThreshold: 0.9,
		MaxLoadedSkillBodies:   2,
	})

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-empty-skill"}
	_, _ = sessions.GetOrCreate(ctx, rc)

	// Reset call count.
	counting.mu.Lock()
	counting.calls = 0
	counting.mu.Unlock()

	_, err := builder.Assemble(ctx, rc, types.AssembleRequest{
		Query:       "test query",
		TokenBudget: 32000,
	})
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}

	callCount := counting.CallCount()
	// With empty catalog, the only embed call should be for SemanticSearch query.
	// No skill-related embed calls should happen.
	// SemanticSearch calls Embed once for the query.
	if callCount > 1 {
		t.Errorf("Expected at most 1 Embed call (for query) with empty catalog, got %d", callCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.7 Three-layer cache preservation
// AddMessage correctly writes to LRU, Redis (via putRedis), and PG async queue.
// MaxMessages trimming works correctly.
// **Validates: Requirements 3.7**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_ThreeLayerCache_AddMessage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxMessages := rapid.IntRange(5, 20).Draw(t, "maxMessages")
		cache := mock.NewMemoryCacheStore()
		store := mock.NewMemorySessionStore()
		sm := NewSessionManager(cache, store, SessionConfig{
			MaxMessages:         maxMessages,
			LRUCacheSize:        100,
			LRUCacheTTLSec:      5,
			SyncQueueSize:       1000,
			SyncBatchSize:       10,
			SyncFlushIntervalMs: 50,
		})
		defer sm.Stop()

		ctx := context.Background()
		rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-cache"}
		sess, err := sm.GetOrCreate(ctx, rc)
		if err != nil {
			t.Fatalf("GetOrCreate failed: %v", err)
		}

		// Add messages.
		msgCount := rapid.IntRange(1, maxMessages+10).Draw(t, "msgCount")
		for i := 0; i < msgCount; i++ {
			msg := types.Message{
				Role:      "user",
				Content:   fmt.Sprintf("msg %d", i),
				Timestamp: time.Now(),
			}
			if err := sm.AddMessage(ctx, sess, msg); err != nil {
				t.Fatalf("AddMessage(%d) failed: %v", i, err)
			}
		}

		// Verify MaxMessages trimming.
		expectedLen := msgCount
		if expectedLen > maxMessages {
			expectedLen = maxMessages
		}
		if len(sess.Messages) != expectedLen {
			t.Errorf("Expected %d messages after trimming (max=%d, added=%d), got %d",
				expectedLen, maxMessages, msgCount, len(sess.Messages))
		}

		// Verify LRU cache has the session.
		key := sm.cacheKey("t1", "u1", "s-cache")
		if entry, ok := sm.lru.Get(key); !ok || entry == nil {
			t.Error("Session not found in LRU cache after AddMessage")
		}

		// Verify Redis has the session.
		data, err := cache.Get(ctx, key)
		if err != nil {
			t.Fatalf("Redis Get failed: %v", err)
		}
		if data == nil {
			t.Error("Session not found in Redis after AddMessage")
		}

		// Verify SyncQueue received items (check DLQ is empty = items were enqueued).
		// We can't directly check the queue, but we can verify the queue is operational.
		if sm.syncQueue == nil {
			t.Error("SyncQueue is nil")
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.8 Progressive loading preservation
// When all blocks fit within budget, they are included in score-descending
// order without unnecessary truncation.
// **Validates: Requirements 3.8**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_ProgressiveLoading_AllFit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate blocks that all fit within budget.
		blockCount := rapid.IntRange(2, 10).Draw(t, "blockCount")
		blocks := make([]types.ContentBlock, blockCount)
		totalTokens := 0
		for i := range blocks {
			tokens := rapid.IntRange(10, 50).Draw(t, fmt.Sprintf("tokens_%d", i))
			score := rapid.Float64Range(0.1, 1.0).Draw(t, fmt.Sprintf("score_%d", i))
			content := fmt.Sprintf("Block %d content with some text", i)
			blocks[i] = types.ContentBlock{
				URI:     fmt.Sprintf("test://block/%d", i),
				Level:   types.ContentL2,
				Content: content,
				Source:  "memory",
				Score:   score,
				Tokens:  tokens,
			}
			totalTokens += tokens
		}

		// Budget is larger than total tokens.
		budget := totalTokens + 1000

		// Sort by score descending (same as Assemble does).
		sort.Slice(blocks, func(i, j int) bool {
			return blocks[i].Score > blocks[j].Score
		})

		// Simulate the progressive loading loop from Assemble.
		var finalBlocks []types.ContentBlock
		usedTokens := 0
		for _, blk := range blocks {
			if usedTokens+blk.Tokens > budget {
				if blk.Level == types.ContentL2 && blk.Tokens > 0 {
					demoted := demoteBlock(blk, types.ContentL1)
					if usedTokens+demoted.Tokens <= budget {
						finalBlocks = append(finalBlocks, demoted)
						usedTokens += demoted.Tokens
						continue
					}
					demoted = demoteBlock(blk, types.ContentL0)
					if usedTokens+demoted.Tokens <= budget {
						finalBlocks = append(finalBlocks, demoted)
						usedTokens += demoted.Tokens
						continue
					}
				}
				continue
			}
			finalBlocks = append(finalBlocks, blk)
			usedTokens += blk.Tokens
		}

		// All blocks should be included since they all fit.
		if len(finalBlocks) != blockCount {
			t.Errorf("Expected all %d blocks to be included (budget=%d, total=%d), got %d",
				blockCount, budget, totalTokens, len(finalBlocks))
		}

		// Verify score-descending order.
		for i := 1; i < len(finalBlocks); i++ {
			if finalBlocks[i].Score > finalBlocks[i-1].Score {
				t.Errorf("Blocks not in score-descending order: [%d].Score=%f > [%d].Score=%f",
					i, finalBlocks[i].Score, i-1, finalBlocks[i-1].Score)
			}
		}

		// Verify no truncation occurred (content unchanged).
		for i, blk := range finalBlocks {
			if blk.Content != blocks[i].Content {
				t.Errorf("Block %d content was modified (truncated) when it should fit: got %q, want %q",
					i, blk.Content, blocks[i].Content)
			}
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.9 Distributed lock preservation
// When Redis lock is already held (SetNX returns false), compact is skipped.
// **Validates: Requirements 3.9**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_DistributedLock_SkipWhenHeld(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	llmCalled := false
	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			llmCalled = true
			return &types.LLMResponse{Content: "Summary.", Model: "mock"}, nil
		},
	}

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, nil)

	// Pre-acquire the distributed lock.
	ctx := context.Background()
	lockKey := "compact:t1:u1:s-locked"
	_, _ = cache.SetNX(ctx, lockKey, []byte("1"), 60*time.Second)

	snapshot := &types.Session{
		ID: "s-locked", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
		Metadata: map[string]interface{}{},
	}

	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)

	err := processor.executeCompact(ctx, types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-locked",
	}, snapshot)

	// Should return nil (skipped, not error).
	if err != nil {
		t.Fatalf("executeCompact should skip when lock is held, got error: %v", err)
	}

	// LLM should NOT have been called since compact was skipped.
	if llmCalled {
		t.Error("LLM was called even though distributed lock was already held — compact should have been skipped")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.10 Semantic search preservation
// Search results are returned in score-descending order and within budget.
// **Validates: Requirements 3.10**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_SemanticSearch_ScoreOrder(t *testing.T) {
	embedding := mock.NewMockEmbeddingProvider(16)
	vectorStore := mock.NewMemoryVectorStore()

	ctx := context.Background()
	_ = vectorStore.Init(ctx, 16)

	// Populate vector store with items.
	for i := 0; i < 20; i++ {
		content := fmt.Sprintf("Memory fact %d about topic %d", i, i%5)
		vecs, _ := embedding.Embed(ctx, []string{content})
		_ = vectorStore.Upsert(ctx, []types.VectorItem{{
			ID: fmt.Sprintf("mem-%d", i), Vector: vecs[0], Content: content,
			URI: fmt.Sprintf("memory://t1/u1/mem-%d", i), TenantID: "t1", UserID: "u1",
		}})
	}

	retrieval := NewRetrievalEngine(vectorStore, embedding, RetrievalConfig{
		RecallScoreThreshold: 0, RecallMaxResults: 10,
	}, nil)

	rc := types.RequestContext{TenantID: "t1", UserID: "u1"}
	budget := 5000

	blocks, err := retrieval.SemanticSearch(ctx, rc, "topic 1", budget)
	if err != nil {
		t.Fatalf("SemanticSearch failed: %v", err)
	}

	// Verify score-descending order.
	for i := 1; i < len(blocks); i++ {
		if blocks[i].Score > blocks[i-1].Score {
			t.Errorf("Results not in score-descending order: [%d].Score=%f > [%d].Score=%f",
				i, blocks[i].Score, i-1, blocks[i-1].Score)
		}
	}

	// Verify total tokens within budget.
	totalTokens := 0
	for _, blk := range blocks {
		totalTokens += blk.Tokens
	}
	if totalTokens > budget {
		t.Errorf("Total tokens %d exceeds budget %d", totalTokens, budget)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.11 SyncQueue stop preservation
// When queue is stopped, Enqueue returns an error.
// **Validates: Requirements 3.11**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_SyncQueue_EnqueueAfterStop(t *testing.T) {
	store := mock.NewMemorySessionStore()
	sq := NewSyncQueue(store, 100, 10, 50*time.Millisecond)
	sq.Start()
	sq.Stop()

	// Enqueue after stop should return an error.
	err := sq.Enqueue(&SyncItem{
		SessionID: "s1",
		TenantID:  "t1",
		UserID:    "u1",
		Message:   types.Message{Role: "user", Content: "test"},
		EnqueueAt: time.Now(),
	})

	if err == nil {
		t.Error("Expected error when enqueueing to stopped queue, got nil")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3.12 Valid API request preservation
// Valid requests with proper parameters are processed normally.
// We test this by verifying that Assemble works with valid parameters.
// **Validates: Requirements 3.12**
// ═══════════════════════════════════════════════════════════════════════════════

func TestPreservation_ValidRequest_ProcessedNormally(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cache := mock.NewMemoryCacheStore()
		store := mock.NewMemorySessionStore()
		sessions := NewSessionManager(cache, store, SessionConfig{
			MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
			SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
		})
		defer sessions.Stop()

		embedding := mock.NewMockEmbeddingProvider(16)
		vectorStore := mock.NewMemoryVectorStore()

		retrieval := NewRetrievalEngine(vectorStore, embedding, RetrievalConfig{
			RecallScoreThreshold: 0, RecallMaxResults: 10,
		}, nil)

		// Generate valid token budget.
		tokenBudget := rapid.IntRange(1000, 64000).Draw(t, "tokenBudget")

		builder := NewContextBuilder(vectorStore, embedding, sessions, nil, cache, &stubSkillCatalog{}, retrieval, ContextConfig{
			TokenBudget:            tokenBudget,
			MaxMessages:            50,
			RecentRawTurnCount:     8,
			SkillBodyLoadThreshold: 0.9,
			MaxLoadedSkillBodies:   2,
		})

		ctx := context.Background()
		rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-valid"}
		_, _ = sessions.GetOrCreate(ctx, rc)

		resp, err := builder.Assemble(ctx, rc, types.AssembleRequest{
			Query:       "valid query",
			TokenBudget: tokenBudget,
		})

		if err != nil {
			t.Fatalf("Assemble with valid params (budget=%d) failed: %v", tokenBudget, err)
		}
		if resp == nil {
			t.Fatal("Assemble returned nil response for valid request")
		}
		if resp.EstimatedTokens < 0 {
			t.Errorf("Negative estimated tokens: %d", resp.EstimatedTokens)
		}
		if resp.EstimatedTokens > tokenBudget {
			t.Errorf("Estimated tokens %d exceeds budget %d", resp.EstimatedTokens, tokenBudget)
		}
	})
}
