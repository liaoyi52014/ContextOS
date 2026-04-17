package log

import (
	"sync"
	"time"

	"github.com/contextos/contextos/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// LogCategory defines the four log categories.
type LogCategory string

const (
	CategorySystem  LogCategory = "system"
	CategoryRequest LogCategory = "request"
	CategoryEngine  LogCategory = "engine"
	CategoryAudit   LogCategory = "audit"
)

// Logger wraps zap.Logger with component-level loggers and config.
type Logger struct {
	base       *zap.Logger
	cfg        config.LogConfig
	components map[string]*zap.Logger
	mu         sync.RWMutex
}

// NewLogger creates a new Logger from the given LogConfig.
func NewLogger(cfg config.LogConfig) (*Logger, error) {
	level := ParseLevel(cfg.Level)

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.LevelKey = "level"
	encoderCfg.MessageKey = "msg"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	zapCfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Encoding:         "json",
		EncoderConfig:    encoderCfg,
		OutputPaths:      buildOutputPaths(cfg),
		ErrorOutputPaths: []string{"stderr"},
	}

	base, err := zapCfg.Build()
	if err != nil {
		return nil, err
	}

	return &Logger{
		base:       base,
		cfg:        cfg,
		components: make(map[string]*zap.Logger),
	}, nil
}

// Component returns a child logger with the "component" field set.
// If the component has a custom level in config.ComponentLevels, that level is used.
// Component loggers are cached for reuse.
func (l *Logger) Component(name string) *zap.Logger {
	l.mu.RLock()
	if cl, ok := l.components[name]; ok {
		l.mu.RUnlock()
		return cl
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock.
	if cl, ok := l.components[name]; ok {
		return cl
	}

	child := l.base.With(zap.String("component", name))

	// Apply per-component level if configured.
	if lvlStr, ok := l.cfg.ComponentLevels[name]; ok {
		lvl := ParseLevel(lvlStr)
		child = child.WithOptions(zap.IncreaseLevel(lvl))
	}

	l.components[name] = child
	return child
}

// WithContext returns a child logger with trace_id, session_id, and tenant fields pre-set.
func (l *Logger) WithContext(traceID, sessionID, tenantID string) *zap.Logger {
	return l.base.With(
		zap.String("trace_id", traceID),
		zap.String("session_id", sessionID),
		zap.String("tenant", tenantID),
	)
}

// WithDuration returns a zap.Float64 field for duration_ms.
func (l *Logger) WithDuration(d time.Duration) zap.Field {
	return zap.Float64("duration_ms", float64(d.Nanoseconds())/1e6)
}

// Base returns the underlying zap.Logger.
func (l *Logger) Base() *zap.Logger {
	return l.base
}

// Sync flushes any buffered log entries.
func (l *Logger) Sync() error {
	return l.base.Sync()
}

// ParseLevel converts a string level to zapcore.Level.
// Supported values: "debug", "info", "warn", "error". Defaults to info.
func ParseLevel(s string) zapcore.Level {
	switch s {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// buildOutputPaths determines output paths from config.
func buildOutputPaths(cfg config.LogConfig) []string {
	switch cfg.Output {
	case "file":
		if cfg.FilePath != "" {
			return []string{cfg.FilePath}
		}
		return []string{"stdout"}
	case "stderr":
		return []string{"stderr"}
	default:
		return []string{"stdout"}
	}
}
