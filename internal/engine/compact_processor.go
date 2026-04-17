package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	ctxlog "github.com/contextos/contextos/internal/log"
	"github.com/contextos/contextos/internal/types"
	"go.uber.org/zap"
)

// HookNotifier is a forward reference interface for triggering lifecycle hooks.
type HookNotifier interface {
	Trigger(ctx context.Context, hookCtx types.HookContext) error
}

// CompactConfig holds tuning parameters for the CompactProcessor.
type CompactConfig struct {
	CompactBudgetRatio    float64 // default 0.5
	CompactTokenThreshold int     // default 16000
	CompactTurnThreshold  int     // default 10
	CompactIntervalMin    int     // default 15 (minutes)
	CompactTimeoutSec     int     // default 120
	MaxConcurrentCompacts int     // default 10
	TokenBudget           int     // total token budget for ratio calculation
}

// DefaultCompactConfig returns a CompactConfig with default values.
func DefaultCompactConfig() *CompactConfig {
	return &CompactConfig{
		CompactBudgetRatio:    0.5,
		CompactTokenThreshold: 16000,
		CompactTurnThreshold:  10,
		CompactIntervalMin:    15,
		CompactTimeoutSec:     120,
		MaxConcurrentCompacts: 10,
		TokenBudget:           32000,
	}
}

// CompactProcessor handles asynchronous context compression for sessions.
type CompactProcessor struct {
	llm         types.LLMClient
	sessions    *SessionManager
	profiles    types.ProfileStore
	cache       types.CacheStore
	vectorStore types.VectorStore
	embedding   types.EmbeddingProvider
	tasks       types.TaskTracker
	hooks       HookNotifier
	webhooks    types.WebhookManager
	tokenAudit  *ctxlog.TokenAuditor
	config      *CompactConfig
	semaphore   chan struct{} // global concurrency limiter
	activeLocks sync.Map      // session_id -> bool, local mutex
	logger      *zap.Logger
}

// NewCompactProcessor creates a CompactProcessor with the given dependencies.
func NewCompactProcessor(
	llm types.LLMClient,
	sessions *SessionManager,
	profiles types.ProfileStore,
	cache types.CacheStore,
	vectorStore types.VectorStore,
	embedding types.EmbeddingProvider,
	tasks types.TaskTracker,
	hooks HookNotifier,
	webhooks types.WebhookManager,
	tokenAudit *ctxlog.TokenAuditor,
	cfg *CompactConfig,
	logger *zap.Logger,
) *CompactProcessor {
	if cfg == nil {
		cfg = DefaultCompactConfig()
	}
	if cfg.MaxConcurrentCompacts <= 0 {
		cfg.MaxConcurrentCompacts = 10
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CompactProcessor{
		llm:         llm,
		sessions:    sessions,
		profiles:    profiles,
		cache:       cache,
		vectorStore: vectorStore,
		embedding:   embedding,
		tasks:       tasks,
		hooks:       hooks,
		webhooks:    webhooks,
		tokenAudit:  tokenAudit,
		config:      cfg,
		semaphore:   make(chan struct{}, cfg.MaxConcurrentCompacts),
		logger:      logger,
	}
}

// EvaluateAndTrigger evaluates compact trigger conditions and launches an
// asynchronous compact if any condition is met. Returns whether compact was
// triggered and the task ID when task tracking is enabled.
func (p *CompactProcessor) EvaluateAndTrigger(ctx context.Context, rc types.RequestContext, session *types.Session) (bool, string, error) {
	if !p.shouldTrigger(session) {
		return false, "", nil
	}

	// Check local mutex — if already running for this session, skip.
	if _, loaded := p.activeLocks.LoadOrStore(session.ID, true); loaded {
		p.logger.Debug("compact already active for session", zap.String("session_id", session.ID))
		return false, "", nil
	}

	// Acquire semaphore slot (non-blocking).
	select {
	case p.semaphore <- struct{}{}:
		// Slot acquired.
	default:
		// Semaphore full, release local lock and return.
		p.activeLocks.Delete(session.ID)
		p.logger.Warn("compact semaphore full, skipping", zap.String("session_id", session.ID))
		return false, "", nil
	}

	// Clone session for safe processing in the goroutine.
	snapshot := p.sessions.Clone(session)
	if snapshot == nil {
		p.activeLocks.Delete(session.ID)
		<-p.semaphore
		return false, "", fmt.Errorf("compact_processor: failed to clone session %s", session.ID)
	}
	// Preserve the original session ID for the snapshot.
	snapshot.ID = session.ID

	taskID := ""
	if p.tasks != nil {
		task, err := p.tasks.Create(ctx, "compact", map[string]interface{}{
			"tenant_id":  rc.TenantID,
			"user_id":    rc.UserID,
			"session_id": session.ID,
		})
		if err != nil {
			p.activeLocks.Delete(session.ID)
			<-p.semaphore
			return false, "", fmt.Errorf("compact_processor: create task: %w", err)
		}
		taskID = task.ID
	}

	// Launch goroutine (non-blocking).
	go func() {
		compactCtx := context.Background()
		if taskID != "" && p.tasks != nil {
			_ = p.tasks.Start(compactCtx, taskID)
		}
		if err := p.executeCompact(compactCtx, rc, snapshot); err != nil {
			if taskID != "" && p.tasks != nil {
				_ = p.tasks.Fail(compactCtx, taskID, err)
			}
			p.logger.Error("compact execution failed",
				zap.String("session_id", session.ID),
				zap.Error(err),
			)
			return
		}
		if taskID != "" && p.tasks != nil {
			_ = p.tasks.Complete(compactCtx, taskID, map[string]interface{}{
				"session_id": session.ID,
			})
		}
	}()

	return true, taskID, nil
}

// shouldTrigger evaluates the four trigger conditions. Any one triggers compact.
func (p *CompactProcessor) shouldTrigger(session *types.Session) bool {
	meta := p.extractCompactMeta(session)

	// Condition 1: Token budget usage ratio >= CompactBudgetRatio
	if p.config.TokenBudget > 0 {
		ratio := float64(meta.totalTokens) / float64(p.config.TokenBudget)
		if ratio >= p.config.CompactBudgetRatio {
			return true
		}
	}

	// Condition 2: New tokens since last compact >= CompactTokenThreshold
	if p.config.CompactTokenThreshold > 0 && meta.newTokens >= p.config.CompactTokenThreshold {
		return true
	}

	// Condition 3: Turn count since last compact >= CompactTurnThreshold
	if p.config.CompactTurnThreshold > 0 && meta.newTurns >= p.config.CompactTurnThreshold {
		return true
	}

	// Condition 4: Time since last compact >= CompactIntervalMin AND at least 1 new turn
	if p.config.CompactIntervalMin > 0 && meta.newTurns > 0 {
		interval := time.Duration(p.config.CompactIntervalMin) * time.Minute
		if time.Since(meta.lastCompactAt) >= interval {
			return true
		}
	}

	return false
}

// compactMeta holds extracted metadata for trigger evaluation.
type compactMeta struct {
	totalTokens   int
	newTokens     int
	newTurns      int
	lastCompactAt time.Time
}

// extractCompactMeta extracts compact-relevant metadata from a session.
func (p *CompactProcessor) extractCompactMeta(session *types.Session) compactMeta {
	meta := compactMeta{}

	// Estimate total tokens from all messages (rough: 4 chars ≈ 1 token).
	for _, msg := range session.Messages {
		meta.totalTokens += estimateTokens(msg.Content)
	}

	// Extract last compact info from session metadata.
	if session.Metadata != nil {
		if v, ok := session.Metadata["last_compact_at"]; ok {
			switch t := v.(type) {
			case string:
				if parsed, err := time.Parse(time.RFC3339, t); err == nil {
					meta.lastCompactAt = parsed
				}
			case time.Time:
				meta.lastCompactAt = t
			}
		}
		if v, ok := session.Metadata["last_compact_turn"]; ok {
			lastTurn := toInt(v)
			meta.newTurns = len(session.Messages) - lastTurn
		} else {
			meta.newTurns = len(session.Messages)
		}
		if v, ok := session.Metadata["last_compact_tokens"]; ok {
			lastTokens := toInt(v)
			meta.newTokens = meta.totalTokens - lastTokens
		} else {
			meta.newTokens = meta.totalTokens
		}
	} else {
		meta.newTurns = len(session.Messages)
		meta.newTokens = meta.totalTokens
	}

	if meta.newTurns < 0 {
		meta.newTurns = 0
	}
	if meta.newTokens < 0 {
		meta.newTokens = 0
	}

	return meta
}

// Note: estimateTokens is defined in retrieval.go and shared across the engine package.

// executeCompact performs the actual compact operation:
// distributed lock -> LLM summary -> extract memories -> embed & store ->
// merge profile -> log checkpoint -> release lock -> trigger hooks.
func (p *CompactProcessor) executeCompact(ctx context.Context, rc types.RequestContext, snapshot *types.Session) error {
	defer func() {
		p.activeLocks.Delete(snapshot.ID)
		<-p.semaphore
	}()

	sessionID := snapshot.ID
	lockKey := fmt.Sprintf("compact:%s:%s:%s", rc.TenantID, rc.UserID, sessionID)
	lockTTL := time.Duration(p.config.CompactTimeoutSec) * time.Second
	if lockTTL <= 0 {
		lockTTL = 120 * time.Second
	}

	// Acquire Redis distributed lock.
	acquired, err := p.cache.SetNX(ctx, lockKey, []byte("1"), lockTTL)
	if err != nil {
		return fmt.Errorf("compact_processor: acquire lock: %w", err)
	}
	if !acquired {
		p.logger.Debug("compact lock already held", zap.String("session_id", sessionID))
		return nil
	}
	defer func() {
		if delErr := p.cache.Delete(ctx, lockKey); delErr != nil {
			p.logger.Warn("compact_processor: failed to release lock",
				zap.String("key", lockKey),
				zap.Error(delErr),
			)
		}
	}()

	p.logger.Info("compact started",
		zap.String("session_id", sessionID),
		zap.String("tenant_id", rc.TenantID),
		zap.String("user_id", rc.UserID),
		zap.Int("message_count", len(snapshot.Messages)),
	)

	// Build summary prompt from messages.
	summaryPrompt := buildSummaryPrompt(snapshot.Messages)

	// Call LLM to generate summary.
	llmResp, err := p.llm.Complete(ctx, types.LLMRequest{
		Messages: []types.Message{
			{Role: "system", Content: "You are a helpful assistant that summarizes conversations."},
			{Role: "user", Content: summaryPrompt},
		},
		MaxTokens:   1024,
		Temperature: 0.3,
	})
	if err != nil {
		return fmt.Errorf("compact_processor: llm summary: %w", err)
	}

	summaryContent := llmResp.Content
	if p.tokenAudit != nil {
		_ = p.tokenAudit.Record(ctx, rc.TenantID, rc.UserID, "llm.compact", llmResp.Model, llmResp.PromptTokens, llmResp.CompletionTokens, llmResp.TotalTokens, sessionID, "")
	}

	// Extract memory facts from the summary (simple heuristic: split by sentences).
	facts := extractFacts(summaryContent)

	// Embed and store memory facts in vector store.
	var memoryIDs []string
	if len(facts) > 0 && p.embedding != nil && p.vectorStore != nil {
		memoryIDs, err = p.embedAndStoreFacts(ctx, rc, snapshot, facts)
		if err != nil {
			p.logger.Error("compact_processor: embed/store facts failed",
				zap.String("session_id", sessionID),
				zap.Error(err),
			)
			// Continue — don't fail the entire compact for memory storage issues.
		}
	}
	if p.tokenAudit != nil && len(facts) > 0 {
		total := 0
		for _, fact := range facts {
			total += estimateTokens(fact)
		}
		_ = p.tokenAudit.Record(ctx, rc.TenantID, rc.UserID, "embedding.compact", "", total, 0, total, sessionID, "")
	}

	// Merge user profile.
	if err := p.mergeProfile(ctx, rc, snapshot, summaryContent); err != nil {
		// Profile merge failure only logs, does not roll back.
		p.logger.Error("compact_processor: profile merge failed",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
	}

	// Persist CompactCheckpoint (log for now).
	checkpoint := types.CompactCheckpoint{
		ID:                   generateCompactID(),
		SessionID:            sessionID,
		TenantID:             rc.TenantID,
		UserID:               rc.UserID,
		CommittedAt:          time.Now(),
		SourceTurnStart:      0,
		SourceTurnEnd:        len(snapshot.Messages),
		SummaryContent:       summaryContent,
		ExtractedMemoryIDs:   memoryIDs,
		PromptTokensUsed:     llmResp.PromptTokens,
		CompletionTokensUsed: llmResp.CompletionTokens,
	}

	checkpointJSON, _ := json.Marshal(checkpoint)
	if checkpointStore, ok := p.sessions.store.(types.CompactCheckpointStore); ok {
		if err := checkpointStore.SaveCompactCheckpoint(ctx, checkpoint); err != nil {
			return fmt.Errorf("compact_processor: save checkpoint: %w", err)
		}
	}

	if snapshot.Metadata == nil {
		snapshot.Metadata = map[string]interface{}{}
	}
	snapshot.Metadata["last_compact_at"] = checkpoint.CommittedAt.Format(time.RFC3339)
	snapshot.Metadata["last_compact_turn"] = len(snapshot.Messages)
	snapshot.Metadata["last_compact_tokens"] = extractCompactTokenCount(snapshot.Messages)
	snapshot.UpdatedAt = time.Now()
	if err := p.sessions.store.Save(ctx, snapshot); err != nil {
		return fmt.Errorf("compact_processor: update session metadata: %w", err)
	}

	p.logger.Info("compact checkpoint persisted",
		zap.String("session_id", sessionID),
		zap.String("checkpoint_id", checkpoint.ID),
		zap.Int("facts_extracted", len(facts)),
		zap.Int("memory_ids", len(memoryIDs)),
		zap.String("checkpoint", string(checkpointJSON)),
	)

	// Trigger hooks (compact event).
	if p.hooks != nil {
		hookCtx := types.HookContext{
			Event:     types.HookCompact,
			TenantID:  rc.TenantID,
			UserID:    rc.UserID,
			SessionID: sessionID,
			Data: map[string]interface{}{
				"checkpoint_id":     checkpoint.ID,
				"summary":           summaryContent,
				"facts_count":       len(facts),
				"memory_ids":        memoryIDs,
				"prompt_tokens":     llmResp.PromptTokens,
				"completion_tokens": llmResp.CompletionTokens,
			},
		}
		if hookErr := p.hooks.Trigger(ctx, hookCtx); hookErr != nil {
			p.logger.Warn("compact_processor: hook trigger failed",
				zap.String("session_id", sessionID),
				zap.Error(hookErr),
			)
		}
	}

	if p.webhooks != nil && len(memoryIDs) > 0 {
		_ = p.webhooks.Notify(ctx, types.WebhookEvent{
			ID:         generateCompactID(),
			Type:       "memory.extracted",
			TenantID:   rc.TenantID,
			UserID:     rc.UserID,
			SessionID:  sessionID,
			OccurredAt: time.Now().UTC(),
			Payload: map[string]interface{}{
				"checkpoint_id": checkpoint.ID,
				"memory_ids":    memoryIDs,
				"count":         len(memoryIDs),
			},
		})
	}
	if p.webhooks != nil {
		_ = p.webhooks.Notify(ctx, types.WebhookEvent{
			ID:         generateCompactID(),
			Type:       "compact.completed",
			TenantID:   rc.TenantID,
			UserID:     rc.UserID,
			SessionID:  sessionID,
			OccurredAt: time.Now().UTC(),
			Payload: map[string]interface{}{
				"checkpoint_id": checkpoint.ID,
				"memory_ids":    memoryIDs,
				"summary":       summaryContent,
			},
		})
	}

	p.logger.Info("compact completed",
		zap.String("session_id", sessionID),
		zap.String("checkpoint_id", checkpoint.ID),
	)

	return nil
}

// buildSummaryPrompt constructs the LLM prompt for conversation summarization.
func buildSummaryPrompt(messages []types.Message) string {
	var sb strings.Builder
	sb.WriteString("Summarize the following conversation. Extract key facts, preferences, and decisions.\n\n")
	for _, msg := range messages {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}
	return sb.String()
}

// extractFacts splits summary text into individual fact sentences.
func extractFacts(summary string) []string {
	// Simple heuristic: split by sentence-ending punctuation.
	var facts []string
	for _, sentence := range splitSentences(summary) {
		trimmed := strings.TrimSpace(sentence)
		if len(trimmed) > 0 {
			facts = append(facts, trimmed)
		}
	}
	return facts
}

// splitSentences splits text into sentences by common delimiters.
func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' {
			s := strings.TrimSpace(current.String())
			if len(s) > 0 {
				sentences = append(sentences, s)
			}
			current.Reset()
		}
	}
	// Remaining text without sentence-ending punctuation.
	if s := strings.TrimSpace(current.String()); len(s) > 0 {
		sentences = append(sentences, s)
	}
	return sentences
}

// embedAndStoreFacts embeds fact texts and stores them in the vector store.
func (p *CompactProcessor) embedAndStoreFacts(
	ctx context.Context,
	rc types.RequestContext,
	snapshot *types.Session,
	facts []string,
) ([]string, error) {
	vectors, err := p.embedding.Embed(ctx, facts)
	if err != nil {
		return nil, fmt.Errorf("embed facts: %w", err)
	}

	items := make([]types.VectorItem, len(facts))
	ids := make([]string, len(facts))
	for i, fact := range facts {
		id := generateCompactID()
		ids[i] = id
		items[i] = types.VectorItem{
			ID:       id,
			Vector:   vectors[i],
			Content:  fact,
			URI:      fmt.Sprintf("memory://%s/%s/%s", rc.TenantID, rc.UserID, id),
			TenantID: rc.TenantID,
			UserID:   rc.UserID,
			Metadata: map[string]string{
				"source":     "compact",
				"session_id": snapshot.ID,
				"category":   "fact",
			},
		}
	}

	if err := p.vectorStore.Upsert(ctx, items); err != nil {
		return nil, fmt.Errorf("upsert facts: %w", err)
	}

	return ids, nil
}

// mergeProfile loads the existing user profile, merges new insights from the
// compact summary, and upserts the result.
func (p *CompactProcessor) mergeProfile(
	ctx context.Context,
	rc types.RequestContext,
	snapshot *types.Session,
	summary string,
) error {
	if p.profiles == nil {
		return nil
	}

	// Load existing profile.
	existing, err := p.profiles.Load(ctx, rc.TenantID, rc.UserID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	// If nil, create new one.
	if existing == nil {
		existing = &types.UserProfile{
			TenantID:    rc.TenantID,
			UserID:      rc.UserID,
			Interests:   []string{},
			Preferences: []string{},
			Goals:       []string{},
			Constraints: []string{},
		}
	}

	// Append new insights from conversation summary.
	existing.Summary = summary
	existing.SourceSessionID = snapshot.ID
	existing.UpdatedAt = time.Now()

	// Extract simple preferences/interests from facts in the summary.
	facts := extractFacts(summary)
	for _, fact := range facts {
		lower := strings.ToLower(fact)
		if strings.Contains(lower, "prefer") || strings.Contains(lower, "like") {
			existing.Preferences = appendUnique(existing.Preferences, fact)
		} else if strings.Contains(lower, "interest") || strings.Contains(lower, "curious") {
			existing.Interests = appendUnique(existing.Interests, fact)
		}
	}

	// Upsert the merged profile.
	if err := p.profiles.Upsert(ctx, existing); err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}

	return nil
}

// appendUnique appends a value to a slice only if it's not already present.
func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

func extractCompactTokenCount(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateTokens(msg.Content)
	}
	return total
}

// generateCompactID produces a UUID v4 string for compact-related IDs.
func generateCompactID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
