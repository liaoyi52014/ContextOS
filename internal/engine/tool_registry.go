package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/contextos/contextos/internal/types"
)

// ToolRegistry manages dynamic tool registration and execution.
type ToolRegistry struct {
	tools map[string]types.Tool
	mu    sync.RWMutex
}

// NewToolRegistry creates an empty ToolRegistry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]types.Tool),
	}
}

// Register adds a tool to the registry. Returns an error if a tool with the
// same name is already registered.
func (r *ToolRegistry) Register(tool types.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool_registry: tool %q already registered", name)
	}
	r.tools[name] = tool
	return nil
}

// Unregister removes a tool from the registry by name. No-op if not found.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Get retrieves a tool by name.
func (r *ToolRegistry) Get(name string) (types.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Execute looks up a tool by name and calls its Execute method.
func (r *ToolRegistry) Execute(ctx context.Context, rc types.RequestContext, name string, params map[string]interface{}) (string, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("tool_registry: tool %q not found", name)
	}
	return tool.Execute(ctx, rc, params)
}

// ListDefinitions returns a list of definition maps for all registered tools.
// Each entry contains name, description, schema, and type.
func (r *ToolRegistry) ListDefinitions() []map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]map[string]interface{}, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, map[string]interface{}{
			"name":        tool.Name(),
			"description": tool.Description(),
			"schema":      tool.Schema(),
			"type":        string(tool.Type()),
		})
	}
	return defs
}

// SkillToolProxy wraps a SkillToolBinding as a types.Tool implementation.
// The Execute method is a placeholder; actual binding resolution is future work.
type SkillToolProxy struct {
	binding  types.SkillToolBinding
	toolType types.ToolType
}

// NewSkillToolProxy creates a SkillToolProxy from a SkillToolBinding.
// The toolType is inferred from the binding string prefix:
//   - "command:*" -> ToolTypeCommand
//   - "mcp:*"     -> ToolTypeMCP
//   - default     -> ToolTypeSkill
func NewSkillToolProxy(binding types.SkillToolBinding) *SkillToolProxy {
	tt := types.ToolTypeSkill
	if len(binding.Binding) > 0 {
		switch {
		case hasPrefix(binding.Binding, "command:"):
			tt = types.ToolTypeCommand
		case hasPrefix(binding.Binding, "mcp:"):
			tt = types.ToolTypeMCP
		}
	}
	return &SkillToolProxy{
		binding:  binding,
		toolType: tt,
	}
}

func (p *SkillToolProxy) Name() string                   { return p.binding.Name }
func (p *SkillToolProxy) Description() string            { return p.binding.Description }
func (p *SkillToolProxy) Schema() map[string]interface{} { return p.binding.InputSchema }
func (p *SkillToolProxy) Type() types.ToolType           { return p.toolType }

// Execute resolves builtin and command bindings directly. MCP bindings are
// returned as a serialized envelope until a transport-backed executor exists.
func (p *SkillToolProxy) Execute(ctx context.Context, rc types.RequestContext, params map[string]interface{}) (string, error) {
	switch {
	case hasPrefix(p.binding.Binding, "builtin:"):
		return executeBuiltinTool(strings.TrimPrefix(p.binding.Binding, "builtin:"), params)
	case hasPrefix(p.binding.Binding, "command:"):
		return executeCommandTool(ctx, strings.TrimPrefix(p.binding.Binding, "command:"), params)
	case hasPrefix(p.binding.Binding, "mcp:"):
		payload := map[string]interface{}{
			"binding":    strings.TrimPrefix(p.binding.Binding, "mcp:"),
			"request":    params,
			"tenant_id":  rc.TenantID,
			"user_id":    rc.UserID,
			"session_id": rc.SessionID,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		return executeBuiltinTool("echo", params)
	}
}

// hasPrefix checks if s starts with prefix. Avoids importing strings for a
// single helper.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func executeBuiltinTool(name string, params map[string]interface{}) (string, error) {
	switch name {
	case "", "echo":
		if input, ok := params["input"].(string); ok {
			return input, nil
		}
		data, err := json.Marshal(params)
		if err != nil {
			return "", err
		}
		return string(data), nil
	case "json":
		data, err := json.Marshal(params)
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("skill_tool_proxy: unsupported builtin binding %q", name)
	}
}

func executeCommandTool(ctx context.Context, commandSpec string, params map[string]interface{}) (string, error) {
	fields := strings.Fields(commandSpec)
	if len(fields) == 0 {
		return "", fmt.Errorf("skill_tool_proxy: empty command binding")
	}
	if input, ok := params["input"].(string); ok && input != "" {
		fields = append(fields, input)
	}

	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(params) > 0 {
		data, err := json.Marshal(params)
		if err != nil {
			return "", err
		}
		if _, ok := params["input"].(string); !ok {
			cmd.Stdin = bytes.NewReader(data)
		}
	}
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}
	return out, nil
}
