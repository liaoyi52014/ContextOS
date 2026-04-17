package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	adminSessionTTL    = 24 * time.Hour
	adminSessionPrefix = "admin_session:"
	bcryptCost         = 10
)

// AdminAuth handles administrator authentication and session management.
type AdminAuth struct {
	store    *pgxpool.Pool
	cache    types.CacheStore
	webhooks types.WebhookManager
}

// NewAdminAuth creates a new AdminAuth.
func NewAdminAuth(store *pgxpool.Pool, cache types.CacheStore) *AdminAuth {
	return &AdminAuth{
		store: store,
		cache: cache,
	}
}

// SetWebhookManager configures optional webhook delivery for auth events.
func (a *AdminAuth) SetWebhookManager(webhooks types.WebhookManager) {
	a.webhooks = webhooks
}

// BootstrapDefaultAdmin creates a default admin if no admin exists.
// This is idempotent: if an admin already exists, it returns nil.
func (a *AdminAuth) BootstrapDefaultAdmin(ctx context.Context, username, password string) error {
	exists, err := a.HasAdmin(ctx)
	if err != nil {
		return fmt.Errorf("check admin existence: %w", err)
	}
	if exists {
		return nil
	}
	return a.CreateAdmin(ctx, username, password)
}

// CreateAdmin creates a new admin user with the given credentials.
func (a *AdminAuth) CreateAdmin(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	id, err := generateUUID()
	if err != nil {
		return fmt.Errorf("generate id: %w", err)
	}

	now := time.Now().UTC()
	_, err = a.store.Exec(ctx,
		`INSERT INTO admin_users (id, username, password_hash, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, true, $4, $5)`,
		id, username, string(hash), now, now,
	)
	if err != nil {
		return &types.AppError{
			Code:    types.ErrConflict,
			Message: fmt.Sprintf("admin user %q already exists", username),
		}
	}
	return nil
}

// Login authenticates an admin and returns a session token.
func (a *AdminAuth) Login(ctx context.Context, username, password string) (*types.AdminSession, error) {
	var user types.AdminUser
	err := a.store.QueryRow(ctx,
		`SELECT id, username, password_hash, created_at, updated_at
		 FROM admin_users WHERE username = $1 AND enabled = true`, username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, &types.AppError{
			Code:    types.ErrUnauthorized,
			Message: "invalid username or password",
		}
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, &types.AppError{
			Code:    types.ErrUnauthorized,
			Message: "invalid username or password",
		}
	}

	// Generate random session token.
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	session := &types.AdminSession{
		Token:     token,
		UserID:    user.ID,
		Username:  user.Username,
		ExpiresAt: time.Now().UTC().Add(adminSessionTTL),
	}

	// Store session in Redis cache.
	data, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}
	if err := a.cache.Set(ctx, adminSessionPrefix+token, data, adminSessionTTL); err != nil {
		return nil, fmt.Errorf("store session in cache: %w", err)
	}

	return session, nil
}

// VerifySession validates an admin session token and returns the session.
func (a *AdminAuth) VerifySession(ctx context.Context, token string) (*types.AdminSession, error) {
	data, err := a.cache.Get(ctx, adminSessionPrefix+token)
	if err != nil {
		return nil, fmt.Errorf("get session from cache: %w", err)
	}
	if data == nil {
		return nil, &types.AppError{
			Code:    types.ErrUnauthorized,
			Message: "invalid or expired session",
		}
	}

	var session types.AdminSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	if time.Now().After(session.ExpiresAt) {
		// Clean up expired session.
		_ = a.cache.Delete(ctx, adminSessionPrefix+token)
		if a.webhooks != nil {
			_ = a.webhooks.Notify(ctx, types.WebhookEvent{
				ID:         token,
				Type:       "session.expired",
				TenantID:   "default",
				UserID:     session.UserID,
				SessionID:  token,
				OccurredAt: time.Now().UTC(),
				Payload: map[string]interface{}{
					"username": session.Username,
				},
			})
		}
		return nil, &types.AppError{
			Code:    types.ErrUnauthorized,
			Message: "session expired",
		}
	}

	return &session, nil
}

// UpdatePassword updates the password for an admin user.
func (a *AdminAuth) UpdatePassword(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tag, err := a.store.Exec(ctx,
		`UPDATE admin_users SET password_hash = $1, updated_at = NOW() WHERE username = $2 OR id = $2`,
		string(hash), username,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{
			Code:    types.ErrNotFound,
			Message: fmt.Sprintf("admin user %q not found", username),
		}
	}
	return nil
}

// HasAdmin checks whether at least one enabled admin user exists.
func (a *AdminAuth) HasAdmin(ctx context.Context) (bool, error) {
	var count int
	err := a.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM admin_users WHERE enabled = true`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("count admin users: %w", err)
	}
	return count > 0, nil
}

// ListAdmins returns all admin users.
func (a *AdminAuth) ListAdmins(ctx context.Context) ([]types.AdminUser, error) {
	rows, err := a.store.Query(ctx,
		`SELECT id, username, enabled, created_at, updated_at
		 FROM admin_users ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list admin users: %w", err)
	}
	defer rows.Close()

	var users []types.AdminUser
	for rows.Next() {
		var user types.AdminUser
		if err := rows.Scan(&user.ID, &user.Username, &user.Enabled, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan admin user: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// DisableAdmin disables an admin user unless it would remove the last enabled admin.
func (a *AdminAuth) DisableAdmin(ctx context.Context, identifier string) error {
	var enabledCount int
	if err := a.store.QueryRow(ctx, `SELECT COUNT(*) FROM admin_users WHERE enabled = true`).Scan(&enabledCount); err != nil {
		return fmt.Errorf("count enabled admins: %w", err)
	}

	var targetEnabled bool
	err := a.store.QueryRow(ctx,
		`SELECT enabled FROM admin_users WHERE username = $1 OR id = $1`,
		identifier,
	).Scan(&targetEnabled)
	if err != nil {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("admin user %q not found", identifier)}
	}
	if targetEnabled && enabledCount <= 1 {
		return &types.AppError{Code: types.ErrConflict, Message: "cannot disable the last enabled admin"}
	}

	tag, err := a.store.Exec(ctx,
		`UPDATE admin_users SET enabled = false, updated_at = NOW() WHERE username = $1 OR id = $1`,
		identifier,
	)
	if err != nil {
		return fmt.Errorf("disable admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("admin user %q not found", identifier)}
	}
	return nil
}
