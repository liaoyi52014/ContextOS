package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// handleAssemble parses an AssembleRequest, calls engine.Assemble, and returns the response.
// It also measures duration and logs a warning if it exceeds slow_query_ms.
func (s *Server) handleAssemble(c *gin.Context) {
	var req types.AssembleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "query is required"})
		return
	}
	if req.TokenBudget < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "token_budget must be non-negative"})
		return
	}

	rc := GetRequestContext(c)
	if req.SessionID != "" {
		rc.SessionID = req.SessionID
	}
	s.applySessionAffinity(c, rc.SessionID)

	if s.engine == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "engine not available"})
		return
	}

	start := time.Now()
	resp, err := s.engine.Assemble(c.Request.Context(), rc, req)
	duration := time.Since(start)

	// Slow query warning (Task 17.6)
	slowMs := 300
	if s.config != nil && s.config.Engine.SlowQueryMs > 0 {
		slowMs = s.config.Engine.SlowQueryMs
	}
	if duration.Milliseconds() > int64(slowMs) && s.logger != nil {
		s.logger.Component("api").Warn("slow context assembly",
			zap.String("request_id", GetRequestID(c)),
			zap.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
			zap.Int("threshold_ms", slowMs),
		)
	}

	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// handleIngest parses an IngestRequest, calls engine.Ingest, and returns the response.
func (s *Server) handleIngest(c *gin.Context) {
	var req types.IngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "messages is required"})
		return
	}
	if len(req.Messages) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "messages exceeds maximum of 200"})
		return
	}

	rc := GetRequestContext(c)
	if req.SessionID != "" {
		rc.SessionID = req.SessionID
	}
	s.applySessionAffinity(c, rc.SessionID)

	if s.engine == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "engine not available"})
		return
	}

	resp, err := s.engine.Ingest(c.Request.Context(), rc, req)
	if err != nil {
		handleEngineError(c, err)
		return
	}
	if resp.CompactTriggered {
		s.compactTriggeredCount.Add(1)
	}

	c.JSON(http.StatusOK, resp)
}

// handleMemorySearch parses a MemorySearchRequest and calls engine.SearchMemory.
func (s *Server) handleMemorySearch(c *gin.Context) {
	var req types.MemorySearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "query is required"})
		return
	}
	if req.Limit < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "limit must be non-negative"})
		return
	}

	rc := GetRequestContext(c)
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	if s.engine == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "engine not available"})
		return
	}

	results, err := s.engine.SearchMemory(c.Request.Context(), rc, req.Query, limit)
	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

// handleMemoryStore parses a MemoryStoreRequest and calls engine.StoreMemory.
func (s *Server) handleMemoryStore(c *gin.Context) {
	var req types.MemoryStoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "content is required"})
		return
	}

	rc := GetRequestContext(c)

	if s.engine == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "engine not available"})
		return
	}

	err := s.engine.StoreMemory(c.Request.Context(), rc, req.Content, req.Metadata)
	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "stored"})
}

// handleMemoryDelete calls engine.ForgetMemory for the given memory ID.
func (s *Server) handleMemoryDelete(c *gin.Context) {
	memoryID := c.Param("id")
	if memoryID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "memory id is required"})
		return
	}

	rc := GetRequestContext(c)

	if s.engine == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "engine not available"})
		return
	}

	err := s.engine.ForgetMemory(c.Request.Context(), rc, memoryID)
	if err != nil {
		handleEngineError(c, err)
		return
	}
	s.auditRequest(c, "memory.delete", "memory", memoryID, nil)

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// handleToolExecute parses a ToolExecuteRequest and calls engine.ExecuteTool.
func (s *Server) handleToolExecute(c *gin.Context) {
	var req types.ToolExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.ToolName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "tool_name is required"})
		return
	}

	rc := GetRequestContext(c)
	if req.SessionID != "" {
		rc.SessionID = req.SessionID
	}
	s.applySessionAffinity(c, rc.SessionID)

	if s.engine == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "engine not available"})
		return
	}

	result, err := s.engine.ExecuteTool(c.Request.Context(), rc, req.ToolName, req.Params)
	if err != nil {
		handleEngineError(c, err)
		return
	}
	s.toolExecCount.Add(1)

	c.JSON(http.StatusOK, gin.H{"result": result})
}

// handleGetTask retrieves a task by ID from the TaskTracker.
func (s *Server) handleGetTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "task_id is required"})
		return
	}

	if s.tasks == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "task tracker not available"})
		return
	}

	task, err := s.tasks.Get(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": types.ErrNotFound, "message": "task not found"})
		return
	}

	c.JSON(http.StatusOK, types.TaskGetResponse{Task: task})
}

// handleSessionList lists sessions for the current tenant/user.
func (s *Server) handleSessionList(c *gin.Context) {
	if s.sessionStore == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "session store not available"})
		return
	}

	rc := GetRequestContext(c)
	sessions, err := s.sessionStore.List(c.Request.Context(), rc.TenantID, rc.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list sessions"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// handleSessionDelete deletes a session by ID.
func (s *Server) handleSessionDelete(c *gin.Context) {
	if s.sessionStore == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "session store not available"})
		return
	}

	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "session id is required"})
		return
	}

	rc := GetRequestContext(c)
	if err := s.sessionStore.Delete(c.Request.Context(), rc.TenantID, rc.UserID, sessionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to delete session"})
		return
	}
	if s.cache != nil {
		_ = s.cache.Delete(c.Request.Context(), rc.TenantID+":"+rc.UserID+":"+sessionID)
	}
	s.auditRequest(c, "session.delete", "session", sessionID, nil)

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// handleTempUpload saves an uploaded file to a temp directory and returns a temp_file_id.
func (s *Server) handleTempUpload(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "file is required"})
		return
	}

	// Generate a temp file ID.
	idBytes := make([]byte, 12)
	_, _ = rand.Read(idBytes)
	tempFileID := "tmp_" + hex.EncodeToString(idBytes)

	tmpDir := os.TempDir()
	ctxTmpDir := filepath.Join(tmpDir, "contextos_uploads")
	if err := os.MkdirAll(ctxTmpDir, 0o750); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to create temp directory"})
		return
	}

	ext := filepath.Ext(file.Filename)
	destPath := filepath.Join(ctxTmpDir, tempFileID+ext)
	if err := c.SaveUploadedFile(file, destPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to save uploaded file"})
		return
	}
	if err := saveTempUpload(&tempUploadMetadata{
		ID:        tempFileID,
		Path:      destPath,
		Filename:  file.Filename,
		ExpiresAt: time.Now().Add(tempUploadTTL),
	}); err != nil {
		_ = os.Remove(destPath)
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to persist upload metadata"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"temp_file_id": tempFileID, "filename": file.Filename})
}

// handleEngineError maps engine errors to appropriate HTTP responses.
func handleEngineError(c *gin.Context, err error) {
	if appErr, ok := err.(*types.AppError); ok {
		status := http.StatusInternalServerError
		switch appErr.Code {
		case types.ErrBadRequest:
			status = http.StatusBadRequest
		case types.ErrUnauthorized:
			status = http.StatusUnauthorized
		case types.ErrForbidden:
			status = http.StatusForbidden
		case types.ErrNotFound:
			status = http.StatusNotFound
		case types.ErrConflict:
			status = http.StatusConflict
		case types.ErrServiceUnavailable:
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"code": appErr.Code, "message": appErr.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": fmt.Sprintf("internal error: %v", err)})
}
