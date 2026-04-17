package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/contextos/contextos/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"pgregory.net/rapid"
)

// newTestLogger creates a Logger backed by a bytes.Buffer for capturing JSON output.
// The base level is set to the given zapcore.Level so all logs at or above that level are captured.
func newTestLogger(buf *bytes.Buffer, cfg config.LogConfig, baseLevel zapcore.Level) *Logger {
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.LevelKey = "level"
	encoderCfg.MessageKey = "msg"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(buf),
		baseLevel,
	)

	return &Logger{
		base:       zap.New(core),
		cfg:        cfg,
		components: make(map[string]*zap.Logger),
	}
}

// Feature: context-engine-middleware, Property 28: 结构化日志格式
// **Validates: Requirements 12.2**
//
// For any log event, the output JSON should contain `ts`, `level`, `msg`,
// `component` fields and be valid JSON.
func TestProperty28_StructuredLogFormat(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate random component name and message
		component := rapid.StringMatching(`[a-zA-Z][a-zA-Z0-9_]{0,15}`).Draw(rt, "component")
		msg := rapid.StringMatching(`[a-zA-Z0-9 _\-\.]{1,64}`).Draw(rt, "msg")
		level := rapid.SampledFrom([]string{"debug", "info", "warn", "error"}).Draw(rt, "level")

		var buf bytes.Buffer
		cfg := config.LogConfig{
			Level:  "debug",
			Format: "json",
			Output: "stdout",
		}
		logger := newTestLogger(&buf, cfg, zapcore.DebugLevel)

		// Get a component logger and log at the drawn level
		cl := logger.Component(component)
		switch level {
		case "debug":
			cl.Debug(msg)
		case "info":
			cl.Info(msg)
		case "warn":
			cl.Warn(msg)
		case "error":
			cl.Error(msg)
		}

		_ = logger.Sync()

		output := strings.TrimSpace(buf.String())
		if output == "" {
			rt.Fatalf("expected log output, got empty string")
		}

		// Each line should be valid JSON with required fields
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			var entry map[string]interface{}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				rt.Fatalf("log output is not valid JSON: %v\nline: %s", err, line)
			}

			// Verify required fields exist
			requiredFields := []string{"ts", "level", "msg", "component"}
			for _, field := range requiredFields {
				if _, ok := entry[field]; !ok {
					rt.Fatalf("log entry missing required field %q: %s", field, line)
				}
			}

			// Verify field values are non-empty strings
			if entry["ts"] == "" {
				rt.Fatalf("ts field is empty: %s", line)
			}
			if entry["level"] == "" {
				rt.Fatalf("level field is empty: %s", line)
			}
			if entry["msg"] == "" {
				rt.Fatalf("msg field is empty: %s", line)
			}
			if entry["component"] == "" {
				rt.Fatalf("component field is empty: %s", line)
			}

			// Verify the component and msg match what we logged
			if entry["component"] != component {
				rt.Fatalf("expected component=%q, got %q", component, entry["component"])
			}
			if entry["msg"] != msg {
				rt.Fatalf("expected msg=%q, got %q", msg, entry["msg"])
			}
		}
	})
}

// Feature: context-engine-middleware, Property 29: 日志级别过滤
// **Validates: Requirements 12.4**
//
// For any component-level configuration, logs below the configured level
// should not be output.
func TestProperty29_LogLevelFiltering(t *testing.T) {
	// All levels ordered from lowest to highest severity
	allLevels := []string{"debug", "info", "warn", "error"}
	levelSeverity := map[string]int{
		"debug": 0,
		"info":  1,
		"warn":  2,
		"error": 3,
	}

	rapid.Check(t, func(rt *rapid.T) {
		// Pick a component level threshold
		componentLevel := rapid.SampledFrom(allLevels).Draw(rt, "componentLevel")
		component := rapid.StringMatching(`[a-zA-Z][a-zA-Z0-9_]{0,15}`).Draw(rt, "component")

		var buf bytes.Buffer
		cfg := config.LogConfig{
			Level:  "debug", // base level is debug so filtering is done per-component
			Format: "json",
			Output: "stdout",
			ComponentLevels: map[string]string{
				component: componentLevel,
			},
		}
		logger := newTestLogger(&buf, cfg, zapcore.DebugLevel)

		cl := logger.Component(component)

		// Log at every level
		cl.Debug("debug-msg")
		cl.Info("info-msg")
		cl.Warn("warn-msg")
		cl.Error("error-msg")

		_ = logger.Sync()

		output := strings.TrimSpace(buf.String())

		// Parse all log entries
		var entries []map[string]interface{}
		if output != "" {
			for _, line := range strings.Split(output, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var entry map[string]interface{}
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					rt.Fatalf("invalid JSON: %v\nline: %s", err, line)
				}
				entries = append(entries, entry)
			}
		}

		thresholdSeverity := levelSeverity[componentLevel]

		// Verify: no log entry should have a level below the threshold
		for _, entry := range entries {
			entryLevel, ok := entry["level"].(string)
			if !ok {
				rt.Fatalf("level field is not a string: %v", entry["level"])
			}
			entrySeverity, exists := levelSeverity[entryLevel]
			if !exists {
				rt.Fatalf("unknown level in output: %q", entryLevel)
			}
			if entrySeverity < thresholdSeverity {
				rt.Fatalf("log at level %q should have been filtered (component threshold: %q)", entryLevel, componentLevel)
			}
		}

		// Verify: all levels at or above threshold should be present
		expectedCount := 0
		for _, lvl := range allLevels {
			if levelSeverity[lvl] >= thresholdSeverity {
				expectedCount++
			}
		}
		if len(entries) != expectedCount {
			rt.Fatalf("expected %d log entries for threshold %q, got %d", expectedCount, componentLevel, len(entries))
		}
	})
}
