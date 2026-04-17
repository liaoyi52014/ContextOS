package engine

import (
	"context"
	"testing"

	"github.com/contextos/contextos/internal/types"
)

func TestSkillToolProxy_ExecuteBuiltinEcho(t *testing.T) {
	proxy := NewSkillToolProxy(types.SkillToolBinding{
		Name:    "echo_tool",
		Binding: "builtin:echo",
	})

	result, err := proxy.Execute(context.Background(), types.RequestContext{}, map[string]interface{}{
		"input": "hello",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "hello" {
		t.Fatalf("expected hello, got %q", result)
	}
}

func TestSkillToolProxy_ExecuteCommand(t *testing.T) {
	proxy := NewSkillToolProxy(types.SkillToolBinding{
		Name:    "cmd_tool",
		Binding: "command:/bin/echo",
	})

	result, err := proxy.Execute(context.Background(), types.RequestContext{}, map[string]interface{}{
		"input": "hello",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty command output")
	}
}
