package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

const modelInvalidationChannel = "contextos:models:invalidate"

// ModelManager manages model providers and model configurations.
type ModelManager struct {
	store     *pgxpool.Pool
	cache     types.CacheStore
	models    sync.Map // model_id -> *types.ModelConfig
	providers sync.Map // provider_id -> *types.ModelProvider
}

// NewModelManager creates a new ModelManager.
func NewModelManager(store *pgxpool.Pool, cache types.CacheStore) *ModelManager {
	return &ModelManager{
		store: store,
		cache: cache,
	}
}

// AddProvider inserts a new model provider into the database.
func (m *ModelManager) AddProvider(ctx context.Context, provider types.ModelProvider) error {
	id, err := generateModelUUID()
	if err != nil {
		return fmt.Errorf("model_manager: generate provider id: %w", err)
	}
	if provider.ID == "" {
		provider.ID = id
	}
	now := time.Now().UTC()
	provider.CreatedAt = now
	provider.UpdatedAt = now

	_, err = m.store.Exec(ctx,
		`INSERT INTO model_providers (id, name, api_base, api_key, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		provider.ID, provider.Name, provider.APIBase, provider.APIKey, provider.Enabled, provider.CreatedAt, provider.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("model_manager: insert provider: %w", err)
	}

	m.providers.Store(provider.ID, &provider)
	m.publishInvalidation(ctx)
	return nil
}

// RemoveProvider deletes a provider if no models reference it.
func (m *ModelManager) RemoveProvider(ctx context.Context, id string) error {
	// Check if any models reference this provider.
	var count int
	err := m.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM models WHERE provider_id = $1`, id,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("model_manager: check provider references: %w", err)
	}
	if count > 0 {
		return &types.AppError{Code: types.ErrConflict, Message: fmt.Sprintf("provider %s is referenced by %d model(s)", id, count)}
	}

	tag, err := m.store.Exec(ctx, `DELETE FROM model_providers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("model_manager: delete provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("provider %s not found", id)}
	}

	m.providers.Delete(id)
	m.publishInvalidation(ctx)
	return nil
}

// ListProviders returns all model providers.
func (m *ModelManager) ListProviders(ctx context.Context) ([]types.ModelProvider, error) {
	rows, err := m.store.Query(ctx,
		`SELECT id, name, api_base, api_key, enabled, created_at, updated_at
		 FROM model_providers ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("model_manager: list providers: %w", err)
	}
	defer rows.Close()

	var providers []types.ModelProvider
	for rows.Next() {
		var p types.ModelProvider
		if err := rows.Scan(&p.ID, &p.Name, &p.APIBase, &p.APIKey, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("model_manager: scan provider: %w", err)
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// UpdateProvider updates an existing model provider.
func (m *ModelManager) UpdateProvider(ctx context.Context, provider types.ModelProvider) error {
	provider.UpdatedAt = time.Now().UTC()
	tag, err := m.store.Exec(ctx,
		`UPDATE model_providers SET name=$1, api_base=$2, api_key=$3, enabled=$4, updated_at=$5
		 WHERE id=$6`,
		provider.Name, provider.APIBase, provider.APIKey, provider.Enabled, provider.UpdatedAt, provider.ID,
	)
	if err != nil {
		return fmt.Errorf("model_manager: update provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("provider %s not found", provider.ID)}
	}

	m.providers.Store(provider.ID, &provider)
	m.publishInvalidation(ctx)
	return nil
}

// AddModel inserts a new model configuration into the database.
func (m *ModelManager) AddModel(ctx context.Context, model types.ModelConfig) error {
	id, err := generateModelUUID()
	if err != nil {
		return fmt.Errorf("model_manager: generate model id: %w", err)
	}
	if model.ID == "" {
		model.ID = id
	}
	now := time.Now().UTC()
	model.CreatedAt = now
	model.UpdatedAt = now

	_, err = m.store.Exec(ctx,
		`INSERT INTO models (id, name, provider_id, model_id, type, dimension, is_default, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		model.ID, model.Name, model.ProviderID, model.ModelID, string(model.Type),
		model.Dimension, model.IsDefault, model.Enabled, model.CreatedAt, model.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("model_manager: insert model: %w", err)
	}

	m.models.Store(model.ID, &model)
	m.publishInvalidation(ctx)
	return nil
}

// EnableModel sets a model's enabled flag to true.
func (m *ModelManager) EnableModel(ctx context.Context, id string) error {
	tag, err := m.store.Exec(ctx,
		`UPDATE models SET enabled=true, updated_at=$1 WHERE id=$2`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("model_manager: enable model: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("model %s not found", id)}
	}

	if val, ok := m.models.Load(id); ok {
		cfg := val.(*types.ModelConfig)
		cfg.Enabled = true
		cfg.UpdatedAt = time.Now().UTC()
	}
	m.publishInvalidation(ctx)
	return nil
}

// DisableModel sets a model's enabled flag to false. Rejects if the model is a default.
func (m *ModelManager) DisableModel(ctx context.Context, id string) error {
	var isDefault bool
	err := m.store.QueryRow(ctx, `SELECT is_default FROM models WHERE id=$1`, id).Scan(&isDefault)
	if err != nil {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("model %s not found", id)}
	}
	if isDefault {
		return &types.AppError{Code: types.ErrConflict, Message: "cannot disable a default model; unset default first"}
	}

	_, err = m.store.Exec(ctx,
		`UPDATE models SET enabled=false, updated_at=$1 WHERE id=$2`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("model_manager: disable model: %w", err)
	}

	if val, ok := m.models.Load(id); ok {
		cfg := val.(*types.ModelConfig)
		cfg.Enabled = false
		cfg.UpdatedAt = time.Now().UTC()
	}
	m.publishInvalidation(ctx)
	return nil
}

// SetDefault unsets the current default of the same type and sets the given model as default.
func (m *ModelManager) SetDefault(ctx context.Context, id string) error {
	// Get the model's type.
	var modelType string
	err := m.store.QueryRow(ctx, `SELECT type FROM models WHERE id=$1`, id).Scan(&modelType)
	if err != nil {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("model %s not found", id)}
	}

	tx, err := m.store.Begin(ctx)
	if err != nil {
		return fmt.Errorf("model_manager: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Unset current default of the same type.
	_, err = tx.Exec(ctx,
		`UPDATE models SET is_default=false, updated_at=$1 WHERE type=$2 AND is_default=true`,
		time.Now().UTC(), modelType,
	)
	if err != nil {
		return fmt.Errorf("model_manager: unset default: %w", err)
	}

	// Set new default.
	_, err = tx.Exec(ctx,
		`UPDATE models SET is_default=true, updated_at=$1 WHERE id=$2`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("model_manager: set default: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("model_manager: commit tx: %w", err)
	}

	// Update sync.Map cache: unset old defaults, set new.
	m.models.Range(func(key, value any) bool {
		cfg := value.(*types.ModelConfig)
		if string(cfg.Type) == modelType && cfg.IsDefault {
			cfg.IsDefault = false
		}
		return true
	})
	if val, ok := m.models.Load(id); ok {
		cfg := val.(*types.ModelConfig)
		cfg.IsDefault = true
	}
	m.publishInvalidation(ctx)

	return nil
}

// ListModels returns all model configurations.
func (m *ModelManager) ListModels(ctx context.Context) ([]types.ModelConfig, error) {
	rows, err := m.store.Query(ctx,
		`SELECT id, name, provider_id, model_id, type, dimension, is_default, enabled, created_at, updated_at
		 FROM models ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("model_manager: list models: %w", err)
	}
	defer rows.Close()

	var models []types.ModelConfig
	for rows.Next() {
		var mc types.ModelConfig
		var mt string
		if err := rows.Scan(&mc.ID, &mc.Name, &mc.ProviderID, &mc.ModelID, &mt,
			&mc.Dimension, &mc.IsDefault, &mc.Enabled, &mc.CreatedAt, &mc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("model_manager: scan model: %w", err)
		}
		mc.Type = types.ModelType(mt)
		models = append(models, mc)
	}
	return models, rows.Err()
}

// GetActiveLLM returns the default enabled LLM model and its provider.
func (m *ModelManager) GetActiveLLM(ctx context.Context) (*types.ModelConfig, *types.ModelProvider, error) {
	return m.getActiveModel(ctx, types.ModelTypeLLM)
}

// GetActiveEmbedding returns the default enabled embedding model and its provider.
func (m *ModelManager) GetActiveEmbedding(ctx context.Context) (*types.ModelConfig, *types.ModelProvider, error) {
	return m.getActiveModel(ctx, types.ModelTypeEmbedding)
}

func (m *ModelManager) getActiveModel(ctx context.Context, mt types.ModelType) (*types.ModelConfig, *types.ModelProvider, error) {
	var mc types.ModelConfig
	var mtStr string
	err := m.store.QueryRow(ctx,
		`SELECT id, name, provider_id, model_id, type, dimension, is_default, enabled, created_at, updated_at
		 FROM models WHERE type=$1 AND is_default=true AND enabled=true`, string(mt),
	).Scan(&mc.ID, &mc.Name, &mc.ProviderID, &mc.ModelID, &mtStr,
		&mc.Dimension, &mc.IsDefault, &mc.Enabled, &mc.CreatedAt, &mc.UpdatedAt)
	if err != nil {
		return nil, nil, &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("no active %s model configured", mt)}
	}
	mc.Type = types.ModelType(mtStr)

	var mp types.ModelProvider
	err = m.store.QueryRow(ctx,
		`SELECT id, name, api_base, api_key, enabled, created_at, updated_at
		 FROM model_providers WHERE id=$1`, mc.ProviderID,
	).Scan(&mp.ID, &mp.Name, &mp.APIBase, &mp.APIKey, &mp.Enabled, &mp.CreatedAt, &mp.UpdatedAt)
	if err != nil {
		return nil, nil, &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("provider %s not found for model %s", mc.ProviderID, mc.ID)}
	}

	return &mc, &mp, nil
}

// LoadAll loads all models and providers from the database into the sync.Map caches.
func (m *ModelManager) LoadAll(ctx context.Context) error {
	m.models = sync.Map{}
	m.providers = sync.Map{}
	// Load providers.
	providers, err := m.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("model_manager: load providers: %w", err)
	}
	for i := range providers {
		m.providers.Store(providers[i].ID, &providers[i])
	}

	// Load models.
	models, err := m.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("model_manager: load models: %w", err)
	}
	for i := range models {
		m.models.Store(models[i].ID, &models[i])
	}

	return nil
}

// StartInvalidationListener subscribes to remote model cache refresh events.
func (m *ModelManager) StartInvalidationListener(ctx context.Context) error {
	bus, ok := m.cache.(types.PubSubCache)
	if !ok {
		return nil
	}
	ch, closeFn, err := bus.Subscribe(ctx, modelInvalidationChannel)
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
				_ = m.LoadAll(ctx)
			}
		}
	}()
	return nil
}

func (m *ModelManager) publishInvalidation(ctx context.Context) {
	bus, ok := m.cache.(types.PubSubCache)
	if !ok {
		return
	}
	data, _ := json.Marshal(map[string]string{"event": "refresh"})
	_ = bus.Publish(ctx, modelInvalidationChannel, data)
}

// generateModelUUID generates a random UUID v4 string using crypto/rand.
func generateModelUUID() (string, error) {
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
