package store

import (
	"context"
	"fmt"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that ESVectorStore implements types.VectorStore.
var _ types.VectorStore = (*ESVectorStore)(nil)

// ESVectorStore is a placeholder VectorStore backed by Elasticsearch.
// This is a stub for future Elasticsearch integration.
type ESVectorStore struct{}

// NewESVectorStore creates a new ESVectorStore.
func NewESVectorStore() *ESVectorStore {
	return &ESVectorStore{}
}

// Init is not yet implemented for Elasticsearch.
func (s *ESVectorStore) Init(ctx context.Context, dimension int) error {
	return fmt.Errorf("es_store: Init not implemented")
}

// Upsert is not yet implemented for Elasticsearch.
func (s *ESVectorStore) Upsert(ctx context.Context, items []types.VectorItem) error {
	return fmt.Errorf("es_store: Upsert not implemented")
}

// Search is not yet implemented for Elasticsearch.
func (s *ESVectorStore) Search(ctx context.Context, query types.SearchQuery) ([]types.SearchResult, error) {
	return nil, fmt.Errorf("es_store: Search not implemented")
}

// Delete is not yet implemented for Elasticsearch.
func (s *ESVectorStore) Delete(ctx context.Context, ids []string) error {
	return fmt.Errorf("es_store: Delete not implemented")
}
