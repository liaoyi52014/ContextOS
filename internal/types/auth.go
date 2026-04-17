package types

import "time"

// AdminUser represents a system administrator account.
type AdminUser struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Enabled      bool      `json:"enabled"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AdminSession represents an active admin login session.
type AdminSession struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

// APIKeyRecord represents a service API key stored in the system.
type APIKeyRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	KeyHash   string    `json:"-"`
	KeyPrefix string    `json:"key_prefix"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}
