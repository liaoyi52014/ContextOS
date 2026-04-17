package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

type recordingWebhookManager struct {
	events []types.WebhookEvent
}

func (r *recordingWebhookManager) Notify(_ context.Context, event types.WebhookEvent) error {
	r.events = append(r.events, event)
	return nil
}
func (r *recordingWebhookManager) Subscribe(context.Context, string, string, []string) (string, error) {
	return "", nil
}
func (r *recordingWebhookManager) Unsubscribe(context.Context, string) error { return nil }
func (r *recordingWebhookManager) List(context.Context, string) ([]types.WebhookSubscription, error) {
	return nil, nil
}

func TestVerifySession_ExpiredSessionNotifiesWebhook(t *testing.T) {
	cache := mock.NewMemoryCacheStore()
	webhooks := &recordingWebhookManager{}
	auth := NewAdminAuth(nil, cache)
	auth.SetWebhookManager(webhooks)

	session := types.AdminSession{
		Token:     "tok",
		UserID:    "admin-1",
		Username:  "root",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if err := cache.Set(context.Background(), adminSessionPrefix+session.Token, data, time.Hour); err != nil {
		t.Fatalf("cache.Set failed: %v", err)
	}

	if _, err := auth.VerifySession(context.Background(), session.Token); err == nil {
		t.Fatal("expected expired session error")
	}
	if len(webhooks.events) != 1 {
		t.Fatalf("expected 1 webhook event, got %d", len(webhooks.events))
	}
	if webhooks.events[0].Type != "session.expired" {
		t.Fatalf("expected session.expired event, got %q", webhooks.events[0].Type)
	}
}
