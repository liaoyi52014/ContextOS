package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Compile-time check that PGVectorStore implements types.VectorStore.
var _ types.VectorStore = (*PGVectorStore)(nil)

// PGVectorStore implements types.VectorStore backed by PostgreSQL with pgvector.
type PGVectorStore struct {
	db     *pgxpool.Pool
	logger *zap.Logger
}

// NewPGVectorStore creates a new PGVectorStore.
func NewPGVectorStore(db *pgxpool.Pool) *PGVectorStore {
	l, _ := zap.NewProduction()
	return &PGVectorStore{db: db, logger: l}
}

// Init dynamically adds vector columns and creates ivfflat indexes.
func (s *PGVectorStore) Init(ctx context.Context, dimension int) error {
	// Add embedding column to vector_items.
	sql1 := fmt.Sprintf(
		`ALTER TABLE vector_items ADD COLUMN IF NOT EXISTS embedding vector(%d)`, dimension,
	)
	if _, err := s.db.Exec(ctx, sql1); err != nil {
		s.logger.Warn("pgvector_store: failed to add embedding column to vector_items", zap.Error(err))
	}

	// Create ivfflat index on vector_items.
	sql2 := `CREATE INDEX IF NOT EXISTS idx_vector_embedding ON vector_items USING ivfflat (embedding vector_cosine_ops)`
	if _, err := s.db.Exec(ctx, sql2); err != nil {
		s.logger.Warn("pgvector_store: failed to create ivfflat index on vector_items", zap.Error(err))
	}

	// Add catalog_embedding column to skills.
	sql3 := fmt.Sprintf(
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS catalog_embedding vector(%d)`, dimension,
	)
	if _, err := s.db.Exec(ctx, sql3); err != nil {
		s.logger.Warn("pgvector_store: failed to add catalog_embedding column to skills", zap.Error(err))
	}

	return nil
}

// Upsert inserts or updates vector items in batch.
func (s *PGVectorStore) Upsert(ctx context.Context, items []types.VectorItem) error {
	for _, item := range items {
		metadataJSON, err := json.Marshal(item.Metadata)
		if err != nil {
			return fmt.Errorf("pgvector_store: marshal metadata: %w", err)
		}

		embeddingStr := formatVector(item.Vector)

		_, err = s.db.Exec(ctx,
			`INSERT INTO vector_items (id, tenant_id, user_id, content, uri, metadata, embedding)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (id) DO UPDATE
			 SET tenant_id = EXCLUDED.tenant_id,
			     user_id = EXCLUDED.user_id,
			     content = EXCLUDED.content,
			     uri = EXCLUDED.uri,
			     metadata = EXCLUDED.metadata,
			     embedding = EXCLUDED.embedding`,
			item.ID, item.TenantID, item.UserID,
			item.Content, item.URI, metadataJSON, embeddingStr,
		)
		if err != nil {
			return fmt.Errorf("pgvector_store: upsert item %s: %w", item.ID, err)
		}
	}
	return nil
}

// Search performs cosine similarity search with tenant/user filtering.
func (s *PGVectorStore) Search(ctx context.Context, query types.SearchQuery) ([]types.SearchResult, error) {
	embeddingStr := formatVector(query.Vector)

	// Build WHERE clause with tenant/user filters.
	conditions := []string{"1=1"}
	args := []interface{}{embeddingStr}
	argIdx := 2

	if query.Filter != nil {
		if query.Filter.TenantID != "" {
			conditions = append(conditions, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, query.Filter.TenantID)
			argIdx++
		}
		if query.Filter.UserID != "" {
			conditions = append(conditions, fmt.Sprintf("user_id = $%d", argIdx))
			args = append(args, query.Filter.UserID)
			argIdx++
		}
	}

	// Threshold filter: cosine similarity = 1 - cosine distance.
	conditions = append(conditions, fmt.Sprintf("1 - (embedding <=> $1) >= $%d", argIdx))
	args = append(args, query.Threshold)
	argIdx++

	topK := query.TopK
	if topK <= 0 {
		topK = 10
	}

	sql := fmt.Sprintf(
		`SELECT id, tenant_id, user_id, content, uri, metadata, embedding,
		        1 - (embedding <=> $1) AS score
		 FROM vector_items
		 WHERE %s
		 ORDER BY score DESC
		 LIMIT %d`,
		strings.Join(conditions, " AND "), topK,
	)

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector_store: search: %w", err)
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var item types.VectorItem
		var metadataRaw []byte
		var embeddingRaw string
		var score float64

		if err := rows.Scan(
			&item.ID, &item.TenantID, &item.UserID,
			&item.Content, &item.URI, &metadataRaw, &embeddingRaw, &score,
		); err != nil {
			return nil, fmt.Errorf("pgvector_store: scan result: %w", err)
		}

		if metadataRaw != nil {
			if err := json.Unmarshal(metadataRaw, &item.Metadata); err != nil {
				return nil, fmt.Errorf("pgvector_store: unmarshal metadata: %w", err)
			}
		}
		if item.Metadata == nil {
			item.Metadata = make(map[string]string)
		}

		results = append(results, types.SearchResult{
			Item:  item,
			Score: score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgvector_store: rows error: %w", err)
	}

	return results, nil
}

// Delete removes vector items by their IDs.
func (s *PGVectorStore) Delete(ctx context.Context, ids []string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM vector_items WHERE id = ANY($1)`,
		ids,
	)
	if err != nil {
		return fmt.Errorf("pgvector_store: delete: %w", err)
	}
	return nil
}

// formatVector converts a float32 slice to pgvector string format: [0.1,0.2,...].
func formatVector(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteByte(']')
	return b.String()
}
