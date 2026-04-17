package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that OpenAILLMClient implements types.LLMClient.
var _ types.LLMClient = (*OpenAILLMClient)(nil)

// OpenAILLMClient implements types.LLMClient using an OpenAI-compatible API.
type OpenAILLMClient struct {
	apiBase string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAILLMClient creates a new OpenAILLMClient.
func NewOpenAILLMClient(apiBase, apiKey, model string) *OpenAILLMClient {
	return &OpenAILLMClient{
		apiBase: apiBase,
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{},
	}
}

// chatMessage is a single message in the chat completions request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatCompletionRequest is the request body for the chat completions API.
type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
}

// chatCompletionResponse is the response body from the chat completions API.
type chatCompletionResponse struct {
	Choices []chatChoice    `json:"choices"`
	Usage   chatUsage       `json:"usage"`
	Model   string          `json:"model"`
}

// chatChoice holds a single completion choice.
type chatChoice struct {
	Message chatMessage `json:"message"`
}

// chatUsage holds token usage information.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

const (
	llmMaxRetries    = 3
	llmBaseBackoff   = 1 * time.Second
	llmBackoffFactor = 2
)

// Complete sends a chat completion request to the OpenAI-compatible API.
// It retries up to 3 times with exponential backoff (1s, 2s, 4s) on transient failures.
func (c *OpenAILLMClient) Complete(ctx context.Context, req types.LLMRequest) (*types.LLMResponse, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}

	messages := make([]chatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = chatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}

	chatReq := chatCompletionRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("llm_client: marshal request: %w", err)
	}

	url := c.apiBase + "/v1/chat/completions"

	var lastErr error
	backoff := llmBaseBackoff

	for attempt := 0; attempt < llmMaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("llm_client: context cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= time.Duration(llmBackoffFactor)
		}

		resp, err := c.doRequest(ctx, url, bodyBytes)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}

	return nil, fmt.Errorf("llm_client: all %d attempts failed: %w", llmMaxRetries, lastErr)
}

// doRequest performs a single HTTP request to the chat completions endpoint.
func (c *OpenAILLMClient) doRequest(ctx context.Context, url string, body []byte) (*types.LLMResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm_client: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm_client: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm_client: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm_client: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("llm_client: unmarshal response: %w", err)
	}

	content := ""
	if len(chatResp.Choices) > 0 {
		content = chatResp.Choices[0].Message.Content
	}

	return &types.LLMResponse{
		Content:          content,
		PromptTokens:     chatResp.Usage.PromptTokens,
		CompletionTokens: chatResp.Usage.CompletionTokens,
		TotalTokens:      chatResp.Usage.TotalTokens,
		Model:            chatResp.Model,
	}, nil
}
