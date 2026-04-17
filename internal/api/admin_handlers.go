package api

import (
	"net/http"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/gin-gonic/gin"
)

// --- Auth endpoints (no admin auth required) ---

// handleAuthSetup creates the initial admin user. Returns 409 if an admin already exists.
func (s *Server) handleAuthSetup(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "username and password are required"})
		return
	}

	hasAdmin, err := s.adminAuth.HasAdmin(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to check admin status"})
		return
	}
	if hasAdmin {
		c.JSON(http.StatusConflict, gin.H{"code": types.ErrConflict, "message": "admin already exists"})
		return
	}

	if err := s.adminAuth.CreateAdmin(c.Request.Context(), req.Username, req.Password); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "admin created"})
}

// handleAuthLogin authenticates an admin and returns a session token.
func (s *Server) handleAuthLogin(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "username and password are required"})
		return
	}

	session, err := s.adminAuth.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, session)
}

// handleAuthVerify verifies the admin session token and returns AuthVerifyResponse.
func (s *Server) handleAuthVerify(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "token is required"})
		return
	}

	session, err := s.adminAuth.VerifySession(c.Request.Context(), req.Token)
	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, types.AuthVerifyResponse{
		UserID:    session.UserID,
		Username:  session.Username,
		ExpiresAt: session.ExpiresAt,
	})
}

// --- Admin user management ---

// handleAdminUserList lists all admin users.
func (s *Server) handleAdminUserList(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	users, err := s.adminAuth.ListAdmins(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list admin users"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"users": users})
}

// handleAdminUserCreate creates a new admin user.
func (s *Server) handleAdminUserCreate(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "username and password are required"})
		return
	}

	if err := s.adminAuth.CreateAdmin(c.Request.Context(), req.Username, req.Password); err != nil {
		handleEngineError(c, err)
		return
	}
	s.auditAdmin(c, "admin.create", "admin_user", req.Username, nil)

	c.JSON(http.StatusOK, gin.H{"status": "user created"})
}

// handleAdminUserUpdatePassword updates an admin user's password.
func (s *Server) handleAdminUserUpdatePassword(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	userID := c.Param("id")
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "password is required"})
		return
	}

	// AdminAuth.UpdatePassword takes username, but the route uses :id.
	// For v1, we pass the ID as the username identifier.
	if err := s.adminAuth.UpdatePassword(c.Request.Context(), userID, req.Password); err != nil {
		handleEngineError(c, err)
		return
	}
	s.auditAdmin(c, "admin.update_password", "admin_user", userID, nil)

	c.JSON(http.StatusOK, gin.H{"status": "password updated"})
}

// handleAdminUserDisable disables an admin user.
func (s *Server) handleAdminUserDisable(c *gin.Context) {
	if s.adminAuth == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "admin auth not available"})
		return
	}

	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "user id is required"})
		return
	}

	if err := s.adminAuth.DisableAdmin(c.Request.Context(), userID); err != nil {
		handleEngineError(c, err)
		return
	}
	s.auditAdmin(c, "admin.disable", "admin_user", userID, nil)

	c.JSON(http.StatusOK, gin.H{"status": "user disabled"})
}

// --- API Key management ---

// handleAPIKeyList lists all API keys.
func (s *Server) handleAPIKeyList(c *gin.Context) {
	if s.keyMgr == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "api key manager not available"})
		return
	}

	keys, err := s.keyMgr.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list api keys"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

// handleAPIKeyCreate creates a new API key.
func (s *Server) handleAPIKeyCreate(c *gin.Context) {
	if s.keyMgr == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "api key manager not available"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "name is required"})
		return
	}

	fullKey, err := s.keyMgr.Create(c.Request.Context(), req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to create api key"})
		return
	}
	s.auditAdmin(c, "apikey.create", "api_key", req.Name, nil)

	c.JSON(http.StatusOK, gin.H{"key": fullKey, "name": req.Name})
}

// handleAPIKeyRevoke revokes an API key by ID.
func (s *Server) handleAPIKeyRevoke(c *gin.Context) {
	if s.keyMgr == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "api key manager not available"})
		return
	}

	keyID := c.Param("id")
	if keyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "key id is required"})
		return
	}

	if err := s.keyMgr.Revoke(c.Request.Context(), keyID); err != nil {
		handleEngineError(c, err)
		return
	}
	s.auditAdmin(c, "apikey.revoke", "api_key", keyID, nil)

	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}

// --- Model management ---

// handleModelList lists all model configurations.
func (s *Server) handleModelList(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	models, err := s.models.ListModels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list models"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"models": models})
}

// handleModelAdd adds a new model configuration.
func (s *Server) handleModelAdd(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	var model types.ModelConfig
	if err := c.ShouldBindJSON(&model); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if model.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "name is required"})
		return
	}

	if err := s.models.AddModel(c.Request.Context(), model); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "model added"})
}

// handleModelEnable enables a model by ID.
func (s *Server) handleModelEnable(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.models.EnableModel(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "model enabled"})
}

// handleModelDisable disables a model by ID.
func (s *Server) handleModelDisable(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.models.DisableModel(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "model disabled"})
}

// handleModelSetDefault sets a model as the default for its type.
func (s *Server) handleModelSetDefault(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.models.SetDefault(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "model set as default"})
}

// --- Provider management ---

// handleProviderList lists all model providers.
func (s *Server) handleProviderList(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	providers, err := s.models.ListProviders(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list providers"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

// handleProviderAdd adds a new model provider.
func (s *Server) handleProviderAdd(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	var provider types.ModelProvider
	if err := c.ShouldBindJSON(&provider); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if provider.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "name is required"})
		return
	}

	if err := s.models.AddProvider(c.Request.Context(), provider); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "provider added"})
}

// handleProviderUpdate updates an existing model provider.
func (s *Server) handleProviderUpdate(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	id := c.Param("id")
	var provider types.ModelProvider
	if err := c.ShouldBindJSON(&provider); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	provider.ID = id

	if err := s.models.UpdateProvider(c.Request.Context(), provider); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "provider updated"})
}

// handleProviderRemove removes a model provider by ID.
func (s *Server) handleProviderRemove(c *gin.Context) {
	if s.models == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "model manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.models.RemoveProvider(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "provider removed"})
}

// --- Skill management ---

// handleSkillList lists all skills.
func (s *Server) handleSkillList(c *gin.Context) {
	if s.skills == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "skill manager not available"})
		return
	}

	skills, err := s.skills.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list skills"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"skills": skills})
}

// handleSkillAdd adds a new skill from a SkillDocument.
func (s *Server) handleSkillAdd(c *gin.Context) {
	if s.skills == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "skill manager not available"})
		return
	}

	var req skillImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	doc, err := resolveSkillDocument(req)
	if err != nil {
		handleEngineError(c, err)
		return
	}
	if doc.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "name is required"})
		return
	}

	if !waitForImport(req) {
		taskID, err := s.startSkillImport(c.Request.Context(), doc)
		if err != nil {
			handleEngineError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"task_id": taskID, "status": "pending"})
		return
	}

	meta, err := s.skills.Add(c.Request.Context(), doc)
	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, meta)
}

// handleSkillInfo returns a single skill by ID.
func (s *Server) handleSkillInfo(c *gin.Context) {
	if s.skills == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "skill manager not available"})
		return
	}

	id := c.Param("id")
	meta, err := s.skills.Info(c.Request.Context(), id)
	if err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, meta)
}

// handleSkillEnable enables a skill by ID.
func (s *Server) handleSkillEnable(c *gin.Context) {
	if s.skills == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "skill manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.skills.Enable(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "skill enabled"})
}

// handleSkillDisable disables a skill by ID.
func (s *Server) handleSkillDisable(c *gin.Context) {
	if s.skills == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "skill manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.skills.Disable(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "skill disabled"})
}

// handleSkillRemove removes a skill by ID.
func (s *Server) handleSkillRemove(c *gin.Context) {
	if s.skills == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "skill manager not available"})
		return
	}

	id := c.Param("id")
	if err := s.skills.Remove(c.Request.Context(), id); err != nil {
		handleEngineError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "skill removed"})
}

// handleWebhookList lists all webhook subscriptions.
func (s *Server) handleWebhookList(c *gin.Context) {
	if s.webhooks == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "webhook manager not available"})
		return
	}
	tenantID := c.DefaultQuery("tenant_id", "default")
	webhooks, err := s.webhooks.List(c.Request.Context(), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to list webhooks"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"webhooks": webhooks})
}

// handleWebhookAdd adds a new webhook subscription.
func (s *Server) handleWebhookAdd(c *gin.Context) {
	if s.webhooks == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "webhook manager not available"})
		return
	}
	var req struct {
		TenantID string   `json:"tenant_id"`
		URL      string   `json:"url"`
		Events   []string `json:"events"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "invalid request body: " + err.Error()})
		return
	}
	if req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "url is required"})
		return
	}
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	id, err := s.webhooks.Subscribe(c.Request.Context(), tenantID, req.URL, req.Events)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to add webhook"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "status": "created"})
}

// handleWebhookRemove removes a webhook subscription.
func (s *Server) handleWebhookRemove(c *gin.Context) {
	if s.webhooks == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "webhook manager not available"})
		return
	}
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": types.ErrBadRequest, "message": "id is required"})
		return
	}
	if err := s.webhooks.Unsubscribe(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to remove webhook"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed"})
}

// --- Observer endpoints (placeholder) ---

// handleObserverSystem returns basic system status.
func (s *Server) handleObserverSystem(c *gin.Context) {
	preferredNode := ""
	if s.affinity != nil {
		preferredNode = s.affinity.GetNode(c.Query("session_id"))
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"timestamp":        time.Now().UTC(),
		"node_id":          s.nodeID,
		"affinity_enabled": s.affinity != nil,
		"preferred_node":   preferredNode,
		"mcp_enabled":      true,
	})
}

// handleObserverQueue returns queue statistics.
func (s *Server) handleObserverQueue(c *gin.Context) {
	if s.tasks == nil {
		c.JSON(http.StatusOK, gin.H{"queue": map[string]interface{}{}})
		return
	}

	stats, err := s.tasks.QueueStats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to get queue stats"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"queue": stats})
}

// --- Token usage ---

// handleTokenUsageQuery returns aggregated token usage.
func (s *Server) handleTokenUsageQuery(c *gin.Context) {
	if s.tokenAudit == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": types.ErrInternal, "message": "token auditor not available"})
		return
	}

	tenantID := c.DefaultQuery("tenant_id", "default")
	fromStr := c.DefaultQuery("from", "")
	toStr := c.DefaultQuery("to", "")

	from := time.Now().AddDate(0, 0, -30)
	to := time.Now()

	if fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = t
		}
	}
	if toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = t
		}
	}

	result, err := s.tokenAudit.Aggregate(c.Request.Context(), tenantID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": types.ErrInternal, "message": "failed to query token usage"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"usage": result})
}
