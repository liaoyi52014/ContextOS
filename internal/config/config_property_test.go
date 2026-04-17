package config

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: context-engine-middleware, Property 13: 配置序列化往返一致性
// **Validates: Requirements 11.5, 11.6, 11.7**
//
// For any valid Config struct, PrintConfig (marshal to YAML) then
// LoadConfigFromReader (parse back) should produce an equivalent Config struct.
func TestProperty13_ConfigSerializationRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genValidConfig(t)

		// Marshal to YAML
		data, err := PrintConfig(cfg)
		if err != nil {
			t.Fatalf("PrintConfig failed: %v", err)
		}

		// Parse back from YAML
		restored, err := LoadConfigFromReader(strings.NewReader(string(data)), "yaml")
		if err != nil {
			t.Fatalf("LoadConfigFromReader failed: %v", err)
		}

		// Verify equivalence for all sections
		assertServerEqual(t, cfg.Server, restored.Server)
		assertAdminEqual(t, cfg.Admin, restored.Admin)
		assertRedisEqual(t, cfg.Redis, restored.Redis)
		assertPostgresEqual(t, cfg.Postgres, restored.Postgres)
		assertLLMEqual(t, cfg.LLM, restored.LLM)
		assertEmbeddingEqual(t, cfg.Embedding, restored.Embedding)
		assertVectorEqual(t, cfg.Vector, restored.Vector)
		assertEngineEqual(t, cfg.Engine, restored.Engine)
		assertLogEqual(t, cfg.Log, restored.Log)
		assertMigrateEqual(t, cfg.Migrate, restored.Migrate)
	})
}

// Feature: context-engine-middleware, Property 14: 环境变量覆盖配置
// **Validates: Requirements 11.2**
//
// For any config parameter and corresponding CONTEXTOS_ environment variable,
// when the env var is set, the final config value should equal the env var value.
func TestProperty14_EnvVarOverridesConfig(t *testing.T) {
	// Test server.port override
	t.Run("server_port", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			port := rapid.IntRange(1024, 65535).Draw(rt, "port")

			yamlContent := "server:\n  port: 8080\n"
			envVal := fmt.Sprintf("%d", port)
			t.Setenv("CONTEXTOS_SERVER_PORT", envVal)

			cfg, err := LoadConfigFromReader(strings.NewReader(yamlContent), "yaml")
			if err != nil {
				rt.Fatalf("LoadConfigFromReader failed: %v", err)
			}

			if cfg.Server.Port != port {
				rt.Fatalf("expected server.port=%d from env, got %d", port, cfg.Server.Port)
			}
		})
	})

	// Test engine.token_budget override
	t.Run("engine_token_budget", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			budget := rapid.IntRange(1000, 200000).Draw(rt, "budget")

			yamlContent := "engine:\n  token_budget: 32000\n"
			envVal := fmt.Sprintf("%d", budget)
			t.Setenv("CONTEXTOS_ENGINE_TOKEN_BUDGET", envVal)

			cfg, err := LoadConfigFromReader(strings.NewReader(yamlContent), "yaml")
			if err != nil {
				rt.Fatalf("LoadConfigFromReader failed: %v", err)
			}

			if cfg.Engine.TokenBudget != budget {
				rt.Fatalf("expected engine.token_budget=%d from env, got %d", budget, cfg.Engine.TokenBudget)
			}
		})
	})

	// Test redis.mode override
	t.Run("redis_mode", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			modes := []RedisMode{RedisModeStandalone, RedisModeSentinel, RedisModeCluster}
			mode := modes[rapid.IntRange(0, len(modes)-1).Draw(rt, "modeIdx")]

			yamlContent := "redis:\n  mode: standalone\n"
			t.Setenv("CONTEXTOS_REDIS_MODE", string(mode))

			cfg, err := LoadConfigFromReader(strings.NewReader(yamlContent), "yaml")
			if err != nil {
				rt.Fatalf("LoadConfigFromReader failed: %v", err)
			}

			if cfg.Redis.Mode != mode {
				rt.Fatalf("expected redis.mode=%s from env, got %s", mode, cfg.Redis.Mode)
			}
		})
	})
}

// --- Generators ---

func genNonEmptyString(t *rapid.T, label string) string {
	return rapid.StringMatching(`[a-zA-Z0-9_\-]{1,32}`).Draw(t, label)
}

func genValidConfig(t *rapid.T) *Config {
	mode := rapid.SampledFrom([]RedisMode{
		RedisModeStandalone, RedisModeSentinel, RedisModeCluster,
	}).Draw(t, "redisMode")

	rc := RedisConfig{
		Mode:       mode,
		Addr:       fmt.Sprintf("localhost:%d", rapid.IntRange(1024, 65535).Draw(t, "redisPort")),
		Password:   rapid.StringMatching(`[a-zA-Z0-9]{0,16}`).Draw(t, "redisPass"),
		DB:         rapid.IntRange(0, 15).Draw(t, "redisDB"),
		PoolSize:   rapid.IntRange(1, 100).Draw(t, "poolSize"),
		MaxRetries: rapid.IntRange(0, 10).Draw(t, "maxRetries"),
	}

	if mode == RedisModeSentinel {
		n := rapid.IntRange(1, 3).Draw(t, "sentinelCount")
		addrs := make([]string, n)
		for i := range addrs {
			addrs[i] = fmt.Sprintf("sentinel%d:%d", i, rapid.IntRange(26379, 26400).Draw(t, fmt.Sprintf("sentAddr%d", i)))
		}
		rc.SentinelAddrs = addrs
		rc.SentinelMaster = genNonEmptyString(t, "sentMaster")
		rc.SentinelPassword = rapid.StringMatching(`[a-zA-Z0-9]{0,16}`).Draw(t, "sentPass")
	} else if mode == RedisModeCluster {
		n := rapid.IntRange(1, 5).Draw(t, "clusterCount")
		addrs := make([]string, n)
		for i := range addrs {
			addrs[i] = fmt.Sprintf("node%d:%d", i, rapid.IntRange(6379, 6400).Draw(t, fmt.Sprintf("clAddr%d", i)))
		}
		rc.ClusterAddrs = addrs
	}

	backends := []string{"pgvector", "elasticsearch", "milvus"}

	return &Config{
		Server: ServerConfig{
			URL:             fmt.Sprintf("http://localhost:%d", rapid.IntRange(1024, 65535).Draw(t, "serverPort")),
			Port:            rapid.IntRange(1024, 65535).Draw(t, "port"),
			DevelopmentMode: rapid.Bool().Draw(t, "devMode"),
		},
		Admin: AdminConfig{
			BootstrapUsername: genNonEmptyString(t, "bsUser"),
			BootstrapPassword: genNonEmptyString(t, "bsPass"),
			Username:          genNonEmptyString(t, "adminUser"),
			Password:          genNonEmptyString(t, "adminPass"),
		},
		Redis:    rc,
		Postgres: PostgresConfig{DSN: fmt.Sprintf("postgres://u:p@localhost/%s", genNonEmptyString(t, "pgDB"))},
		LLM: LLMConfig{
			APIBase:     fmt.Sprintf("https://%s.example.com", genNonEmptyString(t, "llmHost")),
			APIKey:      genNonEmptyString(t, "llmKey"),
			Model:       genNonEmptyString(t, "llmModel"),
			MaxTokens:   rapid.IntRange(100, 100000).Draw(t, "llmMaxTok"),
			Temperature: rapid.Float64Range(0.0, 2.0).Draw(t, "llmTemp"),
		},
		Embedding: EmbeddingConfig{
			APIBase:   fmt.Sprintf("https://%s.example.com", genNonEmptyString(t, "embHost")),
			APIKey:    genNonEmptyString(t, "embKey"),
			Model:     genNonEmptyString(t, "embModel"),
			Dimension: rapid.IntRange(64, 4096).Draw(t, "embDim"),
		},
		Vector: VectorConfig{
			Backend: rapid.SampledFrom(backends).Draw(t, "vecBackend"),
		},
		Engine: EngineConfig{
			TokenBudget:            rapid.IntRange(1000, 200000).Draw(t, "tokenBudget"),
			MaxMessages:            rapid.IntRange(10, 500).Draw(t, "maxMsg"),
			CompactBudgetRatio:     rapid.Float64Range(0.1, 0.9).Draw(t, "compactRatio"),
			CompactTokenThreshold:  rapid.IntRange(1000, 100000).Draw(t, "compactTokThresh"),
			CompactTurnThreshold:   rapid.IntRange(1, 100).Draw(t, "compactTurnThresh"),
			CompactIntervalMin:     rapid.IntRange(1, 120).Draw(t, "compactInterval"),
			CompactTimeoutSec:      rapid.IntRange(10, 600).Draw(t, "compactTimeout"),
			MaxConcurrentCompacts:  rapid.IntRange(1, 50).Draw(t, "maxCompacts"),
			RecentRawTurnCount:     rapid.IntRange(1, 50).Draw(t, "recentTurns"),
			RecallScoreThreshold:   rapid.Float64Range(0.1, 1.0).Draw(t, "recallThresh"),
			RecallMaxResults:       rapid.IntRange(1, 100).Draw(t, "recallMax"),
			SyncQueueSize:          rapid.IntRange(100, 100000).Draw(t, "syncQSize"),
			SyncBatchSize:          rapid.IntRange(10, 1000).Draw(t, "syncBatch"),
			SyncFlushIntervalMs:    rapid.IntRange(50, 5000).Draw(t, "syncFlush"),
			LRUCacheTTLSec:         rapid.IntRange(1, 300).Draw(t, "lruTTL"),
			SlowQueryMs:            rapid.IntRange(50, 5000).Draw(t, "slowQ"),
			SkillBodyLoadThreshold: rapid.Float64Range(0.1, 1.0).Draw(t, "skillThresh"),
			MaxLoadedSkillBodies:   rapid.IntRange(1, 20).Draw(t, "maxSkillBodies"),
		},
		Log: LogConfig{
			Level:    rapid.SampledFrom([]string{"debug", "info", "warn", "error"}).Draw(t, "logLevel"),
			Format:   rapid.SampledFrom([]string{"json", "text"}).Draw(t, "logFmt"),
			Output:   rapid.SampledFrom([]string{"stdout", "file"}).Draw(t, "logOut"),
			FilePath: fmt.Sprintf("/var/log/%s.log", genNonEmptyString(t, "logFile")),
		},
		Migrate: MigrateConfig{
			AutoMigrate: rapid.Bool().Draw(t, "autoMigrate"),
		},
	}
}

// --- Assertion helpers ---

func assertServerEqual(t *rapid.T, a, b ServerConfig) {
	if a.URL != b.URL {
		t.Fatalf("Server.URL: %q != %q", a.URL, b.URL)
	}
	if a.Port != b.Port {
		t.Fatalf("Server.Port: %d != %d", a.Port, b.Port)
	}
	if a.DevelopmentMode != b.DevelopmentMode {
		t.Fatalf("Server.DevelopmentMode: %v != %v", a.DevelopmentMode, b.DevelopmentMode)
	}
}

func assertAdminEqual(t *rapid.T, a, b AdminConfig) {
	if a.BootstrapUsername != b.BootstrapUsername {
		t.Fatalf("Admin.BootstrapUsername: %q != %q", a.BootstrapUsername, b.BootstrapUsername)
	}
	if a.BootstrapPassword != b.BootstrapPassword {
		t.Fatalf("Admin.BootstrapPassword: %q != %q", a.BootstrapPassword, b.BootstrapPassword)
	}
	if a.Username != b.Username {
		t.Fatalf("Admin.Username: %q != %q", a.Username, b.Username)
	}
	if a.Password != b.Password {
		t.Fatalf("Admin.Password: %q != %q", a.Password, b.Password)
	}
}

func assertRedisEqual(t *rapid.T, a, b RedisConfig) {
	if a.Mode != b.Mode {
		t.Fatalf("Redis.Mode: %q != %q", a.Mode, b.Mode)
	}
	if a.Addr != b.Addr {
		t.Fatalf("Redis.Addr: %q != %q", a.Addr, b.Addr)
	}
	if a.Password != b.Password {
		t.Fatalf("Redis.Password: %q != %q", a.Password, b.Password)
	}
	if a.DB != b.DB {
		t.Fatalf("Redis.DB: %d != %d", a.DB, b.DB)
	}
	if a.PoolSize != b.PoolSize {
		t.Fatalf("Redis.PoolSize: %d != %d", a.PoolSize, b.PoolSize)
	}
	if a.MaxRetries != b.MaxRetries {
		t.Fatalf("Redis.MaxRetries: %d != %d", a.MaxRetries, b.MaxRetries)
	}
	if a.SentinelMaster != b.SentinelMaster {
		t.Fatalf("Redis.SentinelMaster: %q != %q", a.SentinelMaster, b.SentinelMaster)
	}
	if a.SentinelPassword != b.SentinelPassword {
		t.Fatalf("Redis.SentinelPassword: %q != %q", a.SentinelPassword, b.SentinelPassword)
	}
	assertStringSliceEqual(t, "Redis.SentinelAddrs", a.SentinelAddrs, b.SentinelAddrs)
	assertStringSliceEqual(t, "Redis.ClusterAddrs", a.ClusterAddrs, b.ClusterAddrs)
}

func assertPostgresEqual(t *rapid.T, a, b PostgresConfig) {
	if a.DSN != b.DSN {
		t.Fatalf("Postgres.DSN: %q != %q", a.DSN, b.DSN)
	}
}

func assertLLMEqual(t *rapid.T, a, b LLMConfig) {
	if a.APIBase != b.APIBase {
		t.Fatalf("LLM.APIBase: %q != %q", a.APIBase, b.APIBase)
	}
	if a.APIKey != b.APIKey {
		t.Fatalf("LLM.APIKey: %q != %q", a.APIKey, b.APIKey)
	}
	if a.Model != b.Model {
		t.Fatalf("LLM.Model: %q != %q", a.Model, b.Model)
	}
	if a.MaxTokens != b.MaxTokens {
		t.Fatalf("LLM.MaxTokens: %d != %d", a.MaxTokens, b.MaxTokens)
	}
	if a.Temperature != b.Temperature {
		t.Fatalf("LLM.Temperature: %f != %f", a.Temperature, b.Temperature)
	}
}

func assertEmbeddingEqual(t *rapid.T, a, b EmbeddingConfig) {
	if a.APIBase != b.APIBase {
		t.Fatalf("Embedding.APIBase: %q != %q", a.APIBase, b.APIBase)
	}
	if a.APIKey != b.APIKey {
		t.Fatalf("Embedding.APIKey: %q != %q", a.APIKey, b.APIKey)
	}
	if a.Model != b.Model {
		t.Fatalf("Embedding.Model: %q != %q", a.Model, b.Model)
	}
	if a.Dimension != b.Dimension {
		t.Fatalf("Embedding.Dimension: %d != %d", a.Dimension, b.Dimension)
	}
}

func assertVectorEqual(t *rapid.T, a, b VectorConfig) {
	if a.Backend != b.Backend {
		t.Fatalf("Vector.Backend: %q != %q", a.Backend, b.Backend)
	}
}

func assertEngineEqual(t *rapid.T, a, b EngineConfig) {
	if a.TokenBudget != b.TokenBudget {
		t.Fatalf("Engine.TokenBudget: %d != %d", a.TokenBudget, b.TokenBudget)
	}
	if a.MaxMessages != b.MaxMessages {
		t.Fatalf("Engine.MaxMessages: %d != %d", a.MaxMessages, b.MaxMessages)
	}
	if a.CompactBudgetRatio != b.CompactBudgetRatio {
		t.Fatalf("Engine.CompactBudgetRatio: %f != %f", a.CompactBudgetRatio, b.CompactBudgetRatio)
	}
	if a.CompactTokenThreshold != b.CompactTokenThreshold {
		t.Fatalf("Engine.CompactTokenThreshold: %d != %d", a.CompactTokenThreshold, b.CompactTokenThreshold)
	}
	if a.CompactTurnThreshold != b.CompactTurnThreshold {
		t.Fatalf("Engine.CompactTurnThreshold: %d != %d", a.CompactTurnThreshold, b.CompactTurnThreshold)
	}
	if a.CompactIntervalMin != b.CompactIntervalMin {
		t.Fatalf("Engine.CompactIntervalMin: %d != %d", a.CompactIntervalMin, b.CompactIntervalMin)
	}
	if a.CompactTimeoutSec != b.CompactTimeoutSec {
		t.Fatalf("Engine.CompactTimeoutSec: %d != %d", a.CompactTimeoutSec, b.CompactTimeoutSec)
	}
	if a.MaxConcurrentCompacts != b.MaxConcurrentCompacts {
		t.Fatalf("Engine.MaxConcurrentCompacts: %d != %d", a.MaxConcurrentCompacts, b.MaxConcurrentCompacts)
	}
	if a.RecentRawTurnCount != b.RecentRawTurnCount {
		t.Fatalf("Engine.RecentRawTurnCount: %d != %d", a.RecentRawTurnCount, b.RecentRawTurnCount)
	}
	if a.RecallScoreThreshold != b.RecallScoreThreshold {
		t.Fatalf("Engine.RecallScoreThreshold: %f != %f", a.RecallScoreThreshold, b.RecallScoreThreshold)
	}
	if a.RecallMaxResults != b.RecallMaxResults {
		t.Fatalf("Engine.RecallMaxResults: %d != %d", a.RecallMaxResults, b.RecallMaxResults)
	}
	if a.SyncQueueSize != b.SyncQueueSize {
		t.Fatalf("Engine.SyncQueueSize: %d != %d", a.SyncQueueSize, b.SyncQueueSize)
	}
	if a.SyncBatchSize != b.SyncBatchSize {
		t.Fatalf("Engine.SyncBatchSize: %d != %d", a.SyncBatchSize, b.SyncBatchSize)
	}
	if a.SyncFlushIntervalMs != b.SyncFlushIntervalMs {
		t.Fatalf("Engine.SyncFlushIntervalMs: %d != %d", a.SyncFlushIntervalMs, b.SyncFlushIntervalMs)
	}
	if a.LRUCacheTTLSec != b.LRUCacheTTLSec {
		t.Fatalf("Engine.LRUCacheTTLSec: %d != %d", a.LRUCacheTTLSec, b.LRUCacheTTLSec)
	}
	if a.SlowQueryMs != b.SlowQueryMs {
		t.Fatalf("Engine.SlowQueryMs: %d != %d", a.SlowQueryMs, b.SlowQueryMs)
	}
	if a.SkillBodyLoadThreshold != b.SkillBodyLoadThreshold {
		t.Fatalf("Engine.SkillBodyLoadThreshold: %f != %f", a.SkillBodyLoadThreshold, b.SkillBodyLoadThreshold)
	}
	if a.MaxLoadedSkillBodies != b.MaxLoadedSkillBodies {
		t.Fatalf("Engine.MaxLoadedSkillBodies: %d != %d", a.MaxLoadedSkillBodies, b.MaxLoadedSkillBodies)
	}
}

func assertLogEqual(t *rapid.T, a, b LogConfig) {
	if a.Level != b.Level {
		t.Fatalf("Log.Level: %q != %q", a.Level, b.Level)
	}
	if a.Format != b.Format {
		t.Fatalf("Log.Format: %q != %q", a.Format, b.Format)
	}
	if a.Output != b.Output {
		t.Fatalf("Log.Output: %q != %q", a.Output, b.Output)
	}
	if a.FilePath != b.FilePath {
		t.Fatalf("Log.FilePath: %q != %q", a.FilePath, b.FilePath)
	}
}

func assertMigrateEqual(t *rapid.T, a, b MigrateConfig) {
	if a.AutoMigrate != b.AutoMigrate {
		t.Fatalf("Migrate.AutoMigrate: %v != %v", a.AutoMigrate, b.AutoMigrate)
	}
}

func assertStringSliceEqual(t *rapid.T, name string, a, b []string) {
	if len(a) != len(b) {
		t.Fatalf("%s length: %d != %d", name, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("%s[%d]: %q != %q", name, i, a[i], b[i])
		}
	}
}
