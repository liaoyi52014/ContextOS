package engine

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RetrievalConfig holds tuning parameters for the RetrievalEngine.
type RetrievalConfig struct {
	RecallScoreThreshold float64
	RecallMaxResults     int
	PatternMaxResults    int
}

// RetrievalEngine provides semantic and pattern-based context retrieval.
type RetrievalEngine struct {
	vectorStore types.VectorStore
	embedding   types.EmbeddingProvider
	config      RetrievalConfig
	db          *pgxpool.Pool
}

// NewRetrievalEngine creates a new RetrievalEngine.
func NewRetrievalEngine(vs types.VectorStore, emb types.EmbeddingProvider, cfg RetrievalConfig, db *pgxpool.Pool) *RetrievalEngine {
	if cfg.RecallMaxResults <= 0 {
		cfg.RecallMaxResults = 10
	}
	if cfg.PatternMaxResults <= 0 {
		cfg.PatternMaxResults = 20
	}
	return &RetrievalEngine{
		vectorStore: vs,
		embedding:   emb,
		config:      cfg,
		db:          db,
	}
}

// estimateTokens provides a rough token estimate for a string (~4 chars per token).
func estimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 && len(s) > 0 {
		n = 1
	}
	return n
}

// SemanticSearch embeds the query, searches the vector store with tenant+user filtering,
// and returns results as ContentBlocks.
func (r *RetrievalEngine) SemanticSearch(ctx context.Context, rc types.RequestContext, query string, budget int) ([]types.ContentBlock, error) {
	// Embed the query text.
	vectors, err := r.embedding.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("retrieval: embed query: %w", err)
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, nil
	}

	topK := r.config.RecallMaxResults
	sq := types.SearchQuery{
		Vector: vectors[0],
		TopK:   topK,
		Filter: &types.Filter{
			TenantID: rc.TenantID,
			UserID:   rc.UserID,
		},
		Threshold: r.config.RecallScoreThreshold,
	}

	results, err := r.vectorStore.Search(ctx, sq)
	if err != nil {
		return nil, fmt.Errorf("retrieval: vector search: %w", err)
	}

	// Deduplicate by item ID.
	seen := make(map[string]bool)
	var blocks []types.ContentBlock
	usedTokens := 0

	for _, res := range results {
		if seen[res.Item.ID] {
			continue
		}
		seen[res.Item.ID] = true

		content := res.Item.Content
		tokens := estimateTokens(content)

		// Single-item truncation: if this block exceeds remaining budget, truncate it.
		remaining := budget - usedTokens
		if remaining <= 0 {
			break
		}
		if tokens > remaining {
			// Truncate content to fit budget.
			maxChars := remaining * 4
			if maxChars < len(content) {
				content = content[:maxChars]
			}
			tokens = remaining
		}

		blocks = append(blocks, types.ContentBlock{
			URI:     res.Item.URI,
			Level:   types.ContentL0,
			Content: content,
			Source:  "memory",
			Score:   res.Score,
			Tokens:  tokens,
		})
		usedTokens += tokens
	}

	// Sort by score descending (already sorted from vector store, but ensure).
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Score > blocks[j].Score
	})

	return blocks, nil
}

// PatternSearch performs keyword/grep/glob search over memory_facts,
// compact_checkpoints.summary, user_profiles.summary, and current session messages.
// v1 implements keyword search using SQL ILIKE.
func (r *RetrievalEngine) PatternSearch(ctx context.Context, rc types.RequestContext, pattern, mode string, budget int) ([]types.ContentBlock, error) {
	if mode == "" {
		mode = PatternModeKeyword
	}
	if !isValidPatternMode(mode) {
		return nil, &types.AppError{Code: types.ErrBadRequest, Message: fmt.Sprintf("invalid pattern search mode %q", mode)}
	}
	if r.db == nil {
		return nil, nil
	}

	maxResults := r.config.PatternMaxResults
	if maxResults <= 0 {
		maxResults = 20
	}

	var blocks []types.ContentBlock
	seen := make(map[string]bool)
	usedTokens := 0

	addBlock := func(uri, content, source string) {
		if seen[uri] {
			return
		}
		seen[uri] = true

		tokens := estimateTokens(content)
		remaining := budget - usedTokens
		if remaining <= 0 {
			return
		}
		if tokens > remaining {
			maxChars := remaining * 4
			if maxChars < len(content) {
				content = content[:maxChars]
			}
			tokens = remaining
		}

		blocks = append(blocks, types.ContentBlock{
			URI:     uri,
			Level:   types.ContentL0,
			Content: content,
			Source:  source,
		})
		usedTokens += tokens
	}

	queryPattern := "%" + pattern + "%"
	rows, err := r.queryPatternRows(ctx, mode,
		`SELECT id, content FROM memory_facts
		 WHERE tenant_id = $1 AND user_id = $2`,
		`SELECT id, content FROM memory_facts
		 WHERE tenant_id = $1 AND user_id = $2 AND content ILIKE $3
		 LIMIT $4`,
		rc.TenantID, rc.UserID, queryPattern, maxResults,
	)
	if err == nil {
		for rows.Next() {
			var id, content string
			if err := rows.Scan(&id, &content); err == nil && r.keepPatternResult(content, pattern, mode) {
				addBlock(fmt.Sprintf("memory://%s", id), content, "memory")
			}
		}
		rows.Close()
	}

	rows, err = r.queryPatternRows(ctx, mode,
		`SELECT id, summary_content FROM compact_checkpoints
		 WHERE tenant_id = $1 AND user_id = $2`,
		`SELECT id, summary_content FROM compact_checkpoints
		 WHERE tenant_id = $1 AND user_id = $2 AND summary_content ILIKE $3
		 LIMIT $4`,
		rc.TenantID, rc.UserID, queryPattern, maxResults,
	)
	if err == nil {
		for rows.Next() {
			var id, summary string
			if err := rows.Scan(&id, &summary); err == nil && r.keepPatternResult(summary, pattern, mode) {
				addBlock(fmt.Sprintf("checkpoint://%s", id), summary, "history")
			}
		}
		rows.Close()
	}

	rows, err = r.queryPatternRows(ctx, mode,
		`SELECT tenant_id, user_id, summary FROM user_profiles
		 WHERE tenant_id = $1 AND user_id = $2`,
		`SELECT tenant_id, user_id, summary FROM user_profiles
		 WHERE tenant_id = $1 AND user_id = $2 AND summary ILIKE $3
		 LIMIT $4`,
		rc.TenantID, rc.UserID, queryPattern, maxResults,
	)
	if err == nil {
		for rows.Next() {
			var tid, uid, summary string
			if err := rows.Scan(&tid, &uid, &summary); err == nil && r.keepPatternResult(summary, pattern, mode) {
				addBlock(fmt.Sprintf("profile://%s/%s", tid, uid), summary, "profile")
			}
		}
		rows.Close()
	}

	// 4. Search current session messages.
	if rc.SessionID != "" {
		rows, err = r.queryPatternRows(ctx, mode,
			`SELECT seq, content FROM session_messages
			 WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3
			 ORDER BY seq DESC`,
			`SELECT seq, content FROM session_messages
			 WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3 AND content ILIKE $4
			 ORDER BY seq DESC
			 LIMIT $5`,
			rc.TenantID, rc.UserID, rc.SessionID, queryPattern, maxResults,
		)
		if err == nil {
			for rows.Next() {
				var seq int
				var content string
				if err := rows.Scan(&seq, &content); err == nil && r.keepPatternResult(content, pattern, mode) {
					addBlock(fmt.Sprintf("session://%s/msg/%d", rc.SessionID, seq), content, "history")
				}
			}
			rows.Close()
		}
	}

	// Sort by score (pattern search doesn't have scores, sort by content length as proxy).
	sort.Slice(blocks, func(i, j int) bool {
		return len(blocks[i].Content) > len(blocks[j].Content)
	})

	return blocks, nil
}

func (r *RetrievalEngine) queryPatternRows(ctx context.Context, mode, fullQuery, keywordQuery string, args ...interface{}) (pgxRows, error) {
	if strings.EqualFold(mode, PatternModeKeyword) {
		return r.db.Query(ctx, keywordQuery, args...)
	}
	return r.db.Query(ctx, fullQuery, args[:len(args)-2]...)
}

func (r *RetrievalEngine) keepPatternResult(content, pattern, mode string) bool {
	ok, err := matchesPattern(content, pattern, mode)
	return err == nil && ok
}

// BatchLoadContent loads content by IDs from the vector_items table at the specified level.
func (r *RetrievalEngine) BatchLoadContent(ctx context.Context, ids []string, level types.ContentLevel) ([]types.ContentBlock, error) {
	if r.db == nil || len(ids) == 0 {
		return nil, nil
	}

	rows, err := r.db.Query(ctx,
		`SELECT id, content, uri FROM vector_items WHERE id = ANY($1)`,
		ids,
	)
	if err != nil {
		return nil, fmt.Errorf("retrieval: batch load: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var blocks []types.ContentBlock
	for rows.Next() {
		var id, content, uri string
		if err := rows.Scan(&id, &content, &uri); err != nil {
			return nil, fmt.Errorf("retrieval: scan batch item: %w", err)
		}
		if seen[id] {
			continue
		}
		seen[id] = true

		// Apply level-based content truncation.
		displayContent := applyContentLevel(content, level)

		blocks = append(blocks, types.ContentBlock{
			URI:     uri,
			Level:   level,
			Content: displayContent,
			Source:  "memory",
			Tokens:  estimateTokens(displayContent),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retrieval: batch rows error: %w", err)
	}

	return blocks, nil
}

// applyContentLevel truncates content based on the requested level.
func applyContentLevel(content string, level types.ContentLevel) string {
	switch level {
	case types.ContentL0:
		// L0: summary — first 200 chars.
		if len(content) > 200 {
			return content[:200]
		}
		return content
	case types.ContentL1:
		// L1: overview — first 1000 chars.
		if len(content) > 1000 {
			return content[:1000]
		}
		return content
	default:
		// L2: full content.
		return content
	}
}

// PatternSearchMode constants for supported modes.
const (
	PatternModeKeyword = "keyword"
	PatternModeGrep    = "grep"
	PatternModeGlob    = "glob"
)

// isValidPatternMode checks if the mode is supported.
func isValidPatternMode(mode string) bool {
	switch strings.ToLower(mode) {
	case PatternModeKeyword, PatternModeGrep, PatternModeGlob:
		return true
	}
	return false
}

type pgxRows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Close()
	Err() error
}

func matchesPattern(content, pattern, mode string) (bool, error) {
	switch strings.ToLower(mode) {
	case "", PatternModeKeyword:
		return strings.Contains(strings.ToLower(content), strings.ToLower(pattern)), nil
	case PatternModeGrep:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, err
		}
		return re.MatchString(content), nil
	case PatternModeGlob:
		return path.Match(pattern, content)
	default:
		return false, fmt.Errorf("invalid mode %q", mode)
	}
}
