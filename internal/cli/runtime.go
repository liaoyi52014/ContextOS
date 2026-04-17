package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/contextos/contextos/internal/api"
	"github.com/contextos/contextos/internal/auth"
	"github.com/contextos/contextos/internal/cluster"
	"github.com/contextos/contextos/internal/config"
	"github.com/contextos/contextos/internal/engine"
	ctxlog "github.com/contextos/contextos/internal/log"
	"github.com/contextos/contextos/internal/migrate"
	"github.com/contextos/contextos/internal/store"
	"github.com/contextos/contextos/internal/types"
	ctxwebhook "github.com/contextos/contextos/internal/webhook"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type runtimeEnv struct {
	cfg            *config.Config
	logger         *ctxlog.Logger
	pgPool         *pgxpool.Pool
	redisClient    redis.UniversalClient
	cache          types.CacheStore
	sessionManager *engine.SessionManager
	engine         types.Engine
	server         *api.Server
	nodeID         string
	affinity       *cluster.ConsistentHash
	cancel         context.CancelFunc
}

func bootstrapRuntime(cfg *config.Config) (*runtimeEnv, error) {
	logger, err := ctxlog.NewLogger(cfg.Log)
	if err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	bootCtx, bootCancel := context.WithTimeout(ctx, 30*time.Second)
	defer bootCancel()

	pgPool, err := pgxpool.New(bootCtx, cfg.Postgres.DSN)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pgPool.Ping(bootCtx); err != nil {
		cancel()
		pgPool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	redisClient, err := connectRedis(cfg.Redis)
	if err != nil {
		cancel()
		pgPool.Close()
		return nil, err
	}
	if err := redisClient.Ping(bootCtx).Err(); err != nil {
		cancel()
		pgPool.Close()
		redisClient.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	cache := store.NewRedisCacheStore(redisClient)

	if cfg.Migrate.AutoMigrate {
		migrator := migrate.NewMigrator(pgPool, &cfg.Migrate)
		if err := migrator.Run(bootCtx); err != nil {
			cancel()
			pgPool.Close()
			redisClient.Close()
			return nil, fmt.Errorf("run migrations: %w", err)
		}
	}

	vectorStore, err := buildVectorStore(cfg.Vector.Backend, pgPool)
	if err != nil {
		cancel()
		pgPool.Close()
		redisClient.Close()
		return nil, err
	}
	embedding := store.NewOpenAIEmbeddingProvider(cfg.Embedding.APIBase, cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimension)
	if err := vectorStore.Init(bootCtx, embedding.Dimension()); err != nil {
		cancel()
		pgPool.Close()
		redisClient.Close()
		return nil, fmt.Errorf("init vector store: %w", err)
	}
	llm := store.NewOpenAILLMClient(cfg.LLM.APIBase, cfg.LLM.APIKey, cfg.LLM.Model)

	sessionStore := store.NewPGSessionStore(pgPool)
	profileStore := store.NewPGProfileStore(pgPool)
	sessionManager := engine.NewSessionManager(cache, sessionStore, engine.SessionConfig{
		MaxMessages:         cfg.Engine.MaxMessages,
		LRUCacheSize:        1000,
		LRUCacheTTLSec:      cfg.Engine.LRUCacheTTLSec,
		SyncQueueSize:       cfg.Engine.SyncQueueSize,
		SyncBatchSize:       cfg.Engine.SyncBatchSize,
		SyncFlushIntervalMs: cfg.Engine.SyncFlushIntervalMs,
	})

	taskTracker := engine.NewDefaultTaskTracker(cache, pgPool)
	toolRegistry := engine.NewToolRegistry()
	hooks := engine.NewHookManager(logger.Component("hooks"))
	models := engine.NewModelManager(pgPool, cache)
	_ = models.LoadAll(bootCtx)
	skills := engine.NewSkillManager(pgPool, cache, toolRegistry, embedding)
	_ = skills.LoadAll(bootCtx)
	webhooks := ctxwebhook.NewWebhookNotifier(pgPool)

	retrieval := engine.NewRetrievalEngine(vectorStore, embedding, engine.RetrievalConfig{
		RecallScoreThreshold: cfg.Engine.RecallScoreThreshold,
		RecallMaxResults:     cfg.Engine.RecallMaxResults,
		PatternMaxResults:    cfg.Engine.RecallMaxResults,
	}, pgPool)
	builder := engine.NewContextBuilder(vectorStore, embedding, sessionManager, profileStore, cache, skills, retrieval, engine.ContextConfig{
		TokenBudget:            cfg.Engine.TokenBudget,
		MaxMessages:            cfg.Engine.MaxMessages,
		RecentRawTurnCount:     cfg.Engine.RecentRawTurnCount,
		SkillBodyLoadThreshold: cfg.Engine.SkillBodyLoadThreshold,
		MaxLoadedSkillBodies:   cfg.Engine.MaxLoadedSkillBodies,
	})
	tokenAudit := ctxlog.NewTokenAuditor(pgPool)
	compact := engine.NewCompactProcessor(
		llm,
		sessionManager,
		profileStore,
		cache,
		vectorStore,
		embedding,
		taskTracker,
		hooks,
		webhooks,
		tokenAudit,
		&engine.CompactConfig{
			CompactBudgetRatio:    cfg.Engine.CompactBudgetRatio,
			CompactTokenThreshold: cfg.Engine.CompactTokenThreshold,
			CompactTurnThreshold:  cfg.Engine.CompactTurnThreshold,
			CompactIntervalMin:    cfg.Engine.CompactIntervalMin,
			CompactTimeoutSec:     cfg.Engine.CompactTimeoutSec,
			MaxConcurrentCompacts: cfg.Engine.MaxConcurrentCompacts,
			TokenBudget:           cfg.Engine.TokenBudget,
		},
		logger.Component("compact"),
	)
	keyMgr := auth.NewAPIKeyManager(pgPool, cache)
	if err := keyMgr.LoadKeys(bootCtx); err != nil {
		cancel()
		sessionManager.Stop()
		pgPool.Close()
		redisClient.Close()
		return nil, fmt.Errorf("load api keys: %w", err)
	}
	adminAuth := auth.NewAdminAuth(pgPool, cache)
	adminAuth.SetWebhookManager(webhooks)
	if cfg.Admin.BootstrapUsername != "" && cfg.Admin.BootstrapPassword != "" {
		if err := adminAuth.BootstrapDefaultAdmin(bootCtx, cfg.Admin.BootstrapUsername, cfg.Admin.BootstrapPassword); err != nil {
			cancel()
			sessionManager.Stop()
			pgPool.Close()
			redisClient.Close()
			return nil, fmt.Errorf("bootstrap default admin: %w", err)
		}
	}

	coreEngine := engine.NewCoreEngine(engine.CoreEngineDeps{
		Sessions:   sessionManager,
		Builder:    builder,
		Retrieval:  retrieval,
		Vector:     vectorStore,
		Embedding:  embedding,
		Compact:    compact,
		Tools:      toolRegistry,
		Hooks:      hooks,
		Tasks:      taskTracker,
		TokenAudit: tokenAudit,
	})

	nodeID, _ := os.Hostname()
	if nodeID == "" {
		nodeID = "contextos-local"
	}
	affinity := cluster.NewConsistentHash(100)
	affinity.AddNode(nodeID)

	server := api.NewServer(api.ServerDeps{
		Engine:       coreEngine,
		KeyMgr:       keyMgr,
		AdminAuth:    adminAuth,
		Skills:       skills,
		Models:       models,
		Tasks:        taskTracker,
		Audit:        ctxlog.NewAuditLogger(pgPool),
		TokenAudit:   tokenAudit,
		Webhooks:     webhooks,
		SessionStore: sessionStore,
		Cache:        cache,
		NodeID:       nodeID,
		Affinity:     affinity,
		Config:       cfg,
		Logger:       logger,
		ReadyCheck: func(ctx context.Context) error {
			if err := pgPool.Ping(ctx); err != nil {
				return err
			}
			_, err := cache.Get(ctx, "contextos:readyz")
			return err
		},
	})

	_ = keyMgr.StartInvalidationListener(ctx)
	_ = models.StartInvalidationListener(ctx)
	_ = skills.StartInvalidationListener(ctx)
	_ = cluster.NewReconciler(cache, sessionStore).Run(bootCtx)
	api.StartTempUploadJanitor(ctx, 5*time.Minute)

	return &runtimeEnv{
		cfg:            cfg,
		logger:         logger,
		pgPool:         pgPool,
		redisClient:    redisClient,
		cache:          cache,
		sessionManager: sessionManager,
		engine:         coreEngine,
		server:         server,
		nodeID:         nodeID,
		affinity:       affinity,
		cancel:         cancel,
	}, nil
}

func (e *runtimeEnv) Close() {
	if e == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	if e.sessionManager != nil {
		e.sessionManager.Stop()
	}
	if e.redisClient != nil {
		_ = e.redisClient.Close()
	}
	if e.pgPool != nil {
		e.pgPool.Close()
	}
	if e.logger != nil {
		_ = e.logger.Sync()
	}
}
