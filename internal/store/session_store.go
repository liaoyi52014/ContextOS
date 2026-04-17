package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check that PGSessionStore implements types.SessionStore.
var _ types.SessionStore = (*PGSessionStore)(nil)
var _ types.MessageAppender = (*PGSessionStore)(nil)
var _ types.UsageRecordStore = (*PGSessionStore)(nil)
var _ types.CompactCheckpointStore = (*PGSessionStore)(nil)
var _ types.SessionScopeLister = (*PGSessionStore)(nil)

// PGSessionStore implements types.SessionStore backed by PostgreSQL.
type PGSessionStore struct {
	db *pgxpool.Pool
}

// NewPGSessionStore creates a new PGSessionStore.
func NewPGSessionStore(db *pgxpool.Pool) *PGSessionStore {
	return &PGSessionStore{db: db}
}

// Load retrieves a session by its composite key (tenant_id, user_id, id).
// Returns nil, nil if the session is not found.
func (s *PGSessionStore) Load(ctx context.Context, tenantID, userID, sessionID string) (*types.Session, error) {
	sess := &types.Session{}
	var (
		metadataRaw  []byte
		llmRaw       []byte
		embRaw       []byte
		commitCount  int
		contextsUsed int
		skillsUsed   int
		toolsUsed    int
	)

	err := s.db.QueryRow(ctx,
		`SELECT id, tenant_id, user_id, metadata, commit_count, contexts_used, skills_used, tools_used,
		        llm_token_usage, embedding_token_usage, max_messages, created_at, updated_at
		 FROM sessions
		 WHERE tenant_id = $1 AND user_id = $2 AND id = $3`,
		tenantID, userID, sessionID,
	).Scan(
		&sess.ID, &sess.TenantID, &sess.UserID,
		&metadataRaw, &commitCount, &contextsUsed, &skillsUsed, &toolsUsed,
		&llmRaw, &embRaw, &sess.MaxMessages,
		&sess.CreatedAt, &sess.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(metadataRaw, &sess.Metadata); err != nil {
		return nil, err
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	sess.Metadata["commit_count"] = commitCount
	sess.Metadata["contexts_used"] = contextsUsed
	sess.Metadata["skills_used"] = skillsUsed
	sess.Metadata["tools_used"] = toolsUsed
	if len(llmRaw) > 0 {
		var llmUsage map[string]int
		if err := json.Unmarshal(llmRaw, &llmUsage); err != nil {
			return nil, err
		}
		sess.Metadata["llm_token_usage"] = llmUsage
	}
	if len(embRaw) > 0 {
		var embUsage map[string]int
		if err := json.Unmarshal(embRaw, &embUsage); err != nil {
			return nil, err
		}
		sess.Metadata["embedding_token_usage"] = embUsage
	}

	// Load messages: fetch most recent max_messages ordered by seq DESC, then reverse.
	rows, err := s.db.Query(ctx,
		`SELECT role, content, metadata, tool_calls, created_at
		 FROM session_messages
		 WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3
		 ORDER BY seq DESC
		 LIMIT $4`,
		tenantID, userID, sessionID, sess.MaxMessages,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []types.Message
	for rows.Next() {
		var msg types.Message
		var metaRaw, toolCallsRaw []byte

		if err := rows.Scan(&msg.Role, &msg.Content, &metaRaw, &toolCallsRaw, &msg.Timestamp); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(metaRaw, &msg.Metadata); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(toolCallsRaw, &msg.ToolCalls); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to chronological order.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	sess.Messages = messages

	return sess, nil
}

// Save upserts a session and appends new messages within a transaction.
func (s *PGSessionStore) Save(ctx context.Context, session *types.Session) error {
	metadataJSON, err := json.Marshal(session.Metadata)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()

	// Upsert session row.
	commitCount, contextsUsed, skillsUsed, toolsUsed, llmUsageJSON, embUsageJSON, err := aggregateSessionMetadata(session.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO sessions (id, tenant_id, user_id, metadata, commit_count, contexts_used, skills_used, tools_used,
		                      llm_token_usage, embedding_token_usage, max_messages, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 ON CONFLICT (tenant_id, user_id, id) DO UPDATE
		 SET metadata = EXCLUDED.metadata,
		     commit_count = EXCLUDED.commit_count,
		     contexts_used = EXCLUDED.contexts_used,
		     skills_used = EXCLUDED.skills_used,
		     tools_used = EXCLUDED.tools_used,
		     llm_token_usage = EXCLUDED.llm_token_usage,
		     embedding_token_usage = EXCLUDED.embedding_token_usage,
		     max_messages = EXCLUDED.max_messages,
		     updated_at = $13`,
		session.ID, session.TenantID, session.UserID,
		metadataJSON, commitCount, contextsUsed, skillsUsed, toolsUsed,
		llmUsageJSON, embUsageJSON, session.MaxMessages,
		session.CreatedAt, now,
	)
	if err != nil {
		return err
	}

	// Determine current max seq for this session.
	var maxSeq int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM session_messages
		 WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3`,
		session.TenantID, session.UserID, session.ID,
	).Scan(&maxSeq)
	if err != nil {
		return err
	}

	// Count existing messages to determine which are new.
	existingCount := maxSeq
	newMessages := session.Messages
	if existingCount > 0 && existingCount < len(newMessages) {
		newMessages = newMessages[existingCount:]
	} else if existingCount >= len(newMessages) {
		newMessages = nil
	}

	// Insert new messages with incrementing seq.
	for i, msg := range newMessages {
		seq := maxSeq + i + 1

		metaJSON, err := json.Marshal(msg.Metadata)
		if err != nil {
			return err
		}
		toolCallsJSON, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return err
		}

		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}

		_, err = tx.Exec(ctx,
			`INSERT INTO session_messages (tenant_id, user_id, session_id, seq, role, content, metadata, tool_calls, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			session.TenantID, session.UserID, session.ID,
			seq, msg.Role, msg.Content, metaJSON, toolCallsJSON, ts,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// Delete removes a session by its composite key. CASCADE handles session_messages.
func (s *PGSessionStore) Delete(ctx context.Context, tenantID, userID, sessionID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM sessions WHERE tenant_id = $1 AND user_id = $2 AND id = $3`,
		tenantID, userID, sessionID,
	)
	return err
}

// List returns session metadata for all sessions belonging to a tenant+user.
func (s *PGSessionStore) List(ctx context.Context, tenantID, userID string) ([]*types.SessionMeta, error) {
	rows, err := s.db.Query(ctx,
		`SELECT s.id, s.tenant_id, s.user_id,
		        COALESCE(mc.cnt, 0) AS message_count,
		        s.commit_count, s.contexts_used, s.skills_used, s.tools_used,
		        s.llm_token_usage, s.embedding_token_usage,
		        s.created_at, s.updated_at
		 FROM sessions s
		 LEFT JOIN (
		     SELECT tenant_id, user_id, session_id, COUNT(*) AS cnt
		     FROM session_messages
		     GROUP BY tenant_id, user_id, session_id
		 ) mc ON mc.tenant_id = s.tenant_id AND mc.user_id = s.user_id AND mc.session_id = s.id
		 WHERE s.tenant_id = $1 AND s.user_id = $2
		 ORDER BY s.updated_at DESC`,
		tenantID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*types.SessionMeta
	for rows.Next() {
		meta := &types.SessionMeta{}
		var llmRaw, embRaw []byte

		if err := rows.Scan(
			&meta.ID, &meta.TenantID, &meta.UserID,
			&meta.MessageCount,
			&meta.CommitCount, &meta.ContextsUsed, &meta.SkillsUsed, &meta.ToolsUsed,
			&llmRaw, &embRaw,
			&meta.CreatedAt, &meta.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if err := json.Unmarshal(llmRaw, &meta.LLMTokenUsage); err != nil {
			return nil, err
		}
		if meta.LLMTokenUsage == nil {
			meta.LLMTokenUsage = make(map[string]int)
		}
		if err := json.Unmarshal(embRaw, &meta.EmbeddingTokenUsage); err != nil {
			return nil, err
		}
		if meta.EmbeddingTokenUsage == nil {
			meta.EmbeddingTokenUsage = make(map[string]int)
		}

		result = append(result, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// ListScopes returns distinct tenant/user pairs that contain sessions.
func (s *PGSessionStore) ListScopes(ctx context.Context) ([]types.SessionScope, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT tenant_id, user_id FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scopes []types.SessionScope
	for rows.Next() {
		var scope types.SessionScope
		if err := rows.Scan(&scope.TenantID, &scope.UserID); err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

func (s *PGSessionStore) AppendMessage(ctx context.Context, tenantID, userID, sessionID string, msg types.Message) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var maxSeq int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM session_messages
		 WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3`,
		tenantID, userID, sessionID,
	).Scan(&maxSeq); err != nil {
		return err
	}

	metaJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		return err
	}
	toolCallsJSON, err := json.Marshal(msg.ToolCalls)
	if err != nil {
		return err
	}

	ts := msg.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO session_messages (tenant_id, user_id, session_id, seq, role, content, metadata, tool_calls, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		tenantID, userID, sessionID, maxSeq+1, msg.Role, msg.Content, metaJSON, toolCallsJSON, ts,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET updated_at = $1 WHERE tenant_id = $2 AND user_id = $3 AND id = $4`,
		time.Now().UTC(), tenantID, userID, sessionID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *PGSessionStore) SaveUsageRecords(ctx context.Context, tenantID, userID, sessionID string, records []types.UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	batch, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer batch.Rollback(ctx)

	for _, record := range records {
		recordID, err := randomID("usage")
		if err != nil {
			return err
		}
		ts := record.Timestamp
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if _, err := batch.Exec(ctx,
			`INSERT INTO session_usage_records (id, tenant_id, user_id, session_id, uri, skill_name, tool_name, input_summary, output_summary, success, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			recordID, tenantID, userID, sessionID, record.URI, record.SkillName, record.ToolName,
			record.InputSummary, record.OutputSummary, record.Success, ts,
		); err != nil {
			return err
		}
	}

	return batch.Commit(ctx)
}

func (s *PGSessionStore) SaveCompactCheckpoint(ctx context.Context, checkpoint types.CompactCheckpoint) error {
	memoryIDsJSON, err := json.Marshal(checkpoint.ExtractedMemoryIDs)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO compact_checkpoints (id, session_id, tenant_id, user_id, committed_at, source_turn_start, source_turn_end,
		                                  summary_content, extracted_memory_ids, prompt_tokens_used, completion_tokens_used)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		checkpoint.ID, checkpoint.SessionID, checkpoint.TenantID, checkpoint.UserID, checkpoint.CommittedAt,
		checkpoint.SourceTurnStart, checkpoint.SourceTurnEnd, checkpoint.SummaryContent,
		memoryIDsJSON, checkpoint.PromptTokensUsed, checkpoint.CompletionTokensUsed,
	)
	return err
}

func aggregateSessionMetadata(metadata map[string]interface{}) (int, int, int, int, []byte, []byte, error) {
	commitCount := toInt(metadata["commit_count"])
	contextsUsed := toInt(metadata["contexts_used"])
	skillsUsed := toInt(metadata["skills_used"])
	toolsUsed := toInt(metadata["tools_used"])

	llmUsageJSON, err := marshalUsageMap(metadata["llm_token_usage"])
	if err != nil {
		return 0, 0, 0, 0, nil, nil, err
	}
	embUsageJSON, err := marshalUsageMap(metadata["embedding_token_usage"])
	if err != nil {
		return 0, 0, 0, 0, nil, nil, err
	}
	return commitCount, contextsUsed, skillsUsed, toolsUsed, llmUsageJSON, embUsageJSON, nil
}

func marshalUsageMap(v interface{}) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	switch usage := v.(type) {
	case map[string]int:
		return json.Marshal(usage)
	case map[string]interface{}:
		converted := make(map[string]int, len(usage))
		for key, value := range usage {
			converted[key] = toInt(value)
		}
		return json.Marshal(converted)
	default:
		return json.Marshal(map[string]int{})
	}
}

func toInt(v interface{}) int {
	switch value := v.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func randomID(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%x", prefix, b[:]), nil
}
