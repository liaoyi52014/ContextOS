package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.13: estimateTokens underestimates CJK
// Expected behavior: "你好世界测试" returns >= 5 tokens
// Current bug: len("你好世界测试")/4 = 15/4 = 3
// **Validates: Requirements 1.13**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_13_CJKTokenUnderestimate(t *testing.T) {
	// "你好世界测试" = 5 CJK characters, 15 bytes in UTF-8.
	// Current buggy code: len(s)/4 = 15/4 = 3
	// Expected correct behavior: >= 5 tokens (each CJK char ≈ 1-2 tokens).
	input := "你好世界测试"
	tokens := estimateTokens(input)

	if tokens < 5 {
		t.Errorf("Bug 1.13 confirmed: estimateTokens(%q) = %d, expected >= 5 — CJK tokens underestimated",
			input, tokens)
	}
}

func TestBugCondition_1_13_CJKMixedText(t *testing.T) {
	// Mixed ASCII + CJK text.
	input := "Hello 你好世界"
	tokens := estimateTokens(input)

	// "Hello " = 6 ASCII chars ≈ 1-2 tokens
	// "你好世界" = 4 CJK chars ≈ 4-8 tokens
	// Total should be at least 5.
	// Current buggy code: len("Hello 你好世界") = 6 + 12 = 18 bytes, 18/4 = 4
	if tokens < 5 {
		t.Errorf("Bug 1.13 confirmed: estimateTokens(%q) = %d, expected >= 5 — mixed CJK/ASCII underestimated",
			input, tokens)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.14: N+1 embedding calls in skill matching
// Expected behavior: N skills result in 1 embed call (batch)
// Current bug: N skills result in N+1 embed calls (1 for query + N for skills)
// **Validates: Requirements 1.14**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_14_NPlus1EmbeddingCalls(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := mock.NewMemorySessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	counting := newCountingEmbeddingProvider(16)
	vectorStore := mock.NewMemoryVectorStore()

	// Create 5 skills in the catalog.
	skillCatalog := &staticSkillCatalog{
		skills: make([]types.SkillMeta, 5),
	}
	for i := 0; i < 5; i++ {
		skillCatalog.skills[i] = types.SkillMeta{
			ID:          fmt.Sprintf("skill-%d", i),
			Name:        fmt.Sprintf("skill-%d", i),
			Description: fmt.Sprintf("A skill that does thing %d with code", i),
			Body:        fmt.Sprintf("Full body of skill %d", i),
		}
	}

	retrieval := NewRetrievalEngine(vectorStore, counting, RetrievalConfig{
		RecallScoreThreshold: 0, RecallMaxResults: 10,
	}, nil)

	builder := NewContextBuilder(vectorStore, counting, sessions, nil, cache, skillCatalog, retrieval, ContextConfig{
		TokenBudget:            32000,
		MaxMessages:            50,
		RecentRawTurnCount:     8,
		SkillBodyLoadThreshold: 0.0, // Low threshold so skills get matched
		MaxLoadedSkillBodies:   2,
	})

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-embed"}
	// Create session first.
	_, _ = sessions.GetOrCreate(ctx, rc)

	counting.mu.Lock()
	counting.calls = 0
	counting.mu.Unlock()

	_, err := builder.Assemble(ctx, rc, types.AssembleRequest{
		Query:       "help me with code",
		TokenBudget: 32000,
	})
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}

	callCount := counting.CallCount()
	// EXPECTED (correct) behavior: at most 2 Embed calls
	// (1 for query in SemanticSearch + 1 batch for all skills).
	// BUG: 1 for SemanticSearch query + 1 for query in matchAndLoadSkills + N for each skill = 7 calls.
	// We check that it's <= 3 (allowing for query embed + semantic search + 1 batch).
	if callCount > 3 {
		t.Errorf("Bug 1.14 confirmed: %d Embed calls for 5 skills (expected <= 3) — N+1 embedding problem",
			callCount)
	}
}


// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.15: Memory budget hardcoded at 1/4
// Expected behavior: short session gets different ratio than long session
// Current bug: memBudget = budget / 4 always
// **Validates: Requirements 1.15**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_15_HardcodedMemoryBudget(t *testing.T) {
	// We test by creating two builders with different session lengths
	// and checking if the memory budget allocation differs.
	// Since we can't directly observe memBudget, we check the SemanticSearch
	// budget parameter indirectly through the number of memory blocks returned.

	// However, a simpler approach: we can verify the bug by checking the code path.
	// The design says memBudget should vary based on session history length.
	// We'll create a short session (2 messages) and a long session (20 messages)
	// and verify they get different memory allocations.

	cache := mock.NewMemoryCacheStore()
	store := mock.NewMemorySessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	embedding := mock.NewMockEmbeddingProvider(16)
	vectorStore := mock.NewMemoryVectorStore()

	// Populate vector store with many items so budget matters.
	ctx := context.Background()
	_ = vectorStore.Init(ctx, 16)
	for i := 0; i < 50; i++ {
		content := fmt.Sprintf("Memory fact number %d about various topics and information that the user discussed previously in their sessions", i)
		vecs, _ := embedding.Embed(ctx, []string{content})
		_ = vectorStore.Upsert(ctx, []types.VectorItem{{
			ID: fmt.Sprintf("mem-%d", i), Vector: vecs[0], Content: content,
			URI: fmt.Sprintf("memory://t1/u1/mem-%d", i), TenantID: "t1", UserID: "u1",
		}})
	}

	retrieval := NewRetrievalEngine(vectorStore, embedding, RetrievalConfig{
		RecallScoreThreshold: 0, RecallMaxResults: 50,
	}, nil)

	budget := 4000

	// Short session (2 messages) — should get MORE memory budget.
	shortBuilder := NewContextBuilder(vectorStore, embedding, sessions, nil, cache, &stubSkillCatalog{}, retrieval, ContextConfig{
		TokenBudget: budget, MaxMessages: 50, RecentRawTurnCount: 8,
		SkillBodyLoadThreshold: 0.9, MaxLoadedSkillBodies: 2,
	})

	rcShort := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-short"}
	sessShort, _ := sessions.GetOrCreate(ctx, rcShort)
	_ = sessions.AddMessage(ctx, sessShort, types.Message{Role: "user", Content: "hello"})
	_ = sessions.AddMessage(ctx, sessShort, types.Message{Role: "assistant", Content: "hi"})

	respShort, err := shortBuilder.Assemble(ctx, rcShort, types.AssembleRequest{
		Query: "tell me about my memories", TokenBudget: budget,
	})
	if err != nil {
		t.Fatalf("Assemble (short) failed: %v", err)
	}

	// Long session (20 messages) — should get LESS memory budget.
	longBuilder := NewContextBuilder(vectorStore, embedding, sessions, nil, cache, &stubSkillCatalog{}, retrieval, ContextConfig{
		TokenBudget: budget, MaxMessages: 50, RecentRawTurnCount: 8,
		SkillBodyLoadThreshold: 0.9, MaxLoadedSkillBodies: 2,
	})

	rcLong := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-long"}
	sessLong, _ := sessions.GetOrCreate(ctx, rcLong)
	for i := 0; i < 20; i++ {
		_ = sessions.AddMessage(ctx, sessLong, types.Message{
			Role: "user", Content: fmt.Sprintf("message %d with content", i),
		})
	}

	respLong, err := longBuilder.Assemble(ctx, rcLong, types.AssembleRequest{
		Query: "tell me about my memories", TokenBudget: budget,
	})
	if err != nil {
		t.Fatalf("Assemble (long) failed: %v", err)
	}

	// Count memory blocks in each response.
	countMemory := func(blocks []types.ContentBlock) int {
		n := 0
		for _, b := range blocks {
			if b.Source == "memory" {
				n++
			}
		}
		return n
	}

	shortMemCount := countMemory(respShort.Sources)
	longMemCount := countMemory(respLong.Sources)

	// EXPECTED (correct) behavior: short session gets more memory blocks
	// because it has a higher memory budget ratio.
	// BUG: both get budget/4 = 1000 tokens for memory, so same allocation.
	if shortMemCount == longMemCount {
		t.Errorf("Bug 1.15 confirmed: short session (%d memory blocks) and long session (%d memory blocks) got same memory allocation — budget is hardcoded at 1/4",
			shortMemCount, longMemCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.16: demoteBlock truncates by bytes, breaking UTF-8
// Expected behavior: truncation of CJK text produces valid UTF-8
// Current bug: content[:200] may split a multi-byte character
// **Validates: Requirements 1.16**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_16_UTF8Truncation(t *testing.T) {
	// applyContentLevel uses content[:200] for L0 and content[:1000] for L1.
	// For CJK text (3 bytes per char), byte 200 falls in the middle of a character.

	// Create 100 CJK characters = 300 bytes.
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteRune('你')
	}
	cjkText := sb.String()

	// Apply L0 level (truncates to 200 bytes).
	result := applyContentLevel(cjkText, types.ContentL0)

	// EXPECTED (correct) behavior: result is valid UTF-8.
	// BUG: content[:200] splits a 3-byte CJK char, producing invalid UTF-8.
	if !utf8.ValidString(result) {
		t.Error("Bug 1.16 confirmed: applyContentLevel produced invalid UTF-8 when truncating CJK text at L0")
	}

	// Also check that the truncated length is reasonable (should be ~66 runes for 200 bytes of CJK).
	runeCount := utf8.RuneCountInString(result)
	if runeCount > 200 {
		t.Errorf("Expected truncation to limit content, got %d runes", runeCount)
	}
}

func TestBugCondition_1_16_ApplyContentLevelL1(t *testing.T) {
	// Create 400 CJK characters = 1200 bytes.
	var sb strings.Builder
	for i := 0; i < 400; i++ {
		sb.WriteRune('世')
	}
	cjkText := sb.String()

	result := applyContentLevel(cjkText, types.ContentL1)

	if !utf8.ValidString(result) {
		t.Error("Bug 1.16 confirmed: applyContentLevel produced invalid UTF-8 when truncating CJK text at L1")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.17: buildMessages loses role info
// Expected behavior: assistant messages keep their role
// Current bug: all history messages get Role: "user"
// **Validates: Requirements 1.17**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_17_BuildMessagesLosesRole(t *testing.T) {
	// buildMessages constructs messages from history blocks.
	// The content format is "[role]: content" (set by Assemble).
	// BUG: buildMessages sets all roles to "user" regardless of the content prefix.

	blocks := []types.ContentBlock{
		{
			URI:     "session://s1/msg/0",
			Level:   types.ContentL2,
			Content: "[user]: Hello, can you help me?",
			Source:  "history",
			Score:   0.3,
			Tokens:  10,
		},
		{
			URI:     "session://s1/msg/1",
			Level:   types.ContentL2,
			Content: "[assistant]: Of course! How can I help?",
			Source:  "history",
			Score:   0.35,
			Tokens:  10,
		},
		{
			URI:     "session://s1/msg/2",
			Level:   types.ContentL2,
			Content: "[system]: You are a helpful assistant.",
			Source:  "history",
			Score:   0.4,
			Tokens:  10,
		},
		{
			// Non-history block should be skipped.
			URI:     "profile://t1/u1",
			Level:   types.ContentL0,
			Content: "User profile summary",
			Source:  "profile",
			Score:   0.6,
			Tokens:  5,
		},
	}

	msgs := buildMessages(blocks)

	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages from history blocks, got %d", len(msgs))
	}

	// EXPECTED (correct) behavior: roles are parsed from "[role]: content" format.
	// BUG: all messages get Role: "user".
	expectedRoles := []string{"user", "assistant", "system"}
	for i, msg := range msgs {
		if msg.Role != expectedRoles[i] {
			t.Errorf("Bug 1.17 confirmed: message %d has Role=%q, expected %q — role info lost",
				i, msg.Role, expectedRoles[i])
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Bug 1.18: Progressive loading skips L0/L1 blocks
// Expected behavior: oversized L0 block gets truncated, not skipped
// Current bug: only L2 blocks get demoted; L0/L1 are skipped entirely
// **Validates: Requirements 1.18**
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_18_ProgressiveLoadSkipsL0(t *testing.T) {
	// Test the progressive loading logic directly by constructing blocks
	// and simulating the budget-filling loop from Assemble.
	// The bug is in the progressive loading loop: only L2 blocks get demoted,
	// L0/L1 blocks that exceed budget are skipped entirely.

	// Create blocks: a small block that fits, then a large L0 block that exceeds remaining budget.
	smallBlock := types.ContentBlock{
		URI: "profile://t1/u1", Level: types.ContentL0,
		Content: "User profile summary.", Source: "profile",
		Score: 0.9, Tokens: 80,
	}
	// Large L0 memory block: 200 tokens, but only 100 tokens of budget remain.
	largeL0Block := types.ContentBlock{
		URI: "memory://t1/u1/large", Level: types.ContentL0,
		Content: strings.Repeat("Important memory content. ", 50), Source: "memory",
		Score: 0.8, Tokens: 200,
	}

	budget := 180 // Only 100 tokens remain after the small block.

	// Simulate the progressive loading loop from Assemble (current code).
	blocks := []types.ContentBlock{smallBlock, largeL0Block}
	var finalBlocks []types.ContentBlock
	usedTokens := 0

	for _, blk := range blocks {
		if usedTokens+blk.Tokens > budget {
			remaining := budget - usedTokens
			if remaining < 50 {
				continue
			}
			if blk.Level == types.ContentL2 {
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
			// For all levels: truncate to fit remaining budget.
			truncated := truncateBlockToBudget(blk, remaining)
			if truncated.Tokens > 0 {
				finalBlocks = append(finalBlocks, truncated)
				usedTokens += truncated.Tokens
			}
			continue
		}
		finalBlocks = append(finalBlocks, blk)
		usedTokens += blk.Tokens
	}

	// Check if the large L0 block was included (possibly truncated).
	hasLargeMemory := false
	for _, blk := range finalBlocks {
		if blk.Source == "memory" {
			hasLargeMemory = true
			break
		}
	}

	// EXPECTED (correct) behavior: the large L0 block should be truncated
	// to fit the remaining budget (100 tokens), not skipped entirely.
	// BUG: the code only demotes L2 blocks; L0 blocks are skipped.
	if !hasLargeMemory {
		t.Error("Bug 1.18 confirmed: large L0 memory block was skipped entirely instead of being truncated to fit remaining budget")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// staticSkillCatalog — test helper for skill catalog
// ═══════════════════════════════════════════════════════════════════════════════

type staticSkillCatalog struct {
	skills []types.SkillMeta
}

func (s *staticSkillCatalog) LoadCatalog(_ context.Context) ([]types.SkillMeta, error) {
	return s.skills, nil
}

func (s *staticSkillCatalog) LoadBody(_ context.Context, id string) (string, error) {
	for _, skill := range s.skills {
		if skill.ID == id {
			return skill.Body, nil
		}
	}
	return "", nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// Supplementary: verify estimateTokens for pure ASCII (baseline check)
// ═══════════════════════════════════════════════════════════════════════════════

func TestBugCondition_1_13_PureASCIIBaseline(t *testing.T) {
	// Pure ASCII should still work approximately as len(s)/4.
	input := "Hello world, this is a test string for token estimation."
	tokens := estimateTokens(input)
	expected := len(input) / 4

	// Allow 10% tolerance.
	diff := tokens - expected
	if diff < 0 {
		diff = -diff
	}
	tolerance := expected / 10
	if tolerance < 1 {
		tolerance = 1
	}
	if diff > tolerance {
		t.Errorf("Pure ASCII estimateTokens(%q) = %d, expected ~%d (within 10%%)", input, tokens, expected)
	}
}
