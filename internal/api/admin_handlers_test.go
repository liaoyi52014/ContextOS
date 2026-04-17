package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/contextos/contextos/internal/mock"
	"github.com/contextos/contextos/internal/types"
	"github.com/gin-gonic/gin"
)

type memoryWebhookManager struct {
	mu   sync.Mutex
	subs []types.WebhookSubscription
	next int
}

func (m *memoryWebhookManager) Notify(context.Context, types.WebhookEvent) error { return nil }
func (m *memoryWebhookManager) Subscribe(_ context.Context, tenantID, url string, events []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	id := "wh-" + string(rune('0'+m.next))
	m.subs = append(m.subs, types.WebhookSubscription{
		ID:       id,
		TenantID: tenantID,
		URL:      url,
		Events:   events,
		Enabled:  true,
	})
	return id, nil
}
func (m *memoryWebhookManager) Unsubscribe(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := m.subs[:0]
	for _, sub := range m.subs {
		if sub.ID != id {
			filtered = append(filtered, sub)
		}
	}
	m.subs = filtered
	return nil
}
func (m *memoryWebhookManager) List(_ context.Context, tenantID string) ([]types.WebhookSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []types.WebhookSubscription
	for _, sub := range m.subs {
		if sub.TenantID == tenantID {
			out = append(out, sub)
		}
	}
	return out, nil
}

type memorySkillAdmin struct {
	mu       sync.Mutex
	added    []types.SkillDocument
	lastMeta *types.SkillMeta
}

func (m *memorySkillAdmin) Add(_ context.Context, doc types.SkillDocument) (*types.SkillMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, doc)
	meta := &types.SkillMeta{
		ID:          "skill-1",
		Name:        doc.Name,
		Description: doc.Description,
		Body:        doc.Body,
		Status:      types.SkillEnabled,
		Tools:       doc.Tools,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	m.lastMeta = meta
	return meta, nil
}
func (m *memorySkillAdmin) List(context.Context) ([]types.SkillMeta, error) { return nil, nil }
func (m *memorySkillAdmin) Info(context.Context, string) (*types.SkillMeta, error) {
	if m.lastMeta == nil {
		return nil, &types.AppError{Code: types.ErrNotFound, Message: "not found"}
	}
	return m.lastMeta, nil
}
func (m *memorySkillAdmin) Enable(context.Context, string) error  { return nil }
func (m *memorySkillAdmin) Disable(context.Context, string) error { return nil }
func (m *memorySkillAdmin) Remove(context.Context, string) error  { return nil }

type memoryTaskTrackerForAPI struct {
	mu    sync.Mutex
	tasks map[string]*types.TaskRecord
}

func newMemoryTaskTrackerForAPI() *memoryTaskTrackerForAPI {
	return &memoryTaskTrackerForAPI{tasks: map[string]*types.TaskRecord{}}
}
func (t *memoryTaskTrackerForAPI) Create(_ context.Context, taskType string, _ map[string]interface{}) (*types.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := "task-1"
	rec := &types.TaskRecord{ID: id, Type: taskType, Status: types.TaskPending, TraceID: id, CreatedAt: time.Now().UTC()}
	t.tasks[id] = rec
	return rec, nil
}
func (t *memoryTaskTrackerForAPI) Start(_ context.Context, taskID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID].Status = types.TaskRunning
	return nil
}
func (t *memoryTaskTrackerForAPI) Complete(_ context.Context, taskID string, result map[string]interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID].Status = types.TaskCompleted
	t.tasks[taskID].ResultSummary = result
	return nil
}
func (t *memoryTaskTrackerForAPI) Fail(_ context.Context, taskID string, err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID].Status = types.TaskFailed
	if err != nil {
		t.tasks[taskID].Error = err.Error()
	}
	return nil
}
func (t *memoryTaskTrackerForAPI) Get(_ context.Context, taskID string) (*types.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tasks[taskID], nil
}
func (t *memoryTaskTrackerForAPI) QueueStats(context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func TestHandleWebhookHandlers_UseWebhookManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := NewServer(ServerDeps{
		Webhooks: &memoryWebhookManager{},
	})

	addReqBody := bytes.NewBufferString(`{"url":"https://example.com/hook","events":["compact.completed"]}`)
	addReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks", addReqBody)
	addReq.Header.Set("Content-Type", "application/json")
	addW := httptest.NewRecorder()
	addCtx, _ := gin.CreateTestContext(addW)
	addCtx.Request = addReq
	server.handleWebhookAdd(addCtx)
	if addW.Code != http.StatusOK {
		t.Fatalf("expected add 200, got %d body=%s", addW.Code, addW.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks", nil)
	listW := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listW)
	listCtx.Request = listReq
	server.handleWebhookList(listCtx)
	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d", listW.Code)
	}
	var listBody struct {
		Webhooks []types.WebhookSubscription `json:"webhooks"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(listBody.Webhooks) != 1 {
		t.Fatalf("expected one webhook, got %d", len(listBody.Webhooks))
	}
}

func TestHandleTempUpload_PersistsMetadataAndCleanup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := NewServer(ServerDeps{
		Cache: mock.NewMemoryCacheStore(),
	})

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "skill.json")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	_, _ = part.Write([]byte(`{"name":"planner","description":"d","body":"b"}`))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads/temp", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = req
	server.handleTempUpload(ctx)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		TempFileID string `json:"temp_file_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	meta, err := loadTempUpload(resp.TempFileID)
	if err != nil {
		t.Fatalf("loadTempUpload failed: %v", err)
	}
	if meta == nil || meta.Path == "" {
		t.Fatal("expected temp upload metadata with path")
	}
	meta.ExpiresAt = time.Now().Add(-time.Minute)
	if err := saveTempUpload(meta); err != nil {
		t.Fatalf("saveTempUpload failed: %v", err)
	}
	if err := cleanupExpiredTempUploads(); err != nil {
		t.Fatalf("cleanupExpiredTempUploads failed: %v", err)
	}
	if _, err := os.Stat(meta.Path); !os.IsNotExist(err) {
		t.Fatalf("expected uploaded file to be removed, stat err=%v", err)
	}
}

func TestHandleSkillAdd_ImportsFromTempFileAsyncTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := filepath.Join(os.TempDir(), "contextos_uploads")
	_ = os.MkdirAll(tmpDir, 0o750)
	filePath := filepath.Join(tmpDir, "tmp_skill.json")
	if err := os.WriteFile(filePath, []byte(`{"name":"planner","description":"desc","body":"body"}`), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := saveTempUpload(&tempUploadMetadata{
		ID:        "tmp_skill",
		Path:      filePath,
		Filename:  "skill.json",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("saveTempUpload failed: %v", err)
	}

	skills := &memorySkillAdmin{}
	tasks := newMemoryTaskTrackerForAPI()
	server := NewServer(ServerDeps{
		Skills: skills,
		Tasks:  tasks,
		Cache:  mock.NewMemoryCacheStore(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills", bytes.NewBufferString(`{"temp_file_id":"tmp_skill","wait":false}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = req
	server.handleSkillAdd(ctx)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tasks.mu.Lock()
		task := tasks.tasks["task-1"]
		tasks.mu.Unlock()
		skills.mu.Lock()
		added := len(skills.added)
		skills.mu.Unlock()
		if task != nil && task.Status == types.TaskCompleted && added == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected async skill import to complete")
}
