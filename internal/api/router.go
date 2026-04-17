package api

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/contextos/contextos/internal/auth"
	"github.com/contextos/contextos/internal/cluster"
	"github.com/contextos/contextos/internal/config"
	"github.com/contextos/contextos/internal/engine"
	"github.com/contextos/contextos/internal/log"
	"github.com/contextos/contextos/internal/types"
	"github.com/gin-gonic/gin"
)

type skillAdmin interface {
	Add(ctx context.Context, doc types.SkillDocument) (*types.SkillMeta, error)
	List(ctx context.Context) ([]types.SkillMeta, error)
	Info(ctx context.Context, id string) (*types.SkillMeta, error)
	Enable(ctx context.Context, id string) error
	Disable(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
}

// ServerDeps holds all dependencies needed to construct a Server.
type ServerDeps struct {
	Engine       types.Engine
	KeyMgr       *auth.APIKeyManager
	AdminAuth    *auth.AdminAuth
	Skills       skillAdmin
	Models       *engine.ModelManager
	Tasks        types.TaskTracker
	Audit        *log.AuditLogger
	TokenAudit   *log.TokenAuditor
	Webhooks     types.WebhookManager
	SessionStore types.SessionStore
	Cache        types.CacheStore
	NodeID       string
	Affinity     *cluster.ConsistentHash
	Config       *config.Config
	Logger       *log.Logger
	ReadyCheck   func(context.Context) error
}

// Server is the HTTP API server.
type Server struct {
	engine                types.Engine
	keyMgr                *auth.APIKeyManager
	adminAuth             *auth.AdminAuth
	skills                skillAdmin
	models                *engine.ModelManager
	tasks                 types.TaskTracker
	audit                 *log.AuditLogger
	tokenAudit            *log.TokenAuditor
	webhooks              types.WebhookManager
	sessionStore          types.SessionStore
	cache                 types.CacheStore
	nodeID                string
	affinity              *cluster.ConsistentHash
	config                *config.Config
	logger                *log.Logger
	readyCheck            func(context.Context) error
	requestCount          atomic.Uint64
	toolExecCount         atomic.Uint64
	compactTriggeredCount atomic.Uint64
}

// NewServer creates a new Server from the given dependencies.
func NewServer(deps ServerDeps) *Server {
	return &Server{
		engine:       deps.Engine,
		keyMgr:       deps.KeyMgr,
		adminAuth:    deps.AdminAuth,
		skills:       deps.Skills,
		models:       deps.Models,
		tasks:        deps.Tasks,
		audit:        deps.Audit,
		tokenAudit:   deps.TokenAudit,
		webhooks:     deps.Webhooks,
		sessionStore: deps.SessionStore,
		cache:        deps.Cache,
		nodeID:       deps.NodeID,
		affinity:     deps.Affinity,
		config:       deps.Config,
		logger:       deps.Logger,
		readyCheck:   deps.ReadyCheck,
	}
}

// SetupRouter creates and configures the gin.Engine with all routes and middleware.
func (s *Server) SetupRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// Global middleware
	r.Use(func(c *gin.Context) {
		s.requestCount.Add(1)
		c.Next()
	})
	r.Use(RequestIDMiddleware())
	r.Use(ProcessTimeMiddleware())

	// Health checks — no auth
	r.GET("/healthz", s.handleHealthz)
	r.GET("/readyz", s.handleReadyz)

	// Prometheus metrics placeholder
	r.GET("/metrics", s.handleMetrics)

	devMode := false
	if s.config != nil {
		devMode = s.config.Server.DevelopmentMode
	}

	// Business API — service auth
	v1 := r.Group("/api/v1")
	v1.Use(ServiceAuthMiddleware(s.keyMgr, devMode))
	{
		v1.POST("/context/assemble", s.handleAssemble)
		v1.POST("/context/ingest", s.handleIngest)

		v1.GET("/sessions", s.handleSessionList)
		v1.DELETE("/sessions/:id", s.handleSessionDelete)

		v1.POST("/memory/search", s.handleMemorySearch)
		v1.POST("/memory/store", s.handleMemoryStore)
		v1.DELETE("/memory/:id", s.handleMemoryDelete)

		v1.POST("/tools/execute", s.handleToolExecute)

		v1.GET("/tasks/:task_id", s.handleGetTask)

		v1.POST("/uploads/temp", s.handleTempUpload)
	}

	// Auth endpoints — no admin auth required
	authGroup := r.Group("/api/v1/auth")
	{
		authGroup.POST("/setup", s.handleAuthSetup)
		authGroup.POST("/login", s.handleAuthLogin)
		authGroup.POST("/verify", s.handleAuthVerify)
	}

	// Admin API — admin auth required
	admin := r.Group("/api/v1/admin")
	admin.Use(AdminAuthMiddleware(s.adminAuth))
	{
		admin.GET("/users", s.handleAdminUserList)
		admin.POST("/users", s.handleAdminUserCreate)
		admin.PUT("/users/:id/password", s.handleAdminUserUpdatePassword)
		admin.PUT("/users/:id/disable", s.handleAdminUserDisable)

		admin.GET("/apikeys", s.handleAPIKeyList)
		admin.POST("/apikeys", s.handleAPIKeyCreate)
		admin.DELETE("/apikeys/:id", s.handleAPIKeyRevoke)

		admin.GET("/models", s.handleModelList)
		admin.POST("/models", s.handleModelAdd)
		admin.PUT("/models/:id/enable", s.handleModelEnable)
		admin.PUT("/models/:id/disable", s.handleModelDisable)
		admin.PUT("/models/:id/default", s.handleModelSetDefault)

		admin.GET("/providers", s.handleProviderList)
		admin.POST("/providers", s.handleProviderAdd)
		admin.PUT("/providers/:id", s.handleProviderUpdate)
		admin.DELETE("/providers/:id", s.handleProviderRemove)
	}

	// Skills API — admin auth required
	skillsGroup := r.Group("/api/v1/skills")
	skillsGroup.Use(AdminAuthMiddleware(s.adminAuth))
	{
		skillsGroup.GET("/", s.handleSkillList)
		skillsGroup.POST("/", s.handleSkillAdd)
		skillsGroup.GET("/:id", s.handleSkillInfo)
		skillsGroup.PUT("/:id/enable", s.handleSkillEnable)
		skillsGroup.PUT("/:id/disable", s.handleSkillDisable)
		skillsGroup.DELETE("/:id", s.handleSkillRemove)
	}

	// Observer API — admin auth required
	observer := r.Group("/api/v1/observer")
	observer.Use(AdminAuthMiddleware(s.adminAuth))
	{
		observer.GET("/system", s.handleObserverSystem)
		observer.GET("/queue", s.handleObserverQueue)
	}

	// Token usage — admin auth required
	usage := r.Group("/api/v1/usage")
	usage.Use(AdminAuthMiddleware(s.adminAuth))
	{
		usage.GET("/tokens", s.handleTokenUsageQuery)
	}

	// Webhooks — admin auth required
	webhooks := r.Group("/api/v1/webhooks")
	webhooks.Use(AdminAuthMiddleware(s.adminAuth))
	{
		webhooks.GET("/", s.handleWebhookList)
		webhooks.POST("/", s.handleWebhookAdd)
		webhooks.DELETE("/:id", s.handleWebhookRemove)
	}

	return r
}

// handleHealthz always returns 200 with status ok.
func (s *Server) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// handleReadyz checks PG and Redis connectivity.
func (s *Server) handleReadyz(c *gin.Context) {
	if s.readyCheck != nil {
		if err := s.readyCheck(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "error": err.Error()})
			return
		}
	} else if s.cache != nil {
		if _, err := s.cache.Get(c.Request.Context(), "contextos:readyz"); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

// handleMetrics returns a minimal Prometheus-compatible response.
func (s *Server) handleMetrics(c *gin.Context) {
	body := []byte(
		"# HELP contextos_http_requests_total Total HTTP requests handled.\n" +
			"# TYPE contextos_http_requests_total counter\n" +
			"contextos_http_requests_total " + itoa(s.requestCount.Load()) + "\n" +
			"# HELP contextos_tool_exec_total Total tool execute requests.\n" +
			"# TYPE contextos_tool_exec_total counter\n" +
			"contextos_tool_exec_total " + itoa(s.toolExecCount.Load()) + "\n" +
			"# HELP contextos_compact_triggered_total Total compact triggers.\n" +
			"# TYPE contextos_compact_triggered_total counter\n" +
			"contextos_compact_triggered_total " + itoa(s.compactTriggeredCount.Load()) + "\n",
	)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", body)
}

func itoa(v uint64) string {
	return fmt.Sprintf("%d", v)
}

func (s *Server) applySessionAffinity(c *gin.Context, sessionID string) {
	if s.affinity == nil || sessionID == "" {
		return
	}
	c.Header("X-Session-Affinity-Node", s.affinity.GetNode(sessionID))
}
