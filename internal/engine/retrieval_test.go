package engine

import (
	"context"
	"testing"

	"github.com/contextos/contextos/internal/types"
)

func TestPatternSearch_InvalidModeReturnsError(t *testing.T) {
	r := NewRetrievalEngine(nil, nil, RetrievalConfig{}, nil)

	_, err := r.PatternSearch(context.Background(), types.RequestContext{
		TenantID:  "t1",
		UserID:    "u1",
		SessionID: "s1",
	}, "hello", "bogus", 100)
	if err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestMatchesPattern_SupportsKeywordGrepAndGlob(t *testing.T) {
	tests := []struct {
		name    string
		content string
		pattern string
		mode    string
		want    bool
	}{
		{name: "keyword match", content: "Remember Go coding conventions", pattern: "go coding", mode: PatternModeKeyword, want: true},
		{name: "keyword no match", content: "Remember Go coding conventions", pattern: "python", mode: PatternModeKeyword, want: false},
		{name: "grep match", content: "order-1234 completed", pattern: `order-\d+`, mode: PatternModeGrep, want: true},
		{name: "grep no match", content: "order-ABCD completed", pattern: `order-\d+`, mode: PatternModeGrep, want: false},
		{name: "glob match", content: "feature/auth/login", pattern: "feature/*/login", mode: PatternModeGlob, want: true},
		{name: "glob no match", content: "feature/auth/reset", pattern: "feature/*/login", mode: PatternModeGlob, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matchesPattern(tt.content, tt.pattern, tt.mode)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}
