package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/contextos/contextos/internal/types"
)

type stubEngine struct{}

func (stubEngine) Assemble(context.Context, types.RequestContext, types.AssembleRequest) (*types.AssembleResponse, error) {
	return &types.AssembleResponse{SystemPrompt: "hello"}, nil
}
func (stubEngine) Ingest(context.Context, types.RequestContext, types.IngestRequest) (*types.IngestResponse, error) {
	return &types.IngestResponse{}, nil
}
func (stubEngine) SearchMemory(context.Context, types.RequestContext, string, int) ([]types.SearchResult, error) {
	return []types.SearchResult{{Item: types.VectorItem{ID: "m1", Content: "fact"}}}, nil
}
func (stubEngine) StoreMemory(context.Context, types.RequestContext, string, map[string]string) error {
	return nil
}
func (stubEngine) ForgetMemory(context.Context, types.RequestContext, string) error { return nil }
func (stubEngine) GetSessionSummary(context.Context, types.RequestContext) (string, error) {
	return "summary", nil
}
func (stubEngine) ExecuteTool(context.Context, types.RequestContext, string, map[string]interface{}) (string, error) {
	return "", nil
}

func TestServeStdio_ListToolsAndCallTool(t *testing.T) {
	server := NewMCPServer(stubEngine{}, "key")

	input := bytes.NewBuffer(nil)
	output := bytes.NewBuffer(nil)
	enc := json.NewEncoder(input)
	if err := enc.Encode(map[string]interface{}{"id": "1", "method": "tools/list"}); err != nil {
		t.Fatalf("encode list request: %v", err)
	}
	if err := enc.Encode(map[string]interface{}{
		"id":     "2",
		"method": "tools/call",
		"params": map[string]interface{}{
			"name": "context_assemble",
			"arguments": map[string]interface{}{
				"tenant_id":  "t1",
				"user_id":    "u1",
				"session_id": "s1",
				"query":      "hello",
			},
		},
	}); err != nil {
		t.Fatalf("encode call request: %v", err)
	}

	if err := server.ServeStdio(context.Background(), input, output); err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}

	dec := json.NewDecoder(output)
	var resp1 map[string]interface{}
	if err := dec.Decode(&resp1); err != nil {
		t.Fatalf("decode response1: %v", err)
	}
	if resp1["error"] != nil {
		t.Fatalf("unexpected error response: %+v", resp1)
	}
	var resp2 map[string]interface{}
	if err := dec.Decode(&resp2); err != nil {
		t.Fatalf("decode response2: %v", err)
	}
	if resp2["error"] != nil {
		t.Fatalf("unexpected error response: %+v", resp2)
	}
}
