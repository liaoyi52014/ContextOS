package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that MemorySessionStore implements types.SessionStore.
var _ types.SessionStore = (*MemorySessionStore)(nil)
var _ types.SessionScopeLister = (*MemorySessionStore)(nil)

// MemorySessionStore is an in-memory implementation of types.SessionStore.
type MemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*types.Session // keyed by "tenant:user:session"
}

// NewMemorySessionStore creates a new MemorySessionStore.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		sessions: make(map[string]*types.Session),
	}
}

func sessionKey(tenantID, userID, sessionID string) string {
	return fmt.Sprintf("%s:%s:%s", tenantID, userID, sessionID)
}

// deepCopySession returns a deep copy of a session to prevent external mutation.
func deepCopySession(s *types.Session) *types.Session {
	data, _ := json.Marshal(s)
	var copy types.Session
	_ = json.Unmarshal(data, &copy)
	return &copy
}

// Load retrieves a session. Returns nil, nil if not found.
func (m *MemorySessionStore) Load(_ context.Context, tenantID, userID, sessionID string) (*types.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[sessionKey(tenantID, userID, sessionID)]
	if !ok {
		return nil, nil
	}
	return deepCopySession(s), nil
}

// Save stores a session (upsert).
func (m *MemorySessionStore) Save(_ context.Context, session *types.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[sessionKey(session.TenantID, session.UserID, session.ID)] = deepCopySession(session)
	return nil
}

// Delete removes a session.
func (m *MemorySessionStore) Delete(_ context.Context, tenantID, userID, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, sessionKey(tenantID, userID, sessionID))
	return nil
}

// List returns metadata for all sessions belonging to a tenant+user.
func (m *MemorySessionStore) List(_ context.Context, tenantID, userID string) ([]*types.SessionMeta, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	prefix := fmt.Sprintf("%s:%s:", tenantID, userID)
	var result []*types.SessionMeta
	for k, s := range m.sessions {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result = append(result, &types.SessionMeta{
				ID:           s.ID,
				TenantID:     s.TenantID,
				UserID:       s.UserID,
				MessageCount: len(s.Messages),
				CreatedAt:    s.CreatedAt,
				UpdatedAt:    s.UpdatedAt,
			})
		}
	}
	return result, nil
}

// ListScopes returns distinct tenant/user pairs that contain sessions.
func (m *MemorySessionStore) ListScopes(_ context.Context) ([]types.SessionScope, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seen := make(map[string]types.SessionScope)
	for _, s := range m.sessions {
		key := s.TenantID + ":" + s.UserID
		seen[key] = types.SessionScope{TenantID: s.TenantID, UserID: s.UserID}
	}
	result := make([]types.SessionScope, 0, len(seen))
	for _, scope := range seen {
		result = append(result, scope)
	}
	return result, nil
}
