package mock

import (
	"context"
	"math"
	"sync"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that MemoryVectorStore implements types.VectorStore.
var _ types.VectorStore = (*MemoryVectorStore)(nil)

// MemoryVectorStore is an in-memory implementation of types.VectorStore.
type MemoryVectorStore struct {
	mu        sync.RWMutex
	items     map[string]types.VectorItem
	dimension int
}

// NewMemoryVectorStore creates a new MemoryVectorStore.
func NewMemoryVectorStore() *MemoryVectorStore {
	return &MemoryVectorStore{
		items: make(map[string]types.VectorItem),
	}
}

// Init sets the expected vector dimension.
func (m *MemoryVectorStore) Init(_ context.Context, dimension int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dimension = dimension
	return nil
}

// Upsert inserts or updates vector items.
func (m *MemoryVectorStore) Upsert(_ context.Context, items []types.VectorItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, item := range items {
		m.items[item.ID] = item
	}
	return nil
}

// Search performs cosine similarity search over stored items.
func (m *MemoryVectorStore) Search(_ context.Context, query types.SearchQuery) ([]types.SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []types.SearchResult
	for _, item := range m.items {
		// Apply filter.
		if query.Filter != nil {
			if query.Filter.TenantID != "" && item.TenantID != query.Filter.TenantID {
				continue
			}
			if query.Filter.UserID != "" && item.UserID != query.Filter.UserID {
				continue
			}
		}

		score := cosineSimilarity(query.Vector, item.Vector)
		if score >= query.Threshold {
			results = append(results, types.SearchResult{
				Item:  item,
				Score: score,
			})
		}
	}

	// Sort by score descending.
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// Limit to TopK.
	if query.TopK > 0 && len(results) > query.TopK {
		results = results[:query.TopK]
	}

	return results, nil
}

// Delete removes items by their IDs.
func (m *MemoryVectorStore) Delete(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, id := range ids {
		delete(m.items, id)
	}
	return nil
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
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
