package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	LLMTimeoutSec         int     // default 60
	EmbedTimeoutSec       int     // default 30
	VectorTimeoutSec      int     // default 30
	RecentRawTurnCount    int     // default 8 — number of recent messages to keep after compact
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
		LLMTimeoutSec:         60,
		EmbedTimeoutSec:       30,
		VectorTimeoutSec:      30,
		RecentRawTurnCount:    8,
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
	wg          sync.WaitGroup // tracks active compact goroutines
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
	p.wg.Add(1)
	go func() {
		defer func() {
			p.activeLocks.Delete(session.ID)
			<-p.semaphore
			p.wg.Done()
			if r := recover(); r != nil {
				p.logger.Error("compact goroutine panicked",
					zap.String("session_id", session.ID),
					zap.Any("panic", r),
				)
				if taskID != "" && p.tasks != nil {
					_ = p.tasks.Fail(context.Background(), taskID, fmt.Errorf("panic: %v", r))
				}
			}
		}()
		overallTimeout := time.Duration(p.config.CompactTimeoutSec) * time.Second
		if overallTimeout <= 0 {
			overallTimeout = 120 * time.Second
		}
		compactCtx, compactCancel := context.WithTimeout(context.Background(), overallTimeout)
		defer compactCancel()
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

// WaitForCompletion waits for all active compact goroutines to finish, with a timeout.
func (p *CompactProcessor) WaitForCompletion(timeout time.Duration) error {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("compact_processor: timed out waiting for %v", timeout)
	}
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

	// Determine where to start summarizing (incremental compaction).
	sourceTurnStart := 0
	if snapshot.Metadata != nil {
		if v, ok := snapshot.Metadata["last_compact_turn"]; ok {
			sourceTurnStart = toInt(v)
		}
	}
	if sourceTurnStart > len(snapshot.Messages) {
		sourceTurnStart = 0
	}

	// Build summary prompt from new messages only.
	summaryPrompt := buildSummaryPrompt(snapshot.Messages[sourceTurnStart:])

	// Call LLM to generate summary.
	llmTimeout := time.Duration(p.config.LLMTimeoutSec) * time.Second
	if llmTimeout <= 0 {
		llmTimeout = 60 * time.Second
	}
	llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
	defer llmCancel()
	llmResp, err := p.llm.Complete(llmCtx, types.LLMRequest{
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

	// Replace messages with summary + recent messages.
	// Save original snapshot message count before replacement for atomic update.
	originalSnapshotLen := len(snapshot.Messages)
	recentCount := p.config.RecentRawTurnCount
	if recentCount <= 0 {
		recentCount = 8
	}
	summaryMsg := types.Message{
		Role:    "system",
		Content: "[Compact Summary] " + summaryContent,
	}
	var recentMsgs []types.Message
	if len(snapshot.Messages) > recentCount {
		recentMsgs = snapshot.Messages[len(snapshot.Messages)-recentCount:]
	} else {
		recentMsgs = snapshot.Messages
	}
	compactedMessages := make([]types.Message, 0, 1+len(recentMsgs))
	compactedMessages = append(compactedMessages, summaryMsg)
	compactedMessages = append(compactedMessages, recentMsgs...)
	snapshot.Messages = compactedMessages

	// Persist CompactCheckpoint (log for now).
	checkpoint := types.CompactCheckpoint{
		ID:                   generateCompactID(),
		SessionID:            sessionID,
		TenantID:             rc.TenantID,
		UserID:               rc.UserID,
		CommittedAt:          time.Now(),
		SourceTurnStart:      sourceTurnStart,
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
	snapshot.Metadata["last_compact_turn"] = len(compactedMessages)
	snapshot.Metadata["last_compact_tokens"] = extractCompactTokenCount(compactedMessages)

	// Atomic update: reload the live session to preserve messages added during compact.
	liveSession, err := p.sessions.store.Load(ctx, rc.TenantID, rc.UserID, sessionID)
	if err != nil {
		return fmt.Errorf("compact_processor: reload live session: %w", err)
	}
	if liveSession == nil {
		// Session was deleted during compact; save snapshot as fallback.
		snapshot.Messages = compactedMessages
		snapshot.UpdatedAt = time.Now()
		if err := p.sessions.store.Save(ctx, snapshot); err != nil {
			return fmt.Errorf("compact_processor: save fallback session: %w", err)
		}
	} else {
		// Calculate messages added during compact.
		var newMsgsDuringCompact []types.Message
		if len(liveSession.Messages) > originalSnapshotLen {
			newMsgsDuringCompact = liveSession.Messages[originalSnapshotLen:]
		}
		// Build final messages: compacted + messages added during compact.
		finalMessages := make([]types.Message, 0, len(compactedMessages)+len(newMsgsDuringCompact))
		finalMessages = append(finalMessages, compactedMessages...)
		finalMessages = append(finalMessages, newMsgsDuringCompact...)

		liveSession.Messages = finalMessages
		if liveSession.Metadata == nil {
			liveSession.Metadata = map[string]interface{}{}
		}
		liveSession.Metadata["last_compact_at"] = snapshot.Metadata["last_compact_at"]
		liveSession.Metadata["last_compact_turn"] = len(finalMessages)
		liveSession.Metadata["last_compact_tokens"] = extractCompactTokenCount(finalMessages)
		liveSession.UpdatedAt = time.Now()
		if err := p.sessions.store.Save(ctx, liveSession); err != nil {
			return fmt.Errorf("compact_processor: update session metadata: %w", err)
		}
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
// It handles structured text (numbered lists, bullet lists, paragraphs)
// in addition to plain sentence splitting.
func extractFacts(summary string) []string {
	var facts []string

	// Split by paragraphs (double newline or more).
	paragraphs := splitParagraphs(summary)

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// Check if paragraph contains list items.
		lines := strings.Split(para, "\n")
		var listItems []string
		hasListItems := false

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if isListItem(trimmed) {
				hasListItems = true
				// Strip list prefix.
				cleaned := stripListPrefix(trimmed)
				if cleaned != "" {
					listItems = append(listItems, cleaned)
				}
			} else if hasListItems {
				// Non-list line after list items — could be a header or continuation.
				// If short, skip (likely a header like "Key findings:").
				if len(trimmed) > 20 {
					listItems = append(listItems, trimmed)
				}
			} else {
				// Plain text line — use sentence splitting.
				for _, sentence := range splitSentences(trimmed) {
					s := strings.TrimSpace(sentence)
					if s != "" {
						listItems = append(listItems, s)
					}
				}
			}
		}

		if hasListItems {
			facts = append(facts, listItems...)
		} else {
			// No list items — use sentence splitting on the whole paragraph.
			for _, sentence := range splitSentences(para) {
				s := strings.TrimSpace(sentence)
				if s != "" {
					facts = append(facts, s)
				}
			}
		}
	}

	return facts
}

// splitParagraphs splits text on double newlines.
func splitParagraphs(text string) []string {
	parts := strings.Split(text, "\n\n")
	var result []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 && strings.TrimSpace(text) != "" {
		result = []string{text}
	}
	return result
}

// isListItem returns true if the line starts with a numbered or bullet list prefix.
func isListItem(line string) bool {
	if len(line) < 2 {
		return false
	}
	// Check numbered list: "1. ", "1) ", "2. ", etc.
	for i, r := range line {
		if r >= '0' && r <= '9' {
			continue
		}
		if i > 0 && (r == '.' || r == ')') && i+1 < len(line) && line[i+1] == ' ' {
			return true
		}
		break
	}
	// Check bullet list: "- ", "* ", "• "
	if (line[0] == '-' || line[0] == '*') && len(line) > 1 && line[1] == ' ' {
		return true
	}
	if strings.HasPrefix(line, "• ") {
		return true
	}
	return false
}

// stripListPrefix removes the numbered or bullet prefix from a list item line.
func stripListPrefix(line string) string {
	// Strip "1. ", "1) ", etc.
	for i, r := range line {
		if r >= '0' && r <= '9' {
			continue
		}
		if i > 0 && (r == '.' || r == ')') {
			return strings.TrimSpace(line[i+1:])
		}
		break
	}
	// Strip "- ", "* "
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		return strings.TrimSpace(line[2:])
	}
	// Strip "• "
	if strings.HasPrefix(line, "• ") {
		return strings.TrimSpace(line[len("• "):])
	}
	return line
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
	embedTimeout := time.Duration(p.config.EmbedTimeoutSec) * time.Second
	if embedTimeout <= 0 {
		embedTimeout = 30 * time.Second
	}
	embedCtx, embedCancel := context.WithTimeout(ctx, embedTimeout)
	defer embedCancel()
	vectors, err := p.embedding.Embed(embedCtx, facts)
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

	vectorTimeout := time.Duration(p.config.VectorTimeoutSec) * time.Second
	if vectorTimeout <= 0 {
		vectorTimeout = 30 * time.Second
	}
	vectorCtx, vectorCancel := context.WithTimeout(ctx, vectorTimeout)
	defer vectorCancel()
	if err := p.vectorStore.Upsert(vectorCtx, items); err != nil {
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
	existing.Summary = mergeSummaries(existing.Summary, summary)
	existing.SourceSessionID = snapshot.ID
	existing.UpdatedAt = time.Now()

	// Extract simple preferences/interests from facts in the summary.
	facts := extractFacts(summary)
	for _, fact := range facts {
		if isPositivePreference(fact) {
			existing.Preferences = appendUnique(existing.Preferences, fact)
		} else {
			lower := strings.ToLower(fact)
			if strings.Contains(lower, "interest") || strings.Contains(lower, "curious") {
				existing.Interests = appendUnique(existing.Interests, fact)
			}
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

// mergeSummaries merges old and new summaries instead of overwriting.
// If old is non-empty, concatenates with a separator. If the merged result
// exceeds 4000 runes, the old part is truncated to keep the most recent content.
func mergeSummaries(old, new string) string {
	if old == "" {
		return new
	}
	merged := old + "\n\n---\n\n" + new
	if utf8.RuneCountInString(merged) <= 4000 {
		return merged
	}
	// Truncate old part to fit within 4000 runes total.
	separator := "\n\n---\n\n"
	separatorRunes := utf8.RuneCountInString(separator)
	newRunes := utf8.RuneCountInString(new)
	maxOldRunes := 4000 - separatorRunes - newRunes
	if maxOldRunes <= 0 {
		// New summary alone is too large or barely fits; just return new.
		return new
	}
	oldRunes := []rune(old)
	if len(oldRunes) > maxOldRunes {
		oldRunes = oldRunes[len(oldRunes)-maxOldRunes:]
	}
	return string(oldRunes) + separator + new
}

// isPositivePreference checks whether a fact expresses a positive preference.
// It returns false for negations ("don't like", "not prefer", etc.) and
// false matches ("likewise", "likely").
func isPositivePreference(fact string) bool {
	lower := strings.ToLower(fact)
	// Check for negation words — if found, this is not a positive preference.
	negations := []string{"don't", "doesn't", "not ", "never ", "no longer", "dislike"}
	for _, neg := range negations {
		if strings.Contains(lower, neg) {
			return false
		}
	}
	// Use word-boundary regex to avoid false matches like "likewise", "likely".
	preferRe := regexp.MustCompile(`\bprefer(s|red|ence)?\b`)
	likeRe := regexp.MustCompile(`\blike(s|d)?\b`)
	return preferRe.MatchString(lower) || likeRe.MatchString(lower)
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
