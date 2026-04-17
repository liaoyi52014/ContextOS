package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check that PGProfileStore implements types.ProfileStore.
var _ types.ProfileStore = (*PGProfileStore)(nil)

// PGProfileStore implements types.ProfileStore backed by PostgreSQL.
type PGProfileStore struct {
	db *pgxpool.Pool
}

// NewPGProfileStore creates a new PGProfileStore.
func NewPGProfileStore(db *pgxpool.Pool) *PGProfileStore {
	return &PGProfileStore{db: db}
}

// Load retrieves a user profile by (tenant_id, user_id).
// Returns nil, nil if no profile is found.
func (s *PGProfileStore) Load(ctx context.Context, tenantID, userID string) (*types.UserProfile, error) {
	p := &types.UserProfile{}
	var interestsRaw, prefsRaw, goalsRaw, constraintsRaw, metadataRaw []byte

	err := s.db.QueryRow(ctx,
		`SELECT tenant_id, user_id, summary, interests, preferences, goals, constraints, metadata, source_session_id, updated_at
		 FROM user_profiles
		 WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID,
	).Scan(
		&p.TenantID, &p.UserID, &p.Summary,
		&interestsRaw, &prefsRaw, &goalsRaw, &constraintsRaw,
		&metadataRaw, &p.SourceSessionID, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(interestsRaw, &p.Interests); err != nil {
		return nil, fmt.Errorf("unmarshal interests: %w", err)
	}
	if err := json.Unmarshal(prefsRaw, &p.Preferences); err != nil {
		return nil, fmt.Errorf("unmarshal preferences: %w", err)
	}
	if err := json.Unmarshal(goalsRaw, &p.Goals); err != nil {
		return nil, fmt.Errorf("unmarshal goals: %w", err)
	}
	if err := json.Unmarshal(constraintsRaw, &p.Constraints); err != nil {
		return nil, fmt.Errorf("unmarshal constraints: %w", err)
	}
	if err := json.Unmarshal(metadataRaw, &p.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}

	return p, nil
}

// Upsert inserts or updates a user profile by (tenant_id, user_id).
func (s *PGProfileStore) Upsert(ctx context.Context, profile *types.UserProfile) error {
	interestsJSON, err := json.Marshal(profile.Interests)
	if err != nil {
		return fmt.Errorf("marshal interests: %w", err)
	}
	prefsJSON, err := json.Marshal(profile.Preferences)
	if err != nil {
		return fmt.Errorf("marshal preferences: %w", err)
	}
	goalsJSON, err := json.Marshal(profile.Goals)
	if err != nil {
		return fmt.Errorf("marshal goals: %w", err)
	}
	constraintsJSON, err := json.Marshal(profile.Constraints)
	if err != nil {
		return fmt.Errorf("marshal constraints: %w", err)
	}
	metadataJSON, err := json.Marshal(profile.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO user_profiles (tenant_id, user_id, summary, interests, preferences, goals, constraints, metadata, source_session_id, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		 ON CONFLICT (tenant_id, user_id) DO UPDATE
		 SET summary = EXCLUDED.summary,
		     interests = EXCLUDED.interests,
		     preferences = EXCLUDED.preferences,
		     goals = EXCLUDED.goals,
		     constraints = EXCLUDED.constraints,
		     metadata = EXCLUDED.metadata,
		     source_session_id = EXCLUDED.source_session_id,
		     updated_at = NOW()`,
		profile.TenantID, profile.UserID, profile.Summary,
		interestsJSON, prefsJSON, goalsJSON, constraintsJSON,
		metadataJSON, profile.SourceSessionID,
	)
	return err
}

// Search searches user profile summaries using ILIKE for the given tenant+user.
// Returns matching profiles as ContentBlock with Source="profile", Level=ContentL0.
func (s *PGProfileStore) Search(ctx context.Context, tenantID, userID, query string, limit int) ([]types.ContentBlock, error) {
	if limit <= 0 {
		limit = 10
	}
	pattern := "%" + query + "%"

	rows, err := s.db.Query(ctx,
		`SELECT tenant_id, user_id, summary
		 FROM user_profiles
		 WHERE tenant_id = $1 AND user_id = $2 AND summary ILIKE $3
		 LIMIT $4`,
		tenantID, userID, pattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.ContentBlock
	for rows.Next() {
		var tid, uid, summary string
		if err := rows.Scan(&tid, &uid, &summary); err != nil {
			return nil, err
		}
		results = append(results, types.ContentBlock{
			URI:     fmt.Sprintf("profile://%s/%s", tid, uid),
			Level:   types.ContentL0,
			Content: summary,
			Source:  "profile",
		})
	}
	return results, rows.Err()
}
