package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/config"
	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
)

type stubEngine struct{}

func (stubEngine) Assemble(context.Context, types.RequestContext, types.AssembleRequest) (*types.AssembleResponse, error) {
	return &types.AssembleResponse{}, nil
}
func (stubEngine) Ingest(context.Context, types.RequestContext, types.IngestRequest) (*types.IngestResponse, error) {
	return &types.IngestResponse{}, nil
}
func (stubEngine) SearchMemory(context.Context, types.RequestContext, string, int) ([]types.SearchResult, error) {
	return nil, nil
}
func (stubEngine) StoreMemory(context.Context, types.RequestContext, string, map[string]string) error {
	return nil
}
func (stubEngine) ForgetMemory(context.Context, types.RequestContext, string) error {
	return nil
}
func (stubEngine) GetSessionSummary(context.Context, types.RequestContext) (string, error) {
	return "", nil
}
func (stubEngine) ExecuteTool(context.Context, types.RequestContext, string, map[string]interface{}) (string, error) {
	return "", nil
}

func TestServerReadyz_UsesDependencyChecks(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{DevelopmentMode: true}}
	server := NewServer(ServerDeps{
		Engine: stubEngine{},
		Config: cfg,
	})
	server.cache = mock.NewMemoryCacheStore()
	server.readyCheck = func(context.Context) error { return nil }

	router := server.SetupRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestServerSessionListAndDelete(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{DevelopmentMode: true}}
	sessionStore := mock.NewMemorySessionStore()
	now := time.Now()
	if err := sessionStore.Save(context.Background(), &types.Session{
		ID:        "s1",
		TenantID:  "default",
		UserID:    "default",
		Messages:  []types.Message{{Role: "user", Content: "hello"}},
		Metadata:  map[string]interface{}{},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("preload session failed: %v", err)
	}

	server := NewServer(ServerDeps{
		Engine:       stubEngine{},
		Config:       cfg,
		SessionStore: sessionStore,
		Cache:        mock.NewMemoryCacheStore(),
	})
	router := server.SetupRouter()

	listW := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	router.ServeHTTP(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d", listW.Code)
	}

	var listBody struct {
		Sessions []*types.SessionMeta `json:"sessions"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listBody.Sessions) != 1 || listBody.Sessions[0].ID != "s1" {
		t.Fatalf("expected session list to include s1, got %+v", listBody.Sessions)
	}

	delW := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/s1", nil)
	router.ServeHTTP(delW, delReq)

	if delW.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d", delW.Code)
	}

	stored, err := sessionStore.Load(context.Background(), "default", "default", "s1")
	if err != nil {
		t.Fatalf("load session after delete: %v", err)
	}
	if stored != nil {
		t.Fatal("expected session to be deleted")
	}
}
