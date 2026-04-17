package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

const apiKeyInvalidationChannel = "contextos:apikeys:invalidate"

type apiKeyInvalidationEvent struct {
	Action string `json:"action"`
	KeyID  string `json:"key_id"`
}

// APIKeyManager handles service API key creation, verification, revocation and listing.
type APIKeyManager struct {
	store *pgxpool.Pool
	cache types.CacheStore
	keys  sync.Map // key_hash -> *types.APIKeyRecord
}

// NewAPIKeyManager creates a new APIKeyManager.
func NewAPIKeyManager(store *pgxpool.Pool, cache types.CacheStore) *APIKeyManager {
	return &APIKeyManager{
		store: store,
		cache: cache,
	}
}

// Create generates a new API key with the given name.
// It returns the full key (shown only once) or an error.
func (m *APIKeyManager) Create(ctx context.Context, name string) (string, error) {
	// Generate 32 random bytes -> 64 hex chars, but we only need 32 hex chars
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate random key: %w", err)
	}
	fullKey := "ctx_" + hex.EncodeToString(randomBytes)

	hash := sha256.Sum256([]byte(fullKey))
	keyHash := hex.EncodeToString(hash[:])

	// key_prefix is first 8 chars after "ctx_"
	keyPrefix := fullKey[4:12]

	id, err := generateUUID()
	if err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}

	_, err = m.store.Exec(ctx,
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, enabled, created_at)
		 VALUES ($1, $2, $3, $4, true, $5)`,
		id, name, keyHash, keyPrefix, time.Now().UTC(),
	)
	if err != nil {
		return "", fmt.Errorf("insert api key: %w", err)
	}

	// Cache the new key record.
	record := &types.APIKeyRecord{
		ID:        id,
		Name:      name,
		KeyHash:   keyHash,
		KeyPrefix: keyPrefix,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}
	m.keys.Store(keyHash, record)
	m.publishInvalidation(ctx, apiKeyInvalidationEvent{Action: "create", KeyID: id})

	return fullKey, nil
}

// Verify checks whether the given API key is valid and enabled.
func (m *APIKeyManager) Verify(ctx context.Context, apiKey string) (bool, error) {
	hash := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(hash[:])

	// Check local sync.Map cache first.
	if val, ok := m.keys.Load(keyHash); ok {
		record := val.(*types.APIKeyRecord)
		return record.Enabled, nil
	}

	// Query database.
	var record types.APIKeyRecord
	err := m.store.QueryRow(ctx,
		`SELECT id, name, key_hash, key_prefix, enabled, created_at
		 FROM api_keys WHERE key_hash = $1`, keyHash,
	).Scan(&record.ID, &record.Name, &record.KeyHash, &record.KeyPrefix, &record.Enabled, &record.CreatedAt)
	if err != nil {
		// Not found -> invalid key.
		return false, nil
	}

	// Cache the result.
	m.keys.Store(keyHash, &record)
	return record.Enabled, nil
}

// Revoke disables an API key by its ID.
func (m *APIKeyManager) Revoke(ctx context.Context, keyID string) error {
	_, err := m.store.Exec(ctx,
		`UPDATE api_keys SET enabled = false WHERE id = $1`, keyID,
	)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}

	// Remove matching entries from sync.Map cache.
	m.keys.Range(func(key, value any) bool {
		record := value.(*types.APIKeyRecord)
		if record.ID == keyID {
			m.keys.Delete(key)
			return false // stop iteration
		}
		return true
	})

	m.publishInvalidation(ctx, apiKeyInvalidationEvent{Action: "revoke", KeyID: keyID})

	return nil
}

// List returns all API keys ordered by creation time descending.
func (m *APIKeyManager) List(ctx context.Context) ([]types.APIKeyRecord, error) {
	rows, err := m.store.Query(ctx,
		`SELECT id, name, key_hash, key_prefix, enabled, created_at
		 FROM api_keys ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []types.APIKeyRecord
	for rows.Next() {
		var r types.APIKeyRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.KeyHash, &r.KeyPrefix, &r.Enabled, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, r)
	}
	return keys, rows.Err()
}

// LoadKeys loads all enabled keys from the database into the sync.Map cache.
// This should be called at startup.
func (m *APIKeyManager) LoadKeys(ctx context.Context) error {
	m.keys = sync.Map{}
	rows, err := m.store.Query(ctx,
		`SELECT id, name, key_hash, key_prefix, enabled, created_at
		 FROM api_keys WHERE enabled = true`,
	)
	if err != nil {
		return fmt.Errorf("load api keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r types.APIKeyRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.KeyHash, &r.KeyPrefix, &r.Enabled, &r.CreatedAt); err != nil {
			return fmt.Errorf("scan api key: %w", err)
		}
		m.keys.Store(r.KeyHash, &r)
	}
	return rows.Err()
}

// StartInvalidationListener subscribes to cross-node cache refresh events.
func (m *APIKeyManager) StartInvalidationListener(ctx context.Context) error {
	bus, ok := m.cache.(types.PubSubCache)
	if !ok {
		return nil
	}
	ch, closeFn, err := bus.Subscribe(ctx, apiKeyInvalidationChannel)
	if err != nil {
		return err
	}
	go func() {
		defer closeFn()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				_ = m.handleInvalidation(ctx, msg)
			}
		}
	}()
	return nil
}

func (m *APIKeyManager) publishInvalidation(ctx context.Context, event apiKeyInvalidationEvent) {
	bus, ok := m.cache.(types.PubSubCache)
	if !ok {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_ = bus.Publish(ctx, apiKeyInvalidationChannel, data)
}

func (m *APIKeyManager) handleInvalidation(ctx context.Context, payload []byte) error {
	var event apiKeyInvalidationEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	return m.LoadKeys(ctx)
}

// generateUUID generates a random UUID v4 string.
func generateUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Set version 4 and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
