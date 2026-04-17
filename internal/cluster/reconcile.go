package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// Reconciler compares Redis cache and PostgreSQL session data,
// fixing inconsistencies by re-populating the cache from the store.
type Reconciler struct {
	cache types.CacheStore
	store types.SessionStore
}

// NewReconciler creates a new Reconciler.
func NewReconciler(cache types.CacheStore, store types.SessionStore) *Reconciler {
	return &Reconciler{cache: cache, store: store}
}

// Run performs a reconciliation pass. For v1, it loads sessions from PG
// and ensures they exist in Redis. This is a simple forward-fill strategy.
func (r *Reconciler) Run(ctx context.Context) error {
	lister, ok := r.store.(types.SessionScopeLister)
	if !ok {
		return nil
	}

	scopes, err := lister.ListScopes(ctx)
	if err != nil {
		return fmt.Errorf("list reconciliation scopes: %w", err)
	}
	for _, scope := range scopes {
		if err := r.ReconcileSessions(ctx, scope.TenantID, scope.UserID); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileSessions checks that all sessions for a given tenant+user
// exist in the cache, and re-populates any missing entries.
func (r *Reconciler) ReconcileSessions(ctx context.Context, tenantID, userID string) error {
	metas, err := r.store.List(ctx, tenantID, userID)
	if err != nil {
		return fmt.Errorf("listing sessions for reconciliation: %w", err)
	}

	for _, meta := range metas {
		key := fmt.Sprintf("%s:%s:%s", tenantID, userID, meta.ID)
		cached, err := r.cache.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("checking cache for session %s: %w", meta.ID, err)
		}
		if cached != nil {
			continue // already in cache
		}

		// Load full session from PG and populate cache.
		session, err := r.store.Load(ctx, tenantID, userID, meta.ID)
		if err != nil {
			return fmt.Errorf("loading session %s from store: %w", meta.ID, err)
		}
		if session == nil {
			continue
		}

		data, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("marshaling session %s: %w", meta.ID, err)
		}

		if err := r.cache.Set(ctx, key, data, 30*time.Minute); err != nil {
			return fmt.Errorf("setting cache for session %s: %w", meta.ID, err)
		}
	}
	return nil
}
