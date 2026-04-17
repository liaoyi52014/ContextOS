package mock

import (
	"context"
	"fmt"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that MockLLMClient implements types.LLMClient.
var _ types.LLMClient = (*MockLLMClient)(nil)

// MockLLMClient is a configurable mock implementation of types.LLMClient.
type MockLLMClient struct {
	// CompleteFunc allows overriding the Complete behavior.
	// If nil, a default echo response is returned.
	CompleteFunc func(ctx context.Context, req types.LLMRequest) (*types.LLMResponse, error)
}

// NewMockLLMClient creates a new MockLLMClient with default echo behavior.
func NewMockLLMClient() *MockLLMClient {
	return &MockLLMClient{}
}

// Complete returns a configurable response, or a default echo response.
func (m *MockLLMClient) Complete(ctx context.Context, req types.LLMRequest) (*types.LLMResponse, error) {
	if m.CompleteFunc != nil {
		return m.CompleteFunc(ctx, req)
	}

	// Default: echo the last message content.
	content := "echo: (empty)"
	if len(req.Messages) > 0 {
		content = fmt.Sprintf("echo: %s", req.Messages[len(req.Messages)-1].Content)
	}

	return &types.LLMResponse{
		Content:          content,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		Model:            "mock-model",
	}, nil
}
