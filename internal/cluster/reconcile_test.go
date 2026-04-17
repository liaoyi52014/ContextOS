package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

func TestReconcilerRun_RepopulatesCacheForKnownScopes(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	store := mock.NewMemorySessionStore()
	now := time.Now().UTC()
	if err := store.Save(context.Background(), &types.Session{
		ID:        "s1",
		TenantID:  "t1",
		UserID:    "u1",
		Messages:  []types.Message{{Role: "user", Content: "hello"}},
		Metadata:  map[string]interface{}{},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("preload session: %v", err)
	}

	r := NewReconciler(cache, store)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	data, err := cache.Get(context.Background(), "t1:u1:s1")
	if err != nil {
		t.Fatalf("cache.Get failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected reconciler to repopulate session cache")
	}
}
