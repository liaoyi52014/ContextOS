package mock

import (
	"context"
	"hash/fnv"
	"math"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that MockEmbeddingProvider implements types.EmbeddingProvider.
var _ types.EmbeddingProvider = (*MockEmbeddingProvider)(nil)

// MockEmbeddingProvider is a mock implementation of types.EmbeddingProvider.
// It generates deterministic fake embeddings based on text hashing.
type MockEmbeddingProvider struct {
	// Dim is the embedding dimension. Defaults to 128 if zero.
	Dim int
}

// NewMockEmbeddingProvider creates a new MockEmbeddingProvider with the given dimension.
// If dimension is 0, defaults to 128.
func NewMockEmbeddingProvider(dimension int) *MockEmbeddingProvider {
	if dimension <= 0 {
		dimension = 128
	}
	return &MockEmbeddingProvider{Dim: dimension}
}

// Embed generates deterministic fake embeddings for the given texts.
func (m *MockEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dim := m.Dim
	if dim <= 0 {
		dim = 128
	}

	result := make([][]float32, len(texts))
	for i, text := range texts {
		result[i] = deterministicEmbedding(text, dim)
	}
	return result, nil
}

// Dimension returns the configured embedding dimension.
func (m *MockEmbeddingProvider) Dimension() int {
	if m.Dim <= 0 {
		return 128
	}
	return m.Dim
}

// deterministicEmbedding generates a normalized embedding vector from text using FNV hashing.
func deterministicEmbedding(text string, dim int) []float32 {
	h := fnv.New64a()
	h.Write([]byte(text))
	seed := h.Sum64()

	vec := make([]float32, dim)
	var norm float64
	for i := range vec {
		// Simple LCG-style pseudo-random from seed.
		seed = seed*6364136223846793005 + 1442695040888963407
		val := float64(int64(seed>>33)) / float64(1<<30)
		vec[i] = float32(val)
		norm += val * val
	}

	// Normalize to unit vector.
	if norm > 0 {
		scale := float32(1.0 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= scale
		}
	}

	return vec
}
