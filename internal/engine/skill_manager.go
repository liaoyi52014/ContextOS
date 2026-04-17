package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

const skillInvalidationChannel = "contextos:skills:invalidate"

// SkillManager manages skill lifecycle and implements the SkillCatalog interface.
type SkillManager struct {
	store     *pgxpool.Pool
	cache     types.CacheStore
	tools     *ToolRegistry
	embedding types.EmbeddingProvider
	skills    sync.Map // id -> *types.SkillMeta
}

// NewSkillManager creates a new SkillManager.
func NewSkillManager(store *pgxpool.Pool, cache types.CacheStore, tools *ToolRegistry, emb types.EmbeddingProvider) *SkillManager {
	return &SkillManager{
		store:     store,
		cache:     cache,
		tools:     tools,
		embedding: emb,
	}
}

// Add creates a new skill from a SkillDocument, persists it, and registers its tools.
func (sm *SkillManager) Add(ctx context.Context, doc types.SkillDocument) (*types.SkillMeta, error) {
	id, err := generateModelUUID()
	if err != nil {
		return nil, fmt.Errorf("skill_manager: generate id: %w", err)
	}

	toolsJSON, err := json.Marshal(doc.Tools)
	if err != nil {
		return nil, fmt.Errorf("skill_manager: marshal tools: %w", err)
	}

	now := time.Now().UTC()
	meta := &types.SkillMeta{
		ID:          id,
		Name:        doc.Name,
		Description: doc.Description,
		Body:        doc.Body,
		Status:      types.SkillEnabled,
		Tools:       doc.Tools,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Insert the skill row first (without catalog_embedding which is a dynamic vector column).
	_, err = sm.store.Exec(ctx,
		`INSERT INTO skills (id, name, description, body, status, tool_bindings, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		meta.ID, meta.Name, meta.Description, meta.Body, string(meta.Status),
		toolsJSON, meta.CreatedAt, meta.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("skill_manager: insert skill: %w", err)
	}

	// Pre-compute catalog_embedding if embedding provider is available.
	// The catalog_embedding column is a dynamic vector column added by PGVectorStore.Init().
	if sm.embedding != nil {
		text := doc.Name + " " + doc.Description
		vecs, embErr := sm.embedding.Embed(ctx, []string{text})
		if embErr == nil && len(vecs) > 0 {
			vecStr := formatEmbeddingVector(vecs[0])
			// Best-effort update; column may not exist yet.
			_, _ = sm.store.Exec(ctx,
				`UPDATE skills SET catalog_embedding=$1 WHERE id=$2`,
				vecStr, meta.ID,
			)
		}
	}

	// Register skill tools.
	sm.registerSkillTools(meta)

	// Cache in sync.Map.
	sm.skills.Store(meta.ID, meta)
	sm.publishInvalidation(ctx)

	return meta, nil
}

// Remove deletes a skill, unregisters its tools, and removes it from cache.
func (sm *SkillManager) Remove(ctx context.Context, id string) error {
	// Load skill to get tool bindings for unregistration.
	if val, ok := sm.skills.Load(id); ok {
		meta := val.(*types.SkillMeta)
		sm.unregisterSkillTools(meta)
	}

	tag, err := sm.store.Exec(ctx, `DELETE FROM skills WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("skill_manager: delete skill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("skill %s not found", id)}
	}

	sm.skills.Delete(id)
	sm.publishInvalidation(ctx)
	return nil
}

// Enable sets a skill's status to enabled, registers its tools, and updates cache.
func (sm *SkillManager) Enable(ctx context.Context, id string) error {
	tag, err := sm.store.Exec(ctx,
		`UPDATE skills SET status=$1, updated_at=$2 WHERE id=$3`,
		string(types.SkillEnabled), time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("skill_manager: enable skill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("skill %s not found", id)}
	}

	if val, ok := sm.skills.Load(id); ok {
		meta := val.(*types.SkillMeta)
		meta.Status = types.SkillEnabled
		meta.UpdatedAt = time.Now().UTC()
		sm.registerSkillTools(meta)
	}
	sm.publishInvalidation(ctx)
	return nil
}

// Disable sets a skill's status to disabled, unregisters its tools, and updates cache.
func (sm *SkillManager) Disable(ctx context.Context, id string) error {
	tag, err := sm.store.Exec(ctx,
		`UPDATE skills SET status=$1, updated_at=$2 WHERE id=$3`,
		string(types.SkillDisabled), time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("skill_manager: disable skill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("skill %s not found", id)}
	}

	if val, ok := sm.skills.Load(id); ok {
		meta := val.(*types.SkillMeta)
		meta.Status = types.SkillDisabled
		meta.UpdatedAt = time.Now().UTC()
		sm.unregisterSkillTools(meta)
	}
	sm.publishInvalidation(ctx)
	return nil
}

// List returns all skills from the database.
func (sm *SkillManager) List(ctx context.Context) ([]types.SkillMeta, error) {
	rows, err := sm.store.Query(ctx,
		`SELECT id, name, description, body, status, tool_bindings, created_at, updated_at
		 FROM skills ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("skill_manager: list skills: %w", err)
	}
	defer rows.Close()

	var skills []types.SkillMeta
	for rows.Next() {
		var s types.SkillMeta
		var status string
		var toolsJSON []byte
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Body, &status, &toolsJSON, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("skill_manager: scan skill: %w", err)
		}
		s.Status = types.SkillStatus(status)
		if len(toolsJSON) > 0 {
			_ = json.Unmarshal(toolsJSON, &s.Tools)
		}
		if s.Tools == nil {
			s.Tools = []types.SkillToolBinding{}
		}
		skills = append(skills, s)
	}
	return skills, rows.Err()
}

// Info returns a single skill by ID.
func (sm *SkillManager) Info(ctx context.Context, id string) (*types.SkillMeta, error) {
	var s types.SkillMeta
	var status string
	var toolsJSON []byte
	err := sm.store.QueryRow(ctx,
		`SELECT id, name, description, body, status, tool_bindings, created_at, updated_at
		 FROM skills WHERE id=$1`, id,
	).Scan(&s.ID, &s.Name, &s.Description, &s.Body, &status, &toolsJSON, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("skill %s not found", id)}
	}
	s.Status = types.SkillStatus(status)
	if len(toolsJSON) > 0 {
		_ = json.Unmarshal(toolsJSON, &s.Tools)
	}
	if s.Tools == nil {
		s.Tools = []types.SkillToolBinding{}
	}
	return &s, nil
}

// LoadCatalog returns all enabled skills with id, name, description, and status (no body).
// Returns an empty slice (not nil, not error) when no enabled skills exist.
func (sm *SkillManager) LoadCatalog(ctx context.Context) ([]types.SkillMeta, error) {
	rows, err := sm.store.Query(ctx,
		`SELECT id, name, description, status FROM skills WHERE status=$1 ORDER BY name`,
		string(types.SkillEnabled),
	)
	if err != nil {
		return nil, fmt.Errorf("skill_manager: load catalog: %w", err)
	}
	defer rows.Close()

	skills := make([]types.SkillMeta, 0)
	for rows.Next() {
		var s types.SkillMeta
		var status string
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &status); err != nil {
			return nil, fmt.Errorf("skill_manager: scan catalog entry: %w", err)
		}
		s.Status = types.SkillStatus(status)
		skills = append(skills, s)
	}
	return skills, rows.Err()
}

// LoadBody returns the body text of a skill by ID.
func (sm *SkillManager) LoadBody(ctx context.Context, id string) (string, error) {
	var body string
	err := sm.store.QueryRow(ctx, `SELECT body FROM skills WHERE id=$1`, id).Scan(&body)
	if err != nil {
		return "", &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("skill %s not found", id)}
	}
	return body, nil
}

// LoadAll loads all skills into the sync.Map cache and registers enabled skill tools.
func (sm *SkillManager) LoadAll(ctx context.Context) error {
	sm.resetSkillCache()
	skills, err := sm.List(ctx)
	if err != nil {
		return fmt.Errorf("skill_manager: load all: %w", err)
	}
	for i := range skills {
		s := &skills[i]
		sm.skills.Store(s.ID, s)
		if s.Status == types.SkillEnabled {
			sm.registerSkillTools(s)
		}
	}
	return nil
}

// StartInvalidationListener subscribes to remote skill cache refresh events.
func (sm *SkillManager) StartInvalidationListener(ctx context.Context) error {
	bus, ok := sm.cache.(types.PubSubCache)
	if !ok {
		return nil
	}
	ch, closeFn, err := bus.Subscribe(ctx, skillInvalidationChannel)
	if err != nil {
		return err
	}
	go func() {
		defer closeFn()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				_ = sm.LoadAll(ctx)
			}
		}
	}()
	return nil
}

// registerSkillTools registers all tool bindings for a skill in the ToolRegistry.
func (sm *SkillManager) registerSkillTools(meta *types.SkillMeta) {
	if sm.tools == nil {
		return
	}
	for _, tb := range meta.Tools {
		proxy := NewSkillToolProxy(tb)
		// Ignore error if already registered (idempotent).
		_ = sm.tools.Register(proxy)
	}
}

// unregisterSkillTools removes all tool bindings for a skill from the ToolRegistry.
func (sm *SkillManager) unregisterSkillTools(meta *types.SkillMeta) {
	if sm.tools == nil {
		return
	}
	for _, tb := range meta.Tools {
		sm.tools.Unregister(tb.Name)
	}
}

func (sm *SkillManager) resetSkillCache() {
	sm.skills.Range(func(key, value any) bool {
		meta := value.(*types.SkillMeta)
		sm.unregisterSkillTools(meta)
		sm.skills.Delete(key)
		return true
	})
}

func (sm *SkillManager) publishInvalidation(ctx context.Context) {
	bus, ok := sm.cache.(types.PubSubCache)
	if !ok {
		return
	}
	data, _ := json.Marshal(map[string]string{"event": "refresh"})
	_ = bus.Publish(ctx, skillInvalidationChannel, data)
}

// Ensure SkillManager implements SkillCatalog at compile time.
var _ SkillCatalog = (*SkillManager)(nil)

// formatEmbeddingVector converts a float32 slice to pgvector string format: [0.1,0.2,...].
func formatEmbeddingVector(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteByte(']')
	return b.String()
}
