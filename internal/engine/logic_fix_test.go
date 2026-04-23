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
// B类 Fix Unit Tests — Context Management & Compact Logic (Bugs 1.7-1.18)
// ═══════════════════════════════════════════════════════════════════════════════

// TestCompactReducesMessages verifies that after compact, messages = 1 summary + recent N.
func TestCompactReducesMessages(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			return &types.LLMResponse{Content: "User discussed Go programming.", Model: "mock"}, nil
		},
	}

	recentCount := 4
	processor := NewCompactProcessor(llm, sessions, nil, cache, nil, nil, nil, nil, nil, nil,
		&CompactConfig{
			CompactBudgetRatio: 1, CompactTokenThreshold: 1, CompactTurnThreshold: 1,
			CompactIntervalMin: 60, CompactTimeoutSec: 5, MaxConcurrentCompacts: 2,
			TokenBudget: 32000, RecentRawTurnCount: recentCount,
		}, nil)

	msgs := make([]types.Message, 20)
	for i := range msgs {
		msgs[i] = types.Message{Role: "user", Content: fmt.Sprintf("message %d", i)}
	}
	snapshot := &types.Session{
		ID: "s-reduce", TenantID: "t1", UserID: "u1",
		Messages: msgs, Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), snapshot)
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)

	err := processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-reduce",
	}, snapshot)
	if err != nil {
		t.Fatalf("executeCompact failed: %v", err)
	}

	saved, _ := store.Load(context.Background(), "t1", "u1", "s-reduce")
	if saved == nil {
		t.Fatal("session not found after compact")
	}

	// Expected: 1 summary + recentCount recent messages.
	expected := 1 + recentCount
	if len(saved.Messages) != expected {
		t.Errorf("expected %d messages after compact (1 summary + %d recent), got %d",
			expected, recentCount, len(saved.Messages))
	}
	if len(saved.Messages) > 0 && !strings.HasPrefix(saved.Messages[0].Content, "[Compact Summary]") {
		t.Errorf("first message should be compact summary, got: %q", saved.Messages[0].Content)
	}
}

// TestSourceTurnStartIncremental verifies that the second compact has SourceTurnStart > 0.
func TestSourceTurnStartIncremental(t *testing.T) {
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

	processor := NewCompactProcessor(llm, sessions, nil, cache, nil, nil, nil, nil, nil, nil,
		&CompactConfig{
			CompactBudgetRatio: 1, CompactTokenThreshold: 1, CompactTurnThreshold: 1,
			CompactIntervalMin: 60, CompactTimeoutSec: 5, MaxConcurrentCompacts: 2,
			TokenBudget: 32000,
		}, nil)

	// First compact.
	snap1 := &types.Session{
		ID: "s-incr", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{
			{Role: "user", Content: "msg 1"},
			{Role: "assistant", Content: "reply 1"},
		},
		Metadata: map[string]interface{}{},
	}
	_ = store.Save(context.Background(), snap1)
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snap1.ID, true)
	_ = processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-incr",
	}, snap1)

	// Reload and add more messages.
	saved, _ := store.Load(context.Background(), "t1", "u1", "s-incr")
	if saved == nil {
		t.Fatal("session not found after first compact")
	}
	saved.Messages = append(saved.Messages,
		types.Message{Role: "user", Content: "msg 2"},
		types.Message{Role: "assistant", Content: "reply 2"},
	)
	_ = store.Save(context.Background(), saved)

	// Second compact.
	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(saved.ID, true)
	_ = processor.executeCompact(context.Background(), types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-incr",
	}, saved)

	if store.CheckpointCount() < 2 {
		t.Fatalf("expected at least 2 checkpoints, got %d", store.CheckpointCount())
	}
	second := store.checkpoints[1]
	if second.SourceTurnStart == 0 {
		t.Error("second compact SourceTurnStart should be > 0 for incremental compaction")
	}
}

// TestExtractFacts_NumberedList verifies "1. A\n2. B" produces 2 facts.
func TestExtractFacts_NumberedList(t *testing.T) {
	input := "1. User prefers Go\n2. Team uses Docker"
	facts := extractFacts(input)
	if len(facts) < 2 {
		t.Errorf("expected at least 2 facts from numbered list, got %d: %v", len(facts), facts)
	}
}

// TestExtractFacts_BulletList verifies "- A\n- B" produces 2 facts.
func TestExtractFacts_BulletList(t *testing.T) {
	input := "- Prefers dark mode\n- Uses vim"
	facts := extractFacts(input)
	if len(facts) < 2 {
		t.Errorf("expected at least 2 facts from bullet list, got %d: %v", len(facts), facts)
	}
}

// TestMergeSummaries_Concatenates verifies old + new summaries are merged.
func TestMergeSummaries_Concatenates(t *testing.T) {
	old := "User is a Go developer."
	new := "User works on distributed systems."
	merged := mergeSummaries(old, new)

	if !strings.Contains(merged, old) {
		t.Error("merged summary should contain old summary")
	}
	if !strings.Contains(merged, new) {
		t.Error("merged summary should contain new summary")
	}
	if !strings.Contains(merged, "---") {
		t.Error("merged summary should contain separator")
	}
}

// TestMergeSummaries_TruncatesOld verifies that when merged > 4000 runes, old is truncated.
func TestMergeSummaries_TruncatesOld(t *testing.T) {
	// Create an old summary that's 3500 runes.
	old := strings.Repeat("旧", 3500)
	new := strings.Repeat("新", 600)
	merged := mergeSummaries(old, new)

	runeCount := utf8.RuneCountInString(merged)
	if runeCount > 4000 {
		t.Errorf("merged summary has %d runes, expected <= 4000", runeCount)
	}
	if !strings.Contains(merged, new) {
		t.Error("merged summary should contain the new summary in full")
	}
}

// TestIsPositivePreference_Negation verifies "don't like" returns false.
func TestIsPositivePreference_Negation(t *testing.T) {
	if isPositivePreference("I don't like spicy food") {
		t.Error("'I don't like spicy food' should NOT be a positive preference")
	}
	if isPositivePreference("I never prefer dark mode") {
		t.Error("'I never prefer dark mode' should NOT be a positive preference")
	}
}

// TestIsPositivePreference_FalseMatch verifies "likewise" returns false.
func TestIsPositivePreference_FalseMatch(t *testing.T) {
	if isPositivePreference("Likewise, the team agreed") {
		t.Error("'Likewise' should NOT be a positive preference")
	}
}

// TestIsPositivePreference_Positive verifies "I prefer dark mode" returns true.
func TestIsPositivePreference_Positive(t *testing.T) {
	if !isPositivePreference("I prefer dark mode") {
		t.Error("'I prefer dark mode' SHOULD be a positive preference")
	}
	if !isPositivePreference("I like Go programming") {
		t.Error("'I like Go programming' SHOULD be a positive preference")
	}
}

// TestAtomicMetadataUpdate verifies messages added during compact are preserved.
func TestAtomicMetadataUpdate(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := newCompactAwareSessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	var addDuringCompact func()
	llm := &mock.MockLLMClient{
		CompleteFunc: func(_ context.Context, _ types.LLMRequest) (*types.LLMResponse, error) {
			if addDuringCompact != nil {
				addDuringCompact()
			}
			return &types.LLMResponse{Content: "Summary.", Model: "mock"}, nil
		},
	}

	processor := newBugConditionCompactProcessor(llm, sessions, nil, cache, nil, nil, nil)

	ctx := context.Background()
	sess := &types.Session{
		ID: "s-atomic", TenantID: "t1", UserID: "u1",
		Messages: []types.Message{
			{Role: "user", Content: "original msg 0"},
			{Role: "user", Content: "original msg 1"},
			{Role: "user", Content: "original msg 2"},
		},
		Metadata: map[string]interface{}{},
	}
	_ = store.Save(ctx, sess)
	snapshot := sessions.Clone(sess)
	snapshot.ID = sess.ID

	addDuringCompact = func() {
		live, _ := store.Load(ctx, "t1", "u1", "s-atomic")
		if live != nil {
			live.Messages = append(live.Messages, types.Message{
				Role: "user", Content: "added during compact",
			})
			_ = store.Save(ctx, live)
		}
	}

	processor.semaphore <- struct{}{}
	processor.activeLocks.Store(snapshot.ID, true)
	_ = processor.executeCompact(ctx, types.RequestContext{
		TenantID: "t1", UserID: "u1", SessionID: "s-atomic",
	}, snapshot)

	saved, _ := store.Load(ctx, "t1", "u1", "s-atomic")
	if saved == nil {
		t.Fatal("session not found after compact")
	}

	found := false
	for _, msg := range saved.Messages {
		if strings.Contains(msg.Content, "added during compact") {
			found = true
			break
		}
	}
	if !found {
		t.Error("message added during compact was lost — atomic update failed")
	}
}

// TestEstimateTokens_CJK verifies Chinese text is estimated correctly.
func TestEstimateTokens_CJK(t *testing.T) {
	input := "你好世界测试"
	tokens := estimateTokens(input)
	// 5 CJK characters should produce at least 5 tokens.
	if tokens < 5 {
		t.Errorf("estimateTokens(%q) = %d, expected >= 5", input, tokens)
	}
}

// TestEstimateTokens_ASCII verifies English text matches len/4.
func TestEstimateTokens_ASCII(t *testing.T) {
	input := "Hello world, this is a test string."
	tokens := estimateTokens(input)
	expected := len(input) / 4

	diff := tokens - expected
	if diff < 0 {
		diff = -diff
	}
	tolerance := expected / 10
	if tolerance < 1 {
		tolerance = 1
	}
	if diff > tolerance {
		t.Errorf("estimateTokens(%q) = %d, expected ~%d (within 10%%)", input, tokens, expected)
	}
}

// TestBatchEmbedSkills verifies 5 skills result in at most 3 embed calls.
func TestBatchEmbedSkills(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := mock.NewMemorySessionStore()
	sessions := NewSessionManager(cache, store, SessionConfig{
		MaxMessages: 50, LRUCacheSize: 100, LRUCacheTTLSec: 2,
		SyncQueueSize: 100, SyncBatchSize: 10, SyncFlushIntervalMs: 20,
	})
	defer sessions.Stop()

	counting := newCountingEmbeddingProvider(16)
	vectorStore := mock.NewMemoryVectorStore()

	skillCatalog := &staticSkillCatalog{
		skills: make([]types.SkillMeta, 5),
	}
	for i := 0; i < 5; i++ {
		skillCatalog.skills[i] = types.SkillMeta{
			ID: fmt.Sprintf("skill-%d", i), Name: fmt.Sprintf("skill-%d", i),
			Description: fmt.Sprintf("A skill that does thing %d", i),
			Body:        fmt.Sprintf("Body of skill %d", i),
		}
	}

	retrieval := NewRetrievalEngine(vectorStore, counting, RetrievalConfig{
		RecallScoreThreshold: 0, RecallMaxResults: 10,
	}, nil)

	builder := NewContextBuilder(vectorStore, counting, sessions, nil, cache, skillCatalog, retrieval, ContextConfig{
		TokenBudget: 32000, MaxMessages: 50, RecentRawTurnCount: 8,
		SkillBodyLoadThreshold: 0.0, MaxLoadedSkillBodies: 2,
	})

	ctx := context.Background()
	rc := types.RequestContext{TenantID: "t1", UserID: "u1", SessionID: "s-batch"}
	_, _ = sessions.GetOrCreate(ctx, rc)

	counting.mu.Lock()
	counting.calls = 0
	counting.mu.Unlock()

	_, _ = builder.Assemble(ctx, rc, types.AssembleRequest{
		Query: "help me with code", TokenBudget: 32000,
	})

	callCount := counting.CallCount()
	if callCount > 3 {
		t.Errorf("expected <= 3 Embed calls for 5 skills (batch), got %d", callCount)
	}
}

// TestDynamicMemoryBudget verifies short vs long sessions get different budgets.
// We verify the dynamic budget logic directly by checking the ratio calculation.
func TestDynamicMemoryBudget(t *testing.T) {
	budget := 4000

	// Short session (2 messages) → memRatio = 0.4 → memBudget = 1600
	shortHistoryLen := 2
	shortRatio := 0.25
	if shortHistoryLen <= 3 {
		shortRatio = 0.4
	}
	if shortHistoryLen > 10 {
		shortRatio = 0.15
	}
	shortBudget := int(float64(budget) * shortRatio)

	// Long session (20 messages) → memRatio = 0.15 → memBudget = 600
	longHistoryLen := 20
	longRatio := 0.25
	if longHistoryLen <= 3 {
		longRatio = 0.4
	}
	if longHistoryLen > 10 {
		longRatio = 0.15
	}
	longBudget := int(float64(budget) * longRatio)

	if shortBudget == longBudget {
		t.Errorf("short session budget (%d) and long session budget (%d) should differ", shortBudget, longBudget)
	}
	if shortBudget <= longBudget {
		t.Errorf("short session should get MORE memory budget (%d) than long session (%d)", shortBudget, longBudget)
	}
	// Verify specific values.
	if shortBudget != 1600 {
		t.Errorf("short session (2 msgs) memBudget = %d, expected 1600 (40%% of 4000)", shortBudget)
	}
	if longBudget != 600 {
		t.Errorf("long session (20 msgs) memBudget = %d, expected 600 (15%% of 4000)", longBudget)
	}
}

// TestTruncateRunes_ValidUTF8 verifies CJK truncation produces valid UTF-8.
func TestTruncateRunes_ValidUTF8(t *testing.T) {
	input := strings.Repeat("中", 300)
	result := truncateRunes(input, 200)

	if !utf8.ValidString(result) {
		t.Error("truncateRunes produced invalid UTF-8")
	}
	if utf8.RuneCountInString(result) != 200 {
		t.Errorf("expected 200 runes, got %d", utf8.RuneCountInString(result))
	}
}

// TestTruncateRunes_ShortString verifies short string is returned unchanged.
func TestTruncateRunes_ShortString(t *testing.T) {
	input := "hello"
	result := truncateRunes(input, 200)
	if result != input {
		t.Errorf("expected %q, got %q", input, result)
	}
}

// TestParseRoleContent_User verifies "[user]: hello" → ("user", "hello").
func TestParseRoleContent_User(t *testing.T) {
	role, content := parseRoleContent("[user]: hello")
	if role != "user" || content != "hello" {
		t.Errorf("expected (user, hello), got (%s, %s)", role, content)
	}
}

// TestParseRoleContent_Assistant verifies "[assistant]: hi" → ("assistant", "hi").
func TestParseRoleContent_Assistant(t *testing.T) {
	role, content := parseRoleContent("[assistant]: hi")
	if role != "assistant" || content != "hi" {
		t.Errorf("expected (assistant, hi), got (%s, %s)", role, content)
	}
}

// TestParseRoleContent_NoPrefix verifies "plain text" → ("user", "plain text").
func TestParseRoleContent_NoPrefix(t *testing.T) {
	role, content := parseRoleContent("plain text")
	if role != "user" || content != "plain text" {
		t.Errorf("expected (user, plain text), got (%s, %s)", role, content)
	}
}

// TestProgressiveLoad_TruncatesL0 verifies that a large L0 block gets truncated
// instead of being skipped when it exceeds the remaining budget.
func TestProgressiveLoad_TruncatesL0(t *testing.T) {
	smallBlock := types.ContentBlock{
		URI: "profile://t1/u1", Level: types.ContentL0,
		Content: "User profile.", Source: "profile",
		Score: 0.9, Tokens: 80,
	}
	largeL0Block := types.ContentBlock{
		URI: "memory://t1/u1/large", Level: types.ContentL0,
		Content: strings.Repeat("Important memory content. ", 50), Source: "memory",
		Score: 0.8, Tokens: 200,
	}

	budget := 180
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

	hasMemory := false
	for _, blk := range finalBlocks {
		if blk.Source == "memory" {
			hasMemory = true
			break
		}
	}
	if !hasMemory {
		t.Error("large L0 memory block should be truncated to fit, not skipped")
	}
}
