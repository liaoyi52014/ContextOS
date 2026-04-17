package sdk

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

// SDKClient provides a unified client for the ContextOS engine.
// It supports two modes:
//   - Embedded mode: calls the engine directly via the types.Engine interface.
//   - Remote mode: calls the HTTP API at apiBase with apiKey authentication.
type SDKClient struct {
	engine  types.Engine  // embedded mode: direct engine reference
	apiBase string        // remote mode: HTTP API base URL
	apiKey  string        // remote mode: API key for authentication
	client  *http.Client  // remote mode: HTTP client
}

// NewEmbeddedSDK creates an SDKClient that delegates directly to the given engine.
func NewEmbeddedSDK(engine types.Engine) *SDKClient {
	return &SDKClient{
		engine: engine,
	}
}

// NewRemoteSDK creates an SDKClient that calls the HTTP API at apiBase.
func NewRemoteSDK(apiBase, apiKey string) *SDKClient {
	return &SDKClient{
		apiBase: apiBase,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// isEmbedded returns true if the client operates in embedded mode.
func (s *SDKClient) isEmbedded() bool {
	return s.engine != nil
}

// Assemble performs context assembly for the given session and query.
func (s *SDKClient) Assemble(ctx context.Context, tenantID, userID, sessionID, query string, tokenBudget int) (*types.AssembleResponse, error) {
	if s.isEmbedded() {
		rc := types.RequestContext{TenantID: tenantID, UserID: userID, SessionID: sessionID}
		req := types.AssembleRequest{
			SessionID:   sessionID,
			Query:       query,
			TokenBudget: tokenBudget,
		}
		return s.engine.Assemble(ctx, rc, req)
	}

	body := map[string]interface{}{
		"session_id":   sessionID,
		"query":        query,
		"token_budget": tokenBudget,
	}
	var resp types.AssembleResponse
	if err := s.doPost(ctx, "/api/v1/context/assemble", tenantID, userID, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Ingest writes messages into a session.
func (s *SDKClient) Ingest(ctx context.Context, tenantID, userID, sessionID string, messages []types.Message) (*types.IngestResponse, error) {
	if s.isEmbedded() {
		rc := types.RequestContext{TenantID: tenantID, UserID: userID, SessionID: sessionID}
		req := types.IngestRequest{
			SessionID: sessionID,
			Messages:  messages,
		}
		return s.engine.Ingest(ctx, rc, req)
	}

	body := map[string]interface{}{
		"session_id": sessionID,
		"messages":   messages,
	}
	var resp types.IngestResponse
	if err := s.doPost(ctx, "/api/v1/context/ingest", tenantID, userID, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SearchMemory searches for memories matching the query.
func (s *SDKClient) SearchMemory(ctx context.Context, tenantID, userID, query string, limit int) ([]types.SearchResult, error) {
	if s.isEmbedded() {
		rc := types.RequestContext{TenantID: tenantID, UserID: userID}
		return s.engine.SearchMemory(ctx, rc, query, limit)
	}

	body := map[string]interface{}{
		"query": query,
		"limit": limit,
	}
	var resp struct {
		Results []types.SearchResult `json:"results"`
	}
	if err := s.doPost(ctx, "/api/v1/memory/search", tenantID, userID, body, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// StoreMemory stores a memory fact.
func (s *SDKClient) StoreMemory(ctx context.Context, tenantID, userID, content string, metadata map[string]string) error {
	if s.isEmbedded() {
		rc := types.RequestContext{TenantID: tenantID, UserID: userID}
		return s.engine.StoreMemory(ctx, rc, content, metadata)
	}

	body := map[string]interface{}{
		"content":  content,
		"metadata": metadata,
	}
	return s.doPost(ctx, "/api/v1/memory/store", tenantID, userID, body, nil)
}

// ForgetMemory deletes a memory by ID.
func (s *SDKClient) ForgetMemory(ctx context.Context, tenantID, userID, memoryID string) error {
	if s.isEmbedded() {
		rc := types.RequestContext{TenantID: tenantID, UserID: userID}
		return s.engine.ForgetMemory(ctx, rc, memoryID)
	}

	return s.doDelete(ctx, "/api/v1/memory/"+memoryID, tenantID, userID)
}

// ExecuteTool executes a named tool with the given parameters.
func (s *SDKClient) ExecuteTool(ctx context.Context, tenantID, userID, sessionID, toolName string, params map[string]interface{}) (string, error) {
	if s.isEmbedded() {
		rc := types.RequestContext{TenantID: tenantID, UserID: userID, SessionID: sessionID}
		return s.engine.ExecuteTool(ctx, rc, toolName, params)
	}

	body := map[string]interface{}{
		"session_id": sessionID,
		"tool_name":  toolName,
		"params":     params,
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := s.doPost(ctx, "/api/v1/tools/execute", tenantID, userID, body, &resp); err != nil {
		return "", err
	}
	return resp.Result, nil
}

// doPost sends a JSON POST request to the remote API.
func (s *SDKClient) doPost(ctx context.Context, path, tenantID, userID string, body interface{}, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sdk: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sdk: create request: %w", err)
	}
	s.setHeaders(req, tenantID, userID)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sdk: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return s.parseError(resp)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("sdk: decode response: %w", err)
		}
	}
	return nil
}

// doDelete sends a DELETE request to the remote API.
func (s *SDKClient) doDelete(ctx context.Context, path, tenantID, userID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.apiBase+path, nil)
	if err != nil {
		return fmt.Errorf("sdk: create request: %w", err)
	}
	s.setHeaders(req, tenantID, userID)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sdk: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return s.parseError(resp)
	}
	return nil
}

// setHeaders sets common headers for remote API requests.
func (s *SDKClient) setHeaders(req *http.Request, tenantID, userID string) {
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("X-API-Key", s.apiKey)
	}
	if tenantID != "" {
		req.Header.Set("X-Tenant-ID", tenantID)
	}
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
}

// parseError reads an error response from the remote API.
func (s *SDKClient) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("sdk: API error (status %d): %s", resp.StatusCode, string(body))
}
