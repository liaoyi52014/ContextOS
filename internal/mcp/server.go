package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/contextos/contextos/internal/types"
)

// MCPToolDef describes a single MCP tool exposed by the server.
type MCPToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// MCPServer exposes ContextOS engine capabilities as MCP tools.
// The actual MCP protocol transport (stdio/SSE) is future work;
// this implementation provides the tool registry and dispatch logic.
type MCPServer struct {
	engine types.Engine
	apiKey string
}

// NewMCPServer creates a new MCPServer backed by the given engine.
func NewMCPServer(engine types.Engine, apiKey string) *MCPServer {
	return &MCPServer{
		engine: engine,
		apiKey: apiKey,
	}
}

// ListTools returns the MCP tool definitions exposed by this server.
func (s *MCPServer) ListTools() []MCPToolDef {
	baseProps := map[string]interface{}{
		"tenant_id":  map[string]interface{}{"type": "string", "description": "Tenant identifier"},
		"user_id":    map[string]interface{}{"type": "string", "description": "User identifier"},
		"session_id": map[string]interface{}{"type": "string", "description": "Session identifier"},
	}

	return []MCPToolDef{
		{
			Name:        "context_assemble",
			Description: "Assemble context for a session with the given query and token budget",
			InputSchema: mergeSchema(baseProps, map[string]interface{}{
				"query":        map[string]interface{}{"type": "string", "description": "The query to assemble context for"},
				"token_budget": map[string]interface{}{"type": "integer", "description": "Maximum token budget"},
			}, []string{"tenant_id", "user_id", "session_id", "query"}),
		},
		{
			Name:        "memory_search",
			Description: "Search memories matching a query",
			InputSchema: mergeSchema(baseProps, map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search query"},
				"limit": map[string]interface{}{"type": "integer", "description": "Maximum number of results"},
			}, []string{"tenant_id", "user_id", "query"}),
		},
		{
			Name:        "memory_store",
			Description: "Store a new memory fact",
			InputSchema: mergeSchema(baseProps, map[string]interface{}{
				"content":  map[string]interface{}{"type": "string", "description": "Memory content to store"},
				"metadata": map[string]interface{}{"type": "object", "description": "Optional metadata key-value pairs"},
			}, []string{"tenant_id", "user_id", "content"}),
		},
		{
			Name:        "memory_forget",
			Description: "Delete a memory by ID",
			InputSchema: mergeSchema(baseProps, map[string]interface{}{
				"memory_id": map[string]interface{}{"type": "string", "description": "ID of the memory to delete"},
			}, []string{"tenant_id", "user_id", "memory_id"}),
		},
		{
			Name:        "session_summary",
			Description: "Get a summary of the current session",
			InputSchema: mergeSchema(baseProps, nil, []string{"tenant_id", "user_id", "session_id"}),
		},
	}
}

// ExecuteTool dispatches a tool call to the underlying engine.
func (s *MCPServer) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	tenantID := stringArg(args, "tenant_id", "default")
	userID := stringArg(args, "user_id", "default")
	sessionID := stringArg(args, "session_id", "")
	rc := types.RequestContext{TenantID: tenantID, UserID: userID, SessionID: sessionID}

	switch toolName {
	case "context_assemble":
		query := stringArg(args, "query", "")
		if query == "" {
			return "", fmt.Errorf("mcp: context_assemble requires 'query'")
		}
		budget := intArg(args, "token_budget", 0)
		req := types.AssembleRequest{
			SessionID:   sessionID,
			Query:       query,
			TokenBudget: budget,
		}
		resp, err := s.engine.Assemble(ctx, rc, req)
		if err != nil {
			return "", fmt.Errorf("mcp: context_assemble: %w", err)
		}
		return marshalResult(resp)

	case "memory_search":
		query := stringArg(args, "query", "")
		if query == "" {
			return "", fmt.Errorf("mcp: memory_search requires 'query'")
		}
		limit := intArg(args, "limit", 10)
		results, err := s.engine.SearchMemory(ctx, rc, query, limit)
		if err != nil {
			return "", fmt.Errorf("mcp: memory_search: %w", err)
		}
		return marshalResult(results)

	case "memory_store":
		content := stringArg(args, "content", "")
		if content == "" {
			return "", fmt.Errorf("mcp: memory_store requires 'content'")
		}
		metadata := stringMapArg(args, "metadata")
		if err := s.engine.StoreMemory(ctx, rc, content, metadata); err != nil {
			return "", fmt.Errorf("mcp: memory_store: %w", err)
		}
		return `{"status":"stored"}`, nil

	case "memory_forget":
		memoryID := stringArg(args, "memory_id", "")
		if memoryID == "" {
			return "", fmt.Errorf("mcp: memory_forget requires 'memory_id'")
		}
		if err := s.engine.ForgetMemory(ctx, rc, memoryID); err != nil {
			return "", fmt.Errorf("mcp: memory_forget: %w", err)
		}
		return `{"status":"deleted"}`, nil

	case "session_summary":
		summary, err := s.engine.GetSessionSummary(ctx, rc)
		if err != nil {
			return "", fmt.Errorf("mcp: session_summary: %w", err)
		}
		return marshalResult(map[string]string{"summary": summary})

	default:
		return "", fmt.Errorf("mcp: unknown tool %q", toolName)
	}
}

// mergeSchema builds a JSON Schema object with the given properties and required fields.
func mergeSchema(base, extra map[string]interface{}, required []string) map[string]interface{} {
	props := make(map[string]interface{})
	for k, v := range base {
		props[k] = v
	}
	for k, v := range extra {
		props[k] = v
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// stringArg extracts a string argument from the args map.
func stringArg(args map[string]interface{}, key, fallback string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fallback
}

// intArg extracts an integer argument from the args map.
func intArg(args map[string]interface{}, key string, fallback int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return fallback
}

// stringMapArg extracts a map[string]string argument from the args map.
func stringMapArg(args map[string]interface{}, key string) map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			result[k] = s
		}
	}
	return result
}

// marshalResult serializes a value to JSON string.
func marshalResult(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("mcp: marshal result: %w", err)
	}
	return string(data), nil
}

type stdioRequest struct {
	ID     interface{}            `json:"id"`
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params,omitempty"`
}

type stdioResponse struct {
	ID     interface{} `json:"id,omitempty"`
	Result interface{} `json:"result,omitempty"`
	Error  interface{} `json:"error,omitempty"`
}

// ServeStdio handles line-delimited JSON requests on stdin/stdout.
func (s *MCPServer) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		var req stdioRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if err := enc.Encode(stdioResponse{Error: map[string]interface{}{"message": err.Error()}}); err != nil {
				return err
			}
			continue
		}
		resp := stdioResponse{ID: req.ID}
		switch req.Method {
		case "tools/list":
			resp.Result = map[string]interface{}{"tools": s.ListTools()}
		case "tools/call":
			params := req.Params
			name, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]interface{})
			result, err := s.ExecuteTool(ctx, name, args)
			if err != nil {
				resp.Error = map[string]interface{}{"message": err.Error()}
			} else {
				var decoded interface{}
				if json.Unmarshal([]byte(result), &decoded) == nil {
					resp.Result = decoded
				} else {
					resp.Result = result
				}
			}
		default:
			resp.Error = map[string]interface{}{"message": fmt.Sprintf("unsupported method %q", req.Method)}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}
