package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookNotifier delivers webhook events to registered subscribers.
type WebhookNotifier struct {
	db     *pgxpool.Pool
	client *http.Client
}

// NewWebhookNotifier creates a new WebhookNotifier.
func NewWebhookNotifier(db *pgxpool.Pool) *WebhookNotifier {
	return &WebhookNotifier{
		db: db,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

var _ types.WebhookManager = (*WebhookNotifier)(nil)

// Notify delivers a webhook event to all matching subscribers.
// It queries webhook_subscriptions for entries matching the event type,
// then POSTs the event payload to each subscriber URL with retries.
func (n *WebhookNotifier) Notify(ctx context.Context, event types.WebhookEvent) error {
	rows, err := n.db.Query(ctx,
		`SELECT id, tenant_id, url, events FROM webhook_subscriptions
		 WHERE enabled = true AND tenant_id = $1`,
		event.TenantID,
	)
	if err != nil {
		return fmt.Errorf("webhook: query subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []types.WebhookSubscription
	for rows.Next() {
		var sub types.WebhookSubscription
		var eventsJSON []byte
		if err := rows.Scan(&sub.ID, &sub.TenantID, &sub.URL, &eventsJSON); err != nil {
			return fmt.Errorf("webhook: scan subscription: %w", err)
		}
		if err := json.Unmarshal(eventsJSON, &sub.Events); err != nil {
			continue
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("webhook: iterate subscriptions: %w", err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("webhook: marshal event: %w", err)
	}

	var lastErr error
	for _, sub := range subs {
		if !matchesEvent(sub.Events, event.Type) {
			continue
		}
		deliveryID := generateID()
		if err := n.deliver(ctx, sub.URL, event.Type, deliveryID, payload); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// deliver POSTs the event payload to the given URL with up to 3 retries.
func (n *WebhookNotifier) deliver(ctx context.Context, url, eventType, deliveryID string, payload []byte) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("webhook: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-ContextOS-Event", eventType)
		req.Header.Set("X-ContextOS-Delivery-ID", deliveryID)

		resp, err := n.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("webhook: POST %s (attempt %d): %w", url, attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("webhook: POST %s returned status %d (attempt %d)", url, resp.StatusCode, attempt+1)
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return lastErr
}

// Subscribe registers a new webhook subscription for the given tenant.
func (n *WebhookNotifier) Subscribe(ctx context.Context, tenantID, url string, events []string) (string, error) {
	id := generateID()
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return "", fmt.Errorf("webhook: marshal events: %w", err)
	}

	_, err = n.db.Exec(ctx,
		`INSERT INTO webhook_subscriptions (id, tenant_id, url, events, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, true, NOW(), NOW())`,
		id, tenantID, url, eventsJSON,
	)
	if err != nil {
		return "", fmt.Errorf("webhook: insert subscription: %w", err)
	}
	return id, nil
}

// Unsubscribe removes a webhook subscription by ID.
func (n *WebhookNotifier) Unsubscribe(ctx context.Context, id string) error {
	_, err := n.db.Exec(ctx,
		`DELETE FROM webhook_subscriptions WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("webhook: delete subscription: %w", err)
	}
	return nil
}

// List returns all webhook subscriptions for the given tenant.
func (n *WebhookNotifier) List(ctx context.Context, tenantID string) ([]types.WebhookSubscription, error) {
	rows, err := n.db.Query(ctx,
		`SELECT id, tenant_id, url, events, enabled, created_at, updated_at
		 FROM webhook_subscriptions WHERE tenant_id = $1
		 ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("webhook: query subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []types.WebhookSubscription
	for rows.Next() {
		var sub types.WebhookSubscription
		var eventsJSON []byte
		if err := rows.Scan(&sub.ID, &sub.TenantID, &sub.URL, &eventsJSON, &sub.Enabled, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, fmt.Errorf("webhook: scan subscription: %w", err)
		}
		if err := json.Unmarshal(eventsJSON, &sub.Events); err != nil {
			sub.Events = nil
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("webhook: iterate subscriptions: %w", err)
	}
	return subs, nil
}

// matchesEvent checks if the subscription's event list includes the given event type.
func matchesEvent(subscribed []string, eventType string) bool {
	for _, e := range subscribed {
		if e == eventType || e == "*" {
			return true
		}
	}
	return false
}

// generateID creates a random hex ID for webhook subscriptions and delivery IDs.
func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
