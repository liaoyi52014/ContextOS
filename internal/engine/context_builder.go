package engine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/sync/errgroup"

	"github.com/contextos/contextos/internal/types"
)

// ContextConfig holds tuning parameters for the ContextBuilder.
type ContextConfig struct {
	TokenBudget            int
	MaxMessages            int
	RecentRawTurnCount     int
	SkillBodyLoadThreshold float64
	MaxLoadedSkillBodies   int
}

// SkillCatalog is the minimal interface ContextBuilder needs from a SkillManager.
type SkillCatalog interface {
	LoadCatalog(ctx context.Context) ([]types.SkillMeta, error)
	LoadBody(ctx context.Context, id string) (string, error)
}

// ContextBuilder implements the two-phase context assembly engine.
type ContextBuilder struct {
	vectorStore types.VectorStore
	embedding   types.EmbeddingProvider
	sessions    *SessionManager
	profiles    types.ProfileStore
	cache       types.CacheStore
	skills      SkillCatalog
	retrieval   *RetrievalEngine
	config      *ContextConfig
}

// NewContextBuilder creates a new ContextBuilder with the given dependencies and config.
func NewContextBuilder(
	vs types.VectorStore, emb types.EmbeddingProvider,
	sessions *SessionManager, profiles types.ProfileStore,
	cache types.CacheStore, skills SkillCatalog,
	retrieval *RetrievalEngine, cfg ContextConfig,
) *ContextBuilder {
	if cfg.TokenBudget <= 0 {
		cfg.TokenBudget = 32000
	}
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 50
	}
	if cfg.RecentRawTurnCount <= 0 {
		cfg.RecentRawTurnCount = 8
	}
	if cfg.SkillBodyLoadThreshold <= 0 {
		cfg.SkillBodyLoadThreshold = 0.9
	}
	if cfg.MaxLoadedSkillBodies <= 0 {
		cfg.MaxLoadedSkillBodies = 2
	}
	return &ContextBuilder{
		vectorStore: vs,
		embedding:   emb,
		sessions:    sessions,
		profiles:    profiles,
		cache:       cache,
		skills:      skills,
		retrieval:   retrieval,
		config:      &cfg,
	}
}

// Assemble performs two-phase context assembly: parallel fetch then serial assembly.
func (b *ContextBuilder) Assemble(ctx context.Context, rc types.RequestContext, req types.AssembleRequest) (*types.AssembleResponse, error) {
	budget := b.config.TokenBudget
	if req.TokenBudget > 0 {
		budget = req.TokenBudget
	}

	// ── Phase 1: Parallel fetch ──
	var (
		profile  *types.UserProfile
		session  *types.Session
		catalog  []types.SkillMeta
	)

	g, gctx := errgroup.WithContext(ctx)

	// 1. Load user profile
	g.Go(func() error {
		if b.profiles == nil {
			return nil
		}
		p, err := b.profiles.Load(gctx, rc.TenantID, rc.UserID)
		if err != nil {
			return fmt.Errorf("context_builder: load profile: %w", err)
		}
		profile = p // nil means skip
		return nil
	})

	// 2. Load session history
	g.Go(func() error {
		sess, err := b.sessions.GetOrCreate(gctx, rc)
		if err != nil {
			return fmt.Errorf("context_builder: get session: %w", err)
		}
		session = sess
		return nil
	})

	// 3. Load Skill catalog
	g.Go(func() error {
		if b.skills == nil {
			return nil
		}
		cat, err := b.skills.LoadCatalog(gctx)
		if err != nil {
			return fmt.Errorf("context_builder: load catalog: %w", err)
		}
		catalog = cat
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Dynamic memory budget based on session history length.
	historyLen := 0
	if session != nil {
		historyLen = len(session.Messages)
	}
	memRatio := 0.25
	if historyLen <= 3 {
		memRatio = 0.4
	}
	if historyLen > 10 {
		memRatio = 0.15
	}
	memBudget := int(float64(budget) * memRatio)
	if memBudget < budget/10 {
		memBudget = budget / 10
	}
	if memBudget > budget/2 {
		memBudget = budget / 2
	}

	// Semantic search memories (after Phase 1, using dynamic budget).
	var memories []types.ContentBlock
	if b.retrieval != nil && b.embedding != nil && b.vectorStore != nil {
		blocks, err := b.retrieval.SemanticSearch(ctx, rc, req.Query, memBudget)
		if err != nil {
			return nil, fmt.Errorf("context_builder: semantic search: %w", err)
		}
		memories = blocks
	}

	// ── Phase 2: Serial assembly ──
	var blocks []types.ContentBlock

	// Profile block
	if profile != nil && profile.Summary != "" {
		content := profile.Summary
		blocks = append(blocks, types.ContentBlock{
			URI:     fmt.Sprintf("profile://%s/%s", rc.TenantID, rc.UserID),
			Level:   types.ContentL0,
			Content: content,
			Source:  "profile",
			Score:   0.6, // moderate baseline score for profile
			Tokens:  estimateTokens(content),
		})
	}

	// Memory blocks (already ContentBlocks from SemanticSearch)
	blocks = append(blocks, memories...)

	// Session history blocks
	if session != nil && len(session.Messages) > 0 {
		msgs := session.Messages
		// Take recent messages up to RecentRawTurnCount
		start := 0
		if len(msgs) > b.config.RecentRawTurnCount {
			start = len(msgs) - b.config.RecentRawTurnCount
		}
		recent := msgs[start:]
		for i, msg := range recent {
			content := fmt.Sprintf("[%s]: %s", msg.Role, msg.Content)
			// More recent messages get higher scores
			score := 0.3 + 0.1*float64(i)/float64(len(recent))
			blocks = append(blocks, types.ContentBlock{
				URI:     fmt.Sprintf("session://%s/msg/%d", session.ID, start+i),
				Level:   types.ContentL2,
				Content: content,
				Source:  "history",
				Score:   score,
				Tokens:  estimateTokens(content),
			})
		}
	}

	// Skill catalog blocks (name + description as L0)
	for _, skill := range catalog {
		content := fmt.Sprintf("Skill: %s — %s", skill.Name, skill.Description)
		blocks = append(blocks, types.ContentBlock{
			URI:     fmt.Sprintf("skill://%s", skill.ID),
			Level:   types.ContentL0,
			Content: content,
			Source:  "skill",
			Score:   0.2, // low baseline, will be upgraded if matched
			Tokens:  estimateTokens(content),
		})
	}

	// Skill matching (only if catalog is non-empty)
	if len(catalog) > 0 {
		blocks = b.matchAndLoadSkills(ctx, blocks, catalog, session, req.Query, budget)
	}

	// Sort all blocks by score descending
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Score > blocks[j].Score
	})

	// Progressive loading: fill budget with highest-score blocks first
	var finalBlocks []types.ContentBlock
	usedTokens := 0
	for _, blk := range blocks {
		if usedTokens+blk.Tokens > budget {
			remaining := budget - usedTokens
			if remaining < 50 {
				continue
			}
			if blk.Level == types.ContentL2 {
				// Demote to L1
				demoted := demoteBlock(blk, types.ContentL1)
				if usedTokens+demoted.Tokens <= budget {
					finalBlocks = append(finalBlocks, demoted)
					usedTokens += demoted.Tokens
					continue
				}
				// Demote to L0
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

	// Build final messages and system prompt
	systemPrompt := buildSystemPrompt(finalBlocks)
	messages := buildMessages(finalBlocks)

	return &types.AssembleResponse{
		SystemPrompt:    systemPrompt,
		Messages:        messages,
		EstimatedTokens: usedTokens,
		Sources:         finalBlocks,
	}, nil
}

// matchAndLoadSkills performs Skill matching and body loading.
// It modifies blocks in-place by upgrading matched Skill blocks.
func (b *ContextBuilder) matchAndLoadSkills(
	ctx context.Context,
	blocks []types.ContentBlock,
	catalog []types.SkillMeta,
	session *types.Session,
	query string,
	budget int,
) []types.ContentBlock {
	// Collect recent messages for matching context (last 4)
	var recentTexts []string
	if session != nil && len(session.Messages) > 0 {
		msgs := session.Messages
		start := 0
		if len(msgs) > 4 {
			start = len(msgs) - 4
		}
		for _, m := range msgs[start:] {
			recentTexts = append(recentTexts, m.Content)
		}
	}

	// Combined text for matching: query + recent messages
	matchInput := query
	for _, t := range recentTexts {
		matchInput += " " + t
	}
	matchInputLower := strings.ToLower(matchInput)
	matchInputTokens := tokenize(matchInput)

	// Compute query embedding for semantic matching
	var queryEmbedding []float32
	if b.embedding != nil {
		vecs, err := b.embedding.Embed(ctx, []string{query})
		if err == nil && len(vecs) > 0 {
			queryEmbedding = vecs[0]
		}
	}

	// Score each skill
	type skillHit struct {
		skill      types.SkillMeta
		matchScore float64
	}
	var hits []skillHit

	// Batch embed all skill texts in a single call.
	var allSkillTexts []string
	for _, skill := range catalog {
		allSkillTexts = append(allSkillTexts, skill.Name+" "+skill.Description)
	}
	var allSkillVecs [][]float32
	if len(queryEmbedding) > 0 && b.embedding != nil {
		vecs, err := b.embedding.Embed(ctx, allSkillTexts)
		if err == nil {
			allSkillVecs = vecs
		}
	}

	for i, skill := range catalog {
		// exact_name_hit: 1.0 if skill name appears in query or recent messages
		exactNameHit := 0.0
		if strings.Contains(matchInputLower, strings.ToLower(skill.Name)) {
			exactNameHit = 1.0
		}

		// keyword_hit: normalized 0-1 based on description keyword overlap
		descTokens := tokenize(skill.Description)
		keywordHit := keywordOverlap(descTokens, matchInputTokens)

		// semantic_hit: cosine similarity between query embedding and skill catalog embedding
		semanticHit := 0.0
		if len(queryEmbedding) > 0 && i < len(allSkillVecs) && len(allSkillVecs[i]) > 0 {
			semanticHit = cosineSim(queryEmbedding, allSkillVecs[i])
			if semanticHit < 0 {
				semanticHit = 0
			}
		}

		matchScore := 1.0*exactNameHit + 0.5*keywordHit + 0.7*semanticHit

		if matchScore >= b.config.SkillBodyLoadThreshold {
			hits = append(hits, skillHit{skill: skill, matchScore: matchScore})
		}
	}

	// Sort hits by matchScore descending
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].matchScore > hits[j].matchScore
	})

	// Load body for top MaxLoadedSkillBodies hits
	loaded := 0
	currentTokens := 0
	for _, blk := range blocks {
		currentTokens += blk.Tokens
	}

	loadedBodies := make(map[string]string) // skill ID -> body content

	for _, hit := range hits {
		if loaded >= b.config.MaxLoadedSkillBodies {
			break
		}

		body, err := b.skills.LoadBody(ctx, hit.skill.ID)
		if err != nil || body == "" {
			continue
		}

		bodyTokens := estimateTokens(body)
		if currentTokens+bodyTokens > budget {
			// Rollback: don't load body, keep name+description only
			continue
		}

		loadedBodies[hit.skill.ID] = body
		currentTokens += bodyTokens
		loaded++
	}

	// Update blocks: upgrade matched skills from L0 to L2
	for i, blk := range blocks {
		if blk.Source != "skill" {
			continue
		}
		// Extract skill ID from URI "skill://{id}"
		skillID := strings.TrimPrefix(blk.URI, "skill://")

		if body, ok := loadedBodies[skillID]; ok {
			// Upgrade to L2 with full body
			blocks[i].Level = types.ContentL2
			blocks[i].Content = body
			blocks[i].Tokens = estimateTokens(body)
			// Find the match score for this skill
			for _, hit := range hits {
				if hit.skill.ID == skillID {
					blocks[i].Score = hit.matchScore
					break
				}
			}
		} else {
			// Check if it was a hit but body wasn't loaded (budget constraint)
			for _, hit := range hits {
				if hit.skill.ID == skillID {
					blocks[i].Score = hit.matchScore
					break
				}
			}
		}
	}

	return blocks
}

// cosineSim computes cosine similarity between two float32 vectors.
func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// tokenize splits text into lowercase tokens, removing punctuation and stop words.
func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
	for _, w := range words {
		if !isStopWord(w) && len(w) > 0 {
			tokens[w] = true
		}
	}
	return tokens
}

// keywordOverlap computes normalized overlap between description tokens and input tokens.
func keywordOverlap(descTokens, inputTokens map[string]bool) float64 {
	if len(descTokens) == 0 {
		return 0
	}
	matches := 0
	for token := range descTokens {
		if inputTokens[token] {
			matches++
		}
	}
	return float64(matches) / float64(len(descTokens))
}

// isStopWord returns true for common English stop words.
func isStopWord(w string) bool {
	return stopWords[w]
}

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"but": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "of": true, "with": true, "by": true, "from": true,
	"is": true, "it": true, "as": true, "be": true, "was": true,
	"are": true, "were": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"this": true, "that": true, "these": true, "those": true,
	"i": true, "you": true, "he": true, "she": true, "we": true, "they": true,
	"me": true, "him": true, "her": true, "us": true, "them": true,
	"my": true, "your": true, "his": true, "its": true, "our": true, "their": true,
	"not": true, "no": true, "so": true, "if": true, "then": true,
	"than": true, "too": true, "very": true, "just": true,
}

// demoteBlock creates a copy of a block at a lower content level.
func demoteBlock(blk types.ContentBlock, level types.ContentLevel) types.ContentBlock {
	content := blk.Content
	switch level {
	case types.ContentL0:
		content = truncateRunes(content, 200)
	case types.ContentL1:
		content = truncateRunes(content, 1000)
	}
	return types.ContentBlock{
		URI:     blk.URI,
		Level:   level,
		Content: content,
		Source:  blk.Source,
		Score:   blk.Score,
		Tokens:  estimateTokens(content),
	}
}

// truncateBlockToBudget truncates a block's content to fit within the remaining token budget.
func truncateBlockToBudget(blk types.ContentBlock, remainingTokens int) types.ContentBlock {
	maxRunes := remainingTokens * 4
	content := truncateRunes(blk.Content, maxRunes)
	return types.ContentBlock{
		URI:     blk.URI,
		Level:   blk.Level,
		Content: content,
		Source:  blk.Source,
		Score:   blk.Score,
		Tokens:  estimateTokens(content),
	}
}

// buildSystemPrompt constructs a system prompt from non-history content blocks.
func buildSystemPrompt(blocks []types.ContentBlock) string {
	var parts []string
	for _, blk := range blocks {
		if blk.Source == "history" {
			continue
		}
		if blk.Content != "" {
			parts = append(parts, blk.Content)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// parseRoleContent extracts the role and content from a "[role]: content" formatted string.
func parseRoleContent(s string) (string, string) {
	if strings.HasPrefix(s, "[") {
		if idx := strings.Index(s, "]: "); idx > 0 {
			role := s[1:idx]
			if role == "user" || role == "assistant" || role == "system" {
				return role, s[idx+3:]
			}
		}
	}
	return "user", s
}

// buildMessages constructs the messages array from history content blocks.
func buildMessages(blocks []types.ContentBlock) []types.Message {
	var msgs []types.Message
	for _, blk := range blocks {
		if blk.Source != "history" {
			continue
		}
		role, content := parseRoleContent(blk.Content)
		msgs = append(msgs, types.Message{
			Role:    role,
			Content: content,
		})
	}
	return msgs
}
