package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that OpenAIEmbeddingProvider implements types.EmbeddingProvider.
var _ types.EmbeddingProvider = (*OpenAIEmbeddingProvider)(nil)

// OpenAIEmbeddingProvider implements types.EmbeddingProvider using an OpenAI-compatible API.
type OpenAIEmbeddingProvider struct {
	apiBase   string
	apiKey    string
	model     string
	dimension int
	client    *http.Client
}

// NewOpenAIEmbeddingProvider creates a new OpenAIEmbeddingProvider.
func NewOpenAIEmbeddingProvider(apiBase, apiKey, model string, dimension int) *OpenAIEmbeddingProvider {
	return &OpenAIEmbeddingProvider{
		apiBase:   apiBase,
		apiKey:    apiKey,
		model:     model,
		dimension: dimension,
		client:    &http.Client{},
	}
}

// embeddingRequest is the request body for the embeddings API.
type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// embeddingResponse is the response body from the embeddings API.
type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

// embeddingData holds a single embedding result.
type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// Embed generates embeddings for the given texts using the OpenAI-compatible API.
func (p *OpenAIEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embeddingRequest{
		Input: texts,
		Model: p.model,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("embedding_provider: marshal request: %w", err)
	}

	url := p.apiBase + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("embedding_provider: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding_provider: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embedding_provider: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding_provider: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("embedding_provider: unmarshal response: %w", err)
	}

	// Build result ordered by index.
	result := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index >= 0 && d.Index < len(result) {
			result[d.Index] = d.Embedding
		}
	}

	return result, nil
}

// Dimension returns the configured embedding dimension.
func (p *OpenAIEmbeddingProvider) Dimension() int {
	return p.dimension
}
