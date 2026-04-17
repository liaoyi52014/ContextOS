package store

import (
	"context"
	"fmt"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that MilvusVectorStore implements types.VectorStore.
var _ types.VectorStore = (*MilvusVectorStore)(nil)

// MilvusVectorStore is a placeholder VectorStore backed by Milvus.
// This is a stub for future Milvus integration.
type MilvusVectorStore struct{}

// NewMilvusVectorStore creates a new MilvusVectorStore.
func NewMilvusVectorStore() *MilvusVectorStore {
	return &MilvusVectorStore{}
}

// Init is not yet implemented for Milvus.
func (s *MilvusVectorStore) Init(ctx context.Context, dimension int) error {
	return fmt.Errorf("milvus_store: Init not implemented")
}

// Upsert is not yet implemented for Milvus.
func (s *MilvusVectorStore) Upsert(ctx context.Context, items []types.VectorItem) error {
	return fmt.Errorf("milvus_store: Upsert not implemented")
}

// Search is not yet implemented for Milvus.
func (s *MilvusVectorStore) Search(ctx context.Context, query types.SearchQuery) ([]types.SearchResult, error) {
	return nil, fmt.Errorf("milvus_store: Search not implemented")
}

// Delete is not yet implemented for Milvus.
func (s *MilvusVectorStore) Delete(ctx context.Context, ids []string) error {
	return fmt.Errorf("milvus_store: Delete not implemented")
}
