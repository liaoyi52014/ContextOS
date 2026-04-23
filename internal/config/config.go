package config

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// RedisMode defines the Redis deployment mode.
type RedisMode string

const (
	RedisModeStandalone RedisMode = "standalone"
	RedisModeSentinel   RedisMode = "sentinel"
	RedisModeCluster    RedisMode = "cluster"
)

// Config is the top-level configuration structure.
type Config struct {
	Server    ServerConfig    `json:"server" yaml:"server" mapstructure:"server"`
	Admin     AdminConfig     `json:"admin" yaml:"admin" mapstructure:"admin"`
	Redis     RedisConfig     `json:"redis" yaml:"redis" mapstructure:"redis"`
	Postgres  PostgresConfig  `json:"postgres" yaml:"postgres" mapstructure:"postgres"`
	LLM       LLMConfig       `json:"llm" yaml:"llm" mapstructure:"llm"`
	Embedding EmbeddingConfig `json:"embedding" yaml:"embedding" mapstructure:"embedding"`
	Vector    VectorConfig    `json:"vector" yaml:"vector" mapstructure:"vector"`
	Engine    EngineConfig    `json:"engine" yaml:"engine" mapstructure:"engine"`
	Log       LogConfig       `json:"log" yaml:"log" mapstructure:"log"`
	Migrate   MigrateConfig   `json:"migrate" yaml:"migrate" mapstructure:"migrate"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	URL             string `json:"url" yaml:"url" mapstructure:"url"`
	Port            int    `json:"port" yaml:"port" mapstructure:"port"`
	DevelopmentMode bool   `json:"development_mode" yaml:"development_mode" mapstructure:"development_mode"`
}

// AdminConfig holds administrator credential settings.
type AdminConfig struct {
	BootstrapUsername string `json:"bootstrap_username" yaml:"bootstrap_username" mapstructure:"bootstrap_username"`
	BootstrapPassword string `json:"bootstrap_password" yaml:"bootstrap_password" mapstructure:"bootstrap_password"`
	Username          string `json:"username" yaml:"username" mapstructure:"username"`
	Password          string `json:"password" yaml:"password" mapstructure:"password"`
}

// RedisConfig holds Redis connection settings for all deployment modes.
type RedisConfig struct {
	Mode             RedisMode `json:"mode" yaml:"mode" mapstructure:"mode"`
	// standalone mode
	Addr             string    `json:"addr" yaml:"addr" mapstructure:"addr"`
	Password         string    `json:"password" yaml:"password" mapstructure:"password"`
	DB               int       `json:"db" yaml:"db" mapstructure:"db"`
	// sentinel mode
	SentinelAddrs    []string  `json:"sentinel_addrs" yaml:"sentinel_addrs" mapstructure:"sentinel_addrs"`
	SentinelMaster   string    `json:"sentinel_master" yaml:"sentinel_master" mapstructure:"sentinel_master"`
	SentinelPassword string    `json:"sentinel_password" yaml:"sentinel_password" mapstructure:"sentinel_password"`
	// cluster mode
	ClusterAddrs     []string  `json:"cluster_addrs" yaml:"cluster_addrs" mapstructure:"cluster_addrs"`
	// common
	PoolSize         int       `json:"pool_size" yaml:"pool_size" mapstructure:"pool_size"`
	MaxRetries       int       `json:"max_retries" yaml:"max_retries" mapstructure:"max_retries"`
}

// PostgresConfig holds PostgreSQL connection settings.
type PostgresConfig struct {
	DSN string `json:"dsn" yaml:"dsn" mapstructure:"dsn"`
}

// LLMConfig holds LLM API settings.
type LLMConfig struct {
	APIBase     string  `json:"api_base" yaml:"api_base" mapstructure:"api_base"`
	APIKey      string  `json:"api_key" yaml:"api_key" mapstructure:"api_key"`
	Model       string  `json:"model" yaml:"model" mapstructure:"model"`
	MaxTokens   int     `json:"max_tokens" yaml:"max_tokens" mapstructure:"max_tokens"`
	Temperature float64 `json:"temperature" yaml:"temperature" mapstructure:"temperature"`
}

// EmbeddingConfig holds embedding API settings.
type EmbeddingConfig struct {
	APIBase   string `json:"api_base" yaml:"api_base" mapstructure:"api_base"`
	APIKey    string `json:"api_key" yaml:"api_key" mapstructure:"api_key"`
	Model     string `json:"model" yaml:"model" mapstructure:"model"`
	Dimension int    `json:"dimension" yaml:"dimension" mapstructure:"dimension"`
}

// VectorConfig holds vector store backend settings.
type VectorConfig struct {
	Backend string `json:"backend" yaml:"backend" mapstructure:"backend"`
}

// EngineConfig holds core engine tuning parameters.
type EngineConfig struct {
	TokenBudget             int     `json:"token_budget" yaml:"token_budget" mapstructure:"token_budget"`
	MaxMessages             int     `json:"max_messages" yaml:"max_messages" mapstructure:"max_messages"`
	CompactBudgetRatio      float64 `json:"compact_budget_ratio" yaml:"compact_budget_ratio" mapstructure:"compact_budget_ratio"`
	CompactTokenThreshold   int     `json:"compact_token_threshold" yaml:"compact_token_threshold" mapstructure:"compact_token_threshold"`
	CompactTurnThreshold    int     `json:"compact_turn_threshold" yaml:"compact_turn_threshold" mapstructure:"compact_turn_threshold"`
	CompactIntervalMin      int     `json:"compact_interval_min" yaml:"compact_interval_min" mapstructure:"compact_interval_min"`
	CompactTimeoutSec       int     `json:"compact_timeout_sec" yaml:"compact_timeout_sec" mapstructure:"compact_timeout_sec"`
	MaxConcurrentCompacts   int     `json:"max_concurrent_compacts" yaml:"max_concurrent_compacts" mapstructure:"max_concurrent_compacts"`
	RecentRawTurnCount      int     `json:"recent_raw_turn_count" yaml:"recent_raw_turn_count" mapstructure:"recent_raw_turn_count"`
	RecallScoreThreshold    float64 `json:"recall_score_threshold" yaml:"recall_score_threshold" mapstructure:"recall_score_threshold"`
	RecallMaxResults        int     `json:"recall_max_results" yaml:"recall_max_results" mapstructure:"recall_max_results"`
	SyncQueueSize           int     `json:"sync_queue_size" yaml:"sync_queue_size" mapstructure:"sync_queue_size"`
	SyncBatchSize           int     `json:"sync_batch_size" yaml:"sync_batch_size" mapstructure:"sync_batch_size"`
	SyncFlushIntervalMs     int     `json:"sync_flush_interval_ms" yaml:"sync_flush_interval_ms" mapstructure:"sync_flush_interval_ms"`
	LRUCacheTTLSec          int     `json:"lru_cache_ttl_sec" yaml:"lru_cache_ttl_sec" mapstructure:"lru_cache_ttl_sec"`
	SlowQueryMs             int     `json:"slow_query_ms" yaml:"slow_query_ms" mapstructure:"slow_query_ms"`
	SkillBodyLoadThreshold  float64 `json:"skill_body_load_threshold" yaml:"skill_body_load_threshold" mapstructure:"skill_body_load_threshold"`
	MaxLoadedSkillBodies    int     `json:"max_loaded_skill_bodies" yaml:"max_loaded_skill_bodies" mapstructure:"max_loaded_skill_bodies"`
	LLMTimeoutSec           int     `json:"llm_timeout_sec" yaml:"llm_timeout_sec" mapstructure:"llm_timeout_sec"`
	EmbedTimeoutSec         int     `json:"embed_timeout_sec" yaml:"embed_timeout_sec" mapstructure:"embed_timeout_sec"`
	VectorTimeoutSec        int     `json:"vector_timeout_sec" yaml:"vector_timeout_sec" mapstructure:"vector_timeout_sec"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level           string            `json:"level" yaml:"level" mapstructure:"level"`
	Format          string            `json:"format" yaml:"format" mapstructure:"format"`
	Output          string            `json:"output" yaml:"output" mapstructure:"output"`
	FilePath        string            `json:"file_path" yaml:"file_path" mapstructure:"file_path"`
	MaxSizeMB       int               `json:"max_size_mb" yaml:"max_size_mb" mapstructure:"max_size_mb"`
	MaxBackups      int               `json:"max_backups" yaml:"max_backups" mapstructure:"max_backups"`
	MaxAgeDays      int               `json:"max_age_days" yaml:"max_age_days" mapstructure:"max_age_days"`
	ComponentLevels map[string]string `json:"component_levels" yaml:"component_levels" mapstructure:"component_levels"`
}

// MigrateConfig holds database migration settings.
type MigrateConfig struct {
	AutoMigrate bool `json:"auto_migrate" yaml:"auto_migrate" mapstructure:"auto_migrate"`
}

// SetDefaults registers all default values on the given viper instance.
func SetDefaults(v *viper.Viper) {
	// server
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.development_mode", false)

	// redis
	v.SetDefault("redis.mode", string(RedisModeStandalone))
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.pool_size", 10)
	v.SetDefault("redis.max_retries", 3)

	// vector
	v.SetDefault("vector.backend", "pgvector")

	// engine
	v.SetDefault("engine.token_budget", 32000)
	v.SetDefault("engine.max_messages", 50)
	v.SetDefault("engine.compact_budget_ratio", 0.5)
	v.SetDefault("engine.compact_token_threshold", 16000)
	v.SetDefault("engine.compact_turn_threshold", 10)
	v.SetDefault("engine.compact_interval_min", 15)
	v.SetDefault("engine.compact_timeout_sec", 120)
	v.SetDefault("engine.max_concurrent_compacts", 10)
	v.SetDefault("engine.recent_raw_turn_count", 8)
	v.SetDefault("engine.recall_score_threshold", 0.7)
	v.SetDefault("engine.recall_max_results", 10)
	v.SetDefault("engine.sync_queue_size", 10000)
	v.SetDefault("engine.sync_batch_size", 100)
	v.SetDefault("engine.sync_flush_interval_ms", 500)
	v.SetDefault("engine.lru_cache_ttl_sec", 5)
	v.SetDefault("engine.slow_query_ms", 300)
	v.SetDefault("engine.skill_body_load_threshold", 0.9)
	v.SetDefault("engine.max_loaded_skill_bodies", 2)
	v.SetDefault("engine.llm_timeout_sec", 60)
	v.SetDefault("engine.embed_timeout_sec", 30)
	v.SetDefault("engine.vector_timeout_sec", 30)

	// migrate
	v.SetDefault("migrate.auto_migrate", true)
}

// LoadConfig loads configuration from the given file path, applies environment
// variable overrides with the CONTEXTOS_ prefix, and returns the parsed Config.
func LoadConfig(path string) (*Config, error) {
	v := viper.New()
	SetDefaults(v)

	v.SetEnvPrefix("CONTEXTOS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.ctx")
		v.AddConfigPath("/etc/contextos")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	return &cfg, nil
}

// LoadConfigFromReader loads configuration from an io.Reader with the specified
// format (e.g. "yaml", "json", "toml"). Useful for testing.
func LoadConfigFromReader(reader io.Reader, format string) (*Config, error) {
	v := viper.New()
	SetDefaults(v)

	v.SetEnvPrefix("CONTEXTOS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetConfigType(format)
	if err := v.ReadConfig(reader); err != nil {
		return nil, fmt.Errorf("reading config from reader: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	return &cfg, nil
}

// PrintConfig marshals the given Config to YAML bytes.
func PrintConfig(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}
