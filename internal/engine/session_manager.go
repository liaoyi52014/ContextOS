package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/contextos/contextos/internal/types"
)

// SessionConfig holds tuning parameters for the SessionManager.
type SessionConfig struct {
	MaxMessages         int
	LRUCacheSize        int
	LRUCacheTTLSec      int
	SyncQueueSize       int
	SyncBatchSize       int
	SyncFlushIntervalMs int
}

// sessionEntry wraps a Session with an expiration time for LRU TTL tracking.
type sessionEntry struct {
	session   *types.Session
	expiresAt time.Time
}

// SessionManager implements three-layer cache session management:
// local LRU -> Redis -> PostgreSQL.
type SessionManager struct {
	lru       *lru.Cache[string, *sessionEntry]
	cache     types.CacheStore
	store     types.SessionStore
	syncQueue *SyncQueue
	config    *SessionConfig
	mu        sync.RWMutex
}

// NewSessionManager creates a SessionManager with the given stores and config.
func NewSessionManager(cache types.CacheStore, store types.SessionStore, cfg SessionConfig) *SessionManager {
	if cfg.LRUCacheSize <= 0 {
		cfg.LRUCacheSize = 1000
	}
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 50
	}
	if cfg.LRUCacheTTLSec <= 0 {
		cfg.LRUCacheTTLSec = 5
	}
	if cfg.SyncQueueSize <= 0 {
		cfg.SyncQueueSize = 10000
	}
	if cfg.SyncBatchSize <= 0 {
		cfg.SyncBatchSize = 100
	}
	if cfg.SyncFlushIntervalMs <= 0 {
		cfg.SyncFlushIntervalMs = 500
	}

	l, err := lru.New[string, *sessionEntry](cfg.LRUCacheSize)
	if err != nil {
		// lru.New only errors on size <= 0, which we guard above.
		panic(fmt.Sprintf("session_manager: failed to create LRU cache: %v", err))
	}

	sq := NewSyncQueue(store, cfg.SyncQueueSize, cfg.SyncBatchSize, time.Duration(cfg.SyncFlushIntervalMs)*time.Millisecond)
	sq.Start()

	return &SessionManager{
		lru:       l,
		cache:     cache,
		store:     store,
		syncQueue: sq,
		config:    &cfg,
	}
}

// cacheKey returns the composite key for all cache layers.
func (m *SessionManager) cacheKey(tenantID, userID, sessionID string) string {
	return tenantID + ":" + userID + ":" + sessionID
}

// GetOrCreate performs a three-layer lookup and creates a new session if none exists.
func (m *SessionManager) GetOrCreate(ctx context.Context, rc types.RequestContext) (*types.Session, error) {
	key := m.cacheKey(rc.TenantID, rc.UserID, rc.SessionID)

	// Layer 1: local LRU
	if entry, ok := m.lru.Get(key); ok {
		if time.Now().Before(entry.expiresAt) {
			return entry.session, nil
		}
		m.lru.Remove(key)
	}

	// Layer 2: Redis cache
	data, err := m.cache.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("session_manager: redis get: %w", err)
	}
	if data != nil {
		var sess types.Session
		if err := json.Unmarshal(data, &sess); err == nil {
			m.putLRU(key, &sess)
			return &sess, nil
		}
	}

	// Layer 3: PostgreSQL
	sess, err := m.store.Load(ctx, rc.TenantID, rc.UserID, rc.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session_manager: pg load: %w", err)
	}
	if sess != nil {
		m.putLRU(key, sess)
		_ = m.putRedis(ctx, key, sess)
		return sess, nil
	}

	// Not found anywhere — create new session.
	now := time.Now()
	sess = &types.Session{
		ID:          generateSessionID(),
		TenantID:    rc.TenantID,
		UserID:      rc.UserID,
		Messages:    []types.Message{},
		Usage:       []types.UsageRecord{},
		Metadata:    map[string]interface{}{},
		CreatedAt:   now,
		UpdatedAt:   now,
		MaxMessages: m.config.MaxMessages,
	}
	// If the request specified a session ID, use it.
	if rc.SessionID != "" {
		sess.ID = rc.SessionID
	}

	m.putLRU(m.cacheKey(sess.TenantID, sess.UserID, sess.ID), sess)
	_ = m.putRedis(ctx, m.cacheKey(sess.TenantID, sess.UserID, sess.ID), sess)

	// Persist to PG synchronously for new sessions.
	if err := m.store.Save(ctx, sess); err != nil {
		return nil, fmt.Errorf("session_manager: pg save new session: %w", err)
	}

	return sess, nil
}

// AddMessage appends a message to the session, trims to MaxMessages,
// writes to Redis first, and enqueues an async PG sync.
func (m *SessionManager) AddMessage(ctx context.Context, session *types.Session, msg types.Message) error {
	m.mu.Lock()
	session.Messages = append(session.Messages, msg)
	if session.MaxMessages > 0 && len(session.Messages) > session.MaxMessages {
		session.Messages = session.Messages[len(session.Messages)-session.MaxMessages:]
	}
	session.UpdatedAt = time.Now()
	m.mu.Unlock()

	key := m.cacheKey(session.TenantID, session.UserID, session.ID)
	m.putLRU(key, session)

	if err := m.putRedis(ctx, key, session); err != nil {
		return fmt.Errorf("session_manager: redis write: %w", err)
	}

	// Enqueue for async PG write.
	item := &SyncItem{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		UserID:    session.UserID,
		Message:   msg,
		EnqueueAt: time.Now(),
	}
	return m.syncQueue.Enqueue(item)
}

// RecordUsage appends usage records to the session, updates aggregated metadata,
// writes to Redis, and enqueues for PG sync.
func (m *SessionManager) RecordUsage(ctx context.Context, session *types.Session, records []types.UsageRecord) error {
	m.mu.Lock()
	session.Usage = append(session.Usage, records...)
	// Update aggregated metadata counts.
	if session.Metadata == nil {
		session.Metadata = map[string]interface{}{}
	}
	for _, r := range records {
		if r.URI != "" {
			session.Metadata["contexts_used"] = toInt(session.Metadata["contexts_used"]) + 1
		}
		if r.SkillName != "" {
			session.Metadata["skills_used"] = toInt(session.Metadata["skills_used"]) + 1
		}
		if r.ToolName != "" {
			session.Metadata["tools_used"] = toInt(session.Metadata["tools_used"]) + 1
		}
	}
	session.UpdatedAt = time.Now()
	m.mu.Unlock()

	key := m.cacheKey(session.TenantID, session.UserID, session.ID)
	m.putLRU(key, session)

	if err := m.putRedis(ctx, key, session); err != nil {
		return fmt.Errorf("session_manager: redis write usage: %w", err)
	}

	// Enqueue a sync item for the session save (usage update).
	item := &SyncItem{
		SessionID:    session.ID,
		TenantID:     session.TenantID,
		UserID:       session.UserID,
		Session:      cloneSession(session),
		UsageRecords: cloneUsageRecords(records),
		EnqueueAt:    time.Now(),
	}
	return m.syncQueue.Enqueue(item)
}

// Clone creates a deep copy of the session with a new ID.
func (m *SessionManager) Clone(session *types.Session) *types.Session {
	data, err := json.Marshal(session)
	if err != nil {
		return nil
	}
	var cloned types.Session
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil
	}
	cloned.ID = generateSessionID()
	return &cloned
}

// Stop gracefully shuts down the SyncQueue, flushing remaining items.
func (m *SessionManager) Stop() {
	m.syncQueue.Stop()
}

// --- internal helpers ---

func (m *SessionManager) putLRU(key string, sess *types.Session) {
	m.lru.Add(key, &sessionEntry{
		session:   sess,
		expiresAt: time.Now().Add(time.Duration(m.config.LRUCacheTTLSec) * time.Second),
	})
}

func (m *SessionManager) putRedis(ctx context.Context, key string, sess *types.Session) error {
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return m.cache.Set(ctx, key, data, time.Duration(m.config.LRUCacheTTLSec*2)*time.Second)
}

// generateSessionID produces a UUID v4 string using crypto/rand.
func generateSessionID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// toInt safely extracts an int from an interface{} value.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}

func cloneSession(session *types.Session) *types.Session {
	data, err := json.Marshal(session)
	if err != nil {
		return nil
	}
	var cloned types.Session
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil
	}
	return &cloned
}

func cloneUsageRecords(records []types.UsageRecord) []types.UsageRecord {
	data, err := json.Marshal(records)
	if err != nil {
		return nil
	}
	var cloned []types.UsageRecord
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil
	}
	return cloned
}
