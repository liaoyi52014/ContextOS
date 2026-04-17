package config

import (
	"strings"
	"testing"
)

func TestLoadConfigFromReader_Defaults(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader("{}"), "yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Server defaults
	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.DevelopmentMode {
		t.Error("expected development_mode false")
	}

	// Redis defaults
	if cfg.Redis.Mode != RedisModeStandalone {
		t.Errorf("expected redis mode standalone, got %s", cfg.Redis.Mode)
	}
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("expected redis addr localhost:6379, got %s", cfg.Redis.Addr)
	}
	if cfg.Redis.DB != 0 {
		t.Errorf("expected redis db 0, got %d", cfg.Redis.DB)
	}
	if cfg.Redis.PoolSize != 10 {
		t.Errorf("expected pool_size 10, got %d", cfg.Redis.PoolSize)
	}
	if cfg.Redis.MaxRetries != 3 {
		t.Errorf("expected max_retries 3, got %d", cfg.Redis.MaxRetries)
	}

	// Vector defaults
	if cfg.Vector.Backend != "pgvector" {
		t.Errorf("expected vector backend pgvector, got %s", cfg.Vector.Backend)
	}

	// Engine defaults
	if cfg.Engine.TokenBudget != 32000 {
		t.Errorf("expected token_budget 32000, got %d", cfg.Engine.TokenBudget)
	}
	if cfg.Engine.MaxMessages != 50 {
		t.Errorf("expected max_messages 50, got %d", cfg.Engine.MaxMessages)
	}
	if cfg.Engine.CompactBudgetRatio != 0.5 {
		t.Errorf("expected compact_budget_ratio 0.5, got %f", cfg.Engine.CompactBudgetRatio)
	}
	if cfg.Engine.CompactTokenThreshold != 16000 {
		t.Errorf("expected compact_token_threshold 16000, got %d", cfg.Engine.CompactTokenThreshold)
	}
	if cfg.Engine.CompactTurnThreshold != 10 {
		t.Errorf("expected compact_turn_threshold 10, got %d", cfg.Engine.CompactTurnThreshold)
	}
	if cfg.Engine.CompactIntervalMin != 15 {
		t.Errorf("expected compact_interval_min 15, got %d", cfg.Engine.CompactIntervalMin)
	}
	if cfg.Engine.CompactTimeoutSec != 120 {
		t.Errorf("expected compact_timeout_sec 120, got %d", cfg.Engine.CompactTimeoutSec)
	}
	if cfg.Engine.MaxConcurrentCompacts != 10 {
		t.Errorf("expected max_concurrent_compacts 10, got %d", cfg.Engine.MaxConcurrentCompacts)
	}
	if cfg.Engine.RecentRawTurnCount != 8 {
		t.Errorf("expected recent_raw_turn_count 8, got %d", cfg.Engine.RecentRawTurnCount)
	}
	if cfg.Engine.RecallScoreThreshold != 0.7 {
		t.Errorf("expected recall_score_threshold 0.7, got %f", cfg.Engine.RecallScoreThreshold)
	}
	if cfg.Engine.RecallMaxResults != 10 {
		t.Errorf("expected recall_max_results 10, got %d", cfg.Engine.RecallMaxResults)
	}
	if cfg.Engine.SyncQueueSize != 10000 {
		t.Errorf("expected sync_queue_size 10000, got %d", cfg.Engine.SyncQueueSize)
	}
	if cfg.Engine.SyncBatchSize != 100 {
		t.Errorf("expected sync_batch_size 100, got %d", cfg.Engine.SyncBatchSize)
	}
	if cfg.Engine.SyncFlushIntervalMs != 500 {
		t.Errorf("expected sync_flush_interval_ms 500, got %d", cfg.Engine.SyncFlushIntervalMs)
	}
	if cfg.Engine.LRUCacheTTLSec != 5 {
		t.Errorf("expected lru_cache_ttl_sec 5, got %d", cfg.Engine.LRUCacheTTLSec)
	}
	if cfg.Engine.SlowQueryMs != 300 {
		t.Errorf("expected slow_query_ms 300, got %d", cfg.Engine.SlowQueryMs)
	}
	if cfg.Engine.SkillBodyLoadThreshold != 0.9 {
		t.Errorf("expected skill_body_load_threshold 0.9, got %f", cfg.Engine.SkillBodyLoadThreshold)
	}
	if cfg.Engine.MaxLoadedSkillBodies != 2 {
		t.Errorf("expected max_loaded_skill_bodies 2, got %d", cfg.Engine.MaxLoadedSkillBodies)
	}

	// Migrate defaults
	if !cfg.Migrate.AutoMigrate {
		t.Error("expected auto_migrate true")
	}
}

func TestLoadConfigFromReader_YAMLOverrides(t *testing.T) {
	yamlContent := `
server:
  port: 9090
  development_mode: true
admin:
  bootstrap_username: admin
  bootstrap_password: secret
redis:
  mode: sentinel
  sentinel_addrs:
    - "host1:26379"
    - "host2:26379"
  sentinel_master: mymaster
postgres:
  dsn: "postgres://user:pass@localhost/ctx"
engine:
  token_budget: 64000
  max_messages: 100
`
	cfg, err := LoadConfigFromReader(strings.NewReader(yamlContent), "yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if !cfg.Server.DevelopmentMode {
		t.Error("expected development_mode true")
	}
	if cfg.Admin.BootstrapUsername != "admin" {
		t.Errorf("expected bootstrap_username admin, got %s", cfg.Admin.BootstrapUsername)
	}
	if cfg.Redis.Mode != RedisModeSentinel {
		t.Errorf("expected redis mode sentinel, got %s", cfg.Redis.Mode)
	}
	if len(cfg.Redis.SentinelAddrs) != 2 {
		t.Errorf("expected 2 sentinel addrs, got %d", len(cfg.Redis.SentinelAddrs))
	}
	if cfg.Redis.SentinelMaster != "mymaster" {
		t.Errorf("expected sentinel_master mymaster, got %s", cfg.Redis.SentinelMaster)
	}
	if cfg.Postgres.DSN != "postgres://user:pass@localhost/ctx" {
		t.Errorf("unexpected dsn: %s", cfg.Postgres.DSN)
	}
	if cfg.Engine.TokenBudget != 64000 {
		t.Errorf("expected token_budget 64000, got %d", cfg.Engine.TokenBudget)
	}
	if cfg.Engine.MaxMessages != 100 {
		t.Errorf("expected max_messages 100, got %d", cfg.Engine.MaxMessages)
	}
	// Non-overridden defaults should still apply
	if cfg.Engine.SlowQueryMs != 300 {
		t.Errorf("expected slow_query_ms default 300, got %d", cfg.Engine.SlowQueryMs)
	}
}

func TestPrintConfig_RoundTrip(t *testing.T) {
	original := &Config{
		Server: ServerConfig{Port: 3000, DevelopmentMode: true},
		Admin:  AdminConfig{BootstrapUsername: "root", BootstrapPassword: "pw"},
		Redis:  RedisConfig{Mode: RedisModeCluster, ClusterAddrs: []string{"a:6379", "b:6379"}, PoolSize: 20, MaxRetries: 5},
		Postgres: PostgresConfig{DSN: "postgres://localhost/test"},
		Engine:   EngineConfig{TokenBudget: 16000, MaxMessages: 25},
		Migrate:  MigrateConfig{AutoMigrate: false},
	}

	data, err := PrintConfig(original)
	if err != nil {
		t.Fatalf("PrintConfig error: %v", err)
	}

	restored, err := LoadConfigFromReader(strings.NewReader(string(data)), "yaml")
	if err != nil {
		t.Fatalf("LoadConfigFromReader error: %v", err)
	}

	if restored.Server.Port != 3000 {
		t.Errorf("round-trip port: expected 3000, got %d", restored.Server.Port)
	}
	if !restored.Server.DevelopmentMode {
		t.Error("round-trip development_mode: expected true")
	}
	if restored.Redis.Mode != RedisModeCluster {
		t.Errorf("round-trip redis mode: expected cluster, got %s", restored.Redis.Mode)
	}
	if len(restored.Redis.ClusterAddrs) != 2 {
		t.Errorf("round-trip cluster_addrs: expected 2, got %d", len(restored.Redis.ClusterAddrs))
	}
	if restored.Postgres.DSN != "postgres://localhost/test" {
		t.Errorf("round-trip dsn: expected postgres://localhost/test, got %s", restored.Postgres.DSN)
	}
	if restored.Engine.TokenBudget != 16000 {
		t.Errorf("round-trip token_budget: expected 16000, got %d", restored.Engine.TokenBudget)
	}
	if restored.Migrate.AutoMigrate {
		t.Error("round-trip auto_migrate: expected false")
	}
}

func TestLoadConfigFromReader_JSONFormat(t *testing.T) {
	jsonContent := `{"server":{"port":7070},"redis":{"mode":"cluster","cluster_addrs":["n1:6379"]}}`
	cfg, err := LoadConfigFromReader(strings.NewReader(jsonContent), "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 7070 {
		t.Errorf("expected port 7070, got %d", cfg.Server.Port)
	}
	if cfg.Redis.Mode != RedisModeCluster {
		t.Errorf("expected redis mode cluster, got %s", cfg.Redis.Mode)
	}
}
