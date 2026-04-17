package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/contextos/contextos/internal/log"
	"github.com/contextos/contextos/internal/types"
)

// CoreEngineDeps groups dependencies for CoreEngine.
type CoreEngineDeps struct {
	Sessions   *SessionManager
	Builder    *ContextBuilder
	Retrieval  *RetrievalEngine
	Vector     types.VectorStore
	Embedding  types.EmbeddingProvider
	Compact    *CompactProcessor
	Tools      *ToolRegistry
	Hooks      *HookManager
	Tasks      types.TaskTracker
	TokenAudit *log.TokenAuditor
}

// CoreEngine implements the main ContextOS engine contract.
type CoreEngine struct {
	sessions   *SessionManager
	builder    *ContextBuilder
	retrieval  *RetrievalEngine
	vector     types.VectorStore
	embedding  types.EmbeddingProvider
	compact    *CompactProcessor
	tools      *ToolRegistry
	hooks      *HookManager
	tasks      types.TaskTracker
	tokenAudit *log.TokenAuditor
}

// NewCoreEngine constructs a CoreEngine from its dependencies.
func NewCoreEngine(deps CoreEngineDeps) *CoreEngine {
	return &CoreEngine{
		sessions:   deps.Sessions,
		builder:    deps.Builder,
		retrieval:  deps.Retrieval,
		vector:     deps.Vector,
		embedding:  deps.Embedding,
		compact:    deps.Compact,
		tools:      deps.Tools,
		hooks:      deps.Hooks,
		tasks:      deps.Tasks,
		tokenAudit: deps.TokenAudit,
	}
}

var _ types.Engine = (*CoreEngine)(nil)

func (e *CoreEngine) Assemble(ctx context.Context, rc types.RequestContext, req types.AssembleRequest) (*types.AssembleResponse, error) {
	start := time.Now()
	tc := newTelemetryCollector("assemble", req.Telemetry)
	if e.builder == nil {
		err := &types.AppError{Code: types.ErrInternal, Message: "context builder not configured"}
		recordTelemetryError(tc, "builder", err)
		return nil, err
	}
	if e.hooks != nil {
		_ = e.hooks.Trigger(ctx, types.HookContext{
			Event:     types.HookBeforePrompt,
			TenantID:  rc.TenantID,
			UserID:    rc.UserID,
			SessionID: rc.SessionID,
			Data:      map[string]interface{}{"query": req.Query},
		})
	}
	resp, err := e.builder.Assemble(ctx, rc, req)
	if err != nil {
		recordTelemetryError(tc, "assemble", err)
		return nil, err
	}
	attachTelemetry(&resp.Telemetry, tc, start)
	return resp, nil
}

func (e *CoreEngine) Ingest(ctx context.Context, rc types.RequestContext, req types.IngestRequest) (*types.IngestResponse, error) {
	start := time.Now()
	tc := newTelemetryCollector("ingest", req.Telemetry)
	if e.sessions == nil {
		err := &types.AppError{Code: types.ErrInternal, Message: "session manager not configured"}
		recordTelemetryError(tc, "sessions", err)
		return nil, err
	}

	session, err := e.sessions.GetOrCreate(ctx, rc)
	if err != nil {
		recordTelemetryError(tc, "sessions", err)
		return nil, err
	}

	for _, msg := range req.Messages {
		if err := e.sessions.AddMessage(ctx, session, msg); err != nil {
			recordTelemetryError(tc, "sessions", err)
			return nil, err
		}
	}

	usageRecords := buildUsageRecords(req)
	if len(usageRecords) > 0 {
		session.Metadata["commit_count"] = toInt(session.Metadata["commit_count"]) + 1
		if err := e.sessions.RecordUsage(ctx, session, usageRecords); err != nil {
			recordTelemetryError(tc, "sessions", err)
			return nil, err
		}
	}

	compactTriggered := false
	compactTaskID := ""
	if e.compact != nil {
		compactTriggered, compactTaskID, err = e.compact.EvaluateAndTrigger(ctx, rc, session)
		if err != nil {
			recordTelemetryError(tc, "compact", err)
			return nil, err
		}
		if tc != nil {
			tc.RecordCompact(compactTriggered, compactTaskID)
		}
	}

	if e.hooks != nil {
		_ = e.hooks.Trigger(ctx, types.HookContext{
			Event:     types.HookAfterTurn,
			TenantID:  rc.TenantID,
			UserID:    rc.UserID,
			SessionID: session.ID,
			Data: map[string]interface{}{
				"message_count":     len(req.Messages),
				"compact_triggered": compactTriggered,
				"compact_task_id":   compactTaskID,
			},
		})
	}

	resp := &types.IngestResponse{
		Written:          len(req.Messages),
		CompactTriggered: compactTriggered,
		CompactTaskID:    compactTaskID,
	}
	attachTelemetry(&resp.Telemetry, tc, start)
	return resp, nil
}

func (e *CoreEngine) SearchMemory(ctx context.Context, rc types.RequestContext, query string, limit int) ([]types.SearchResult, error) {
	start := time.Now()
	tc := newTelemetryCollector("memory.search", &types.TelemetryOption{Summary: true})
	if e.embedding == nil || e.vector == nil {
		return nil, nil
	}
	vecs, err := e.embedding.Embed(ctx, []string{query})
	if err != nil {
		recordTelemetryError(tc, "embedding", err)
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	results, err := e.vector.Search(ctx, types.SearchQuery{
		Vector: vecs[0],
		TopK:   limit,
		Filter: &types.Filter{
			TenantID: rc.TenantID,
			UserID:   rc.UserID,
		},
		Threshold: 0,
	})
	if err != nil {
		recordTelemetryError(tc, "vector", err)
		return nil, err
	}
	if tc != nil {
		tc.RecordVector(1, len(results))
		tc.SetDuration(time.Since(start))
	}
	if e.tokenAudit != nil {
		tokens := estimateTokens(query)
		_ = e.tokenAudit.Record(ctx, rc.TenantID, rc.UserID, "embedding.search", "", tokens, 0, tokens, rc.SessionID, "")
	}
	return results, nil
}

func (e *CoreEngine) StoreMemory(ctx context.Context, rc types.RequestContext, content string, metadata map[string]string) error {
	start := time.Now()
	tc := log.NewTelemetryCollector("memory.store")
	if e.embedding == nil || e.vector == nil {
		err := &types.AppError{Code: types.ErrInternal, Message: "memory storage not configured"}
		recordTelemetryError(tc, "vector", err)
		return err
	}
	vecs, err := e.embedding.Embed(ctx, []string{content})
	if err != nil {
		recordTelemetryError(tc, "embedding", err)
		return err
	}
	if len(vecs) == 0 {
		err := &types.AppError{Code: types.ErrInternal, Message: "embedding provider returned no vectors"}
		recordTelemetryError(tc, "embedding", err)
		return err
	}

	id, err := randomEngineID("mem")
	if err != nil {
		recordTelemetryError(tc, "memory", err)
		return err
	}
	err = e.vector.Upsert(ctx, []types.VectorItem{
		{
			ID:       id,
			Vector:   vecs[0],
			Content:  content,
			URI:      fmt.Sprintf("memory://%s/%s/%s", rc.TenantID, rc.UserID, id),
			TenantID: rc.TenantID,
			UserID:   rc.UserID,
			Metadata: metadata,
		},
	})
	if err != nil {
		recordTelemetryError(tc, "vector", err)
		return err
	}
	if e.tokenAudit != nil {
		tokens := estimateTokens(content)
		_ = e.tokenAudit.Record(ctx, rc.TenantID, rc.UserID, "embedding.store", "", tokens, 0, tokens, rc.SessionID, "")
	}
	tc.SetDuration(time.Since(start))
	return nil
}

func (e *CoreEngine) ForgetMemory(ctx context.Context, _ types.RequestContext, memoryID string) error {
	if e.vector == nil {
		return &types.AppError{Code: types.ErrInternal, Message: "vector store not configured"}
	}
	return e.vector.Delete(ctx, []string{memoryID})
}

func (e *CoreEngine) GetSessionSummary(ctx context.Context, rc types.RequestContext) (string, error) {
	if e.sessions == nil {
		return "", &types.AppError{Code: types.ErrInternal, Message: "session manager not configured"}
	}
	session, err := e.sessions.GetOrCreate(ctx, rc)
	if err != nil {
		return "", err
	}
	if len(session.Messages) == 0 {
		return "", nil
	}

	var parts []string
	start := 0
	if len(session.Messages) > 5 {
		start = len(session.Messages) - 5
	}
	for _, msg := range session.Messages[start:] {
		parts = append(parts, fmt.Sprintf("[%s] %s", msg.Role, msg.Content))
	}
	return strings.Join(parts, "\n"), nil
}

func (e *CoreEngine) ExecuteTool(ctx context.Context, rc types.RequestContext, toolName string, params map[string]interface{}) (string, error) {
	start := time.Now()
	tc := log.NewTelemetryCollector("tools.execute")
	if e.tools == nil {
		err := &types.AppError{Code: types.ErrInternal, Message: "tool registry not configured"}
		recordTelemetryError(tc, "tools", err)
		return "", err
	}
	result, err := e.tools.Execute(ctx, rc, toolName, params)
	if err != nil {
		recordTelemetryError(tc, "tools", err)
	}
	if e.hooks != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		_ = e.hooks.Trigger(ctx, types.HookContext{
			Event:     types.HookToolPostCall,
			TenantID:  rc.TenantID,
			UserID:    rc.UserID,
			SessionID: rc.SessionID,
			Data: map[string]interface{}{
				"tool_name": toolName,
				"params":    params,
				"result":    result,
				"status":    status,
			},
		})
	}
	tc.SetDuration(time.Since(start))
	return result, err
}

func buildUsageRecords(req types.IngestRequest) []types.UsageRecord {
	records := make([]types.UsageRecord, 0, len(req.UsedContexts)+len(req.UsedSkills)+len(req.ToolCalls))
	now := time.Now().UTC()
	for _, uri := range req.UsedContexts {
		records = append(records, types.UsageRecord{URI: uri, Success: true, Timestamp: now})
	}
	for _, skill := range req.UsedSkills {
		records = append(records, types.UsageRecord{SkillName: skill, Success: true, Timestamp: now})
	}
	for _, call := range req.ToolCalls {
		record := call
		if record.Timestamp.IsZero() {
			record.Timestamp = now
		}
		records = append(records, record)
	}
	return records
}

func randomEngineID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func newTelemetryCollector(operation string, option *types.TelemetryOption) *log.TelemetryCollector {
	if option == nil {
		return nil
	}
	return log.NewTelemetryCollector(operation)
}

func attachTelemetry(target **types.OperationTelemetry, tc *log.TelemetryCollector, start time.Time) {
	if tc == nil {
		return
	}
	tc.SetDuration(time.Since(start))
	*target = tc.Build()
}

func recordTelemetryError(tc *log.TelemetryCollector, component string, err error) {
	if tc == nil || err == nil {
		return
	}
	tc.RecordError(component, err.Error())
}
