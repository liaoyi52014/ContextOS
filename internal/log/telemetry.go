package log

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// TelemetryCollector accumulates metrics during a single operation
// and builds a final OperationTelemetry summary.
type TelemetryCollector struct {
	operation string

	// token metrics
	promptTokens     int
	completionTokens int

	// vector metrics
	vectorSearchCount int
	vectorResultCount int

	// skill metrics
	skillCatalogCount int
	skillLoadedCount  int

	// compact metrics
	compactTriggered bool
	compactTaskID    string

	// profile metrics
	profileLoaded bool

	// errors
	errors []map[string]string

	// duration
	duration time.Duration
}

// NewTelemetryCollector creates a new collector for the given operation name.
func NewTelemetryCollector(operation string) *TelemetryCollector {
	return &TelemetryCollector{operation: operation}
}

// RecordTokens adds token usage to the collector.
func (tc *TelemetryCollector) RecordTokens(promptTokens, completionTokens int) {
	tc.promptTokens += promptTokens
	tc.completionTokens += completionTokens
}

// RecordVector records vector search metrics.
func (tc *TelemetryCollector) RecordVector(searchCount, resultCount int) {
	tc.vectorSearchCount += searchCount
	tc.vectorResultCount += resultCount
}

// RecordSkill records skill catalog and loading metrics.
func (tc *TelemetryCollector) RecordSkill(catalogCount, loadedCount int) {
	tc.skillCatalogCount = catalogCount
	tc.skillLoadedCount = loadedCount
}

// RecordCompact records whether compaction was triggered.
func (tc *TelemetryCollector) RecordCompact(triggered bool, taskID string) {
	tc.compactTriggered = triggered
	tc.compactTaskID = taskID
}

// RecordProfile records whether a user profile was loaded.
func (tc *TelemetryCollector) RecordProfile(loaded bool) {
	tc.profileLoaded = loaded
}

// RecordError records an error from a specific component.
func (tc *TelemetryCollector) RecordError(component, message string) {
	tc.errors = append(tc.errors, map[string]string{
		"component": component,
		"message":   message,
	})
}

// SetDuration sets the total operation duration.
func (tc *TelemetryCollector) SetDuration(d time.Duration) {
	tc.duration = d
}

// Build constructs the final OperationTelemetry, omitting empty groups.
func (tc *TelemetryCollector) Build() *types.OperationTelemetry {
	status := "ok"
	if len(tc.errors) > 0 {
		status = "error"
	}

	summary := types.TelemetrySummary{
		Operation:  tc.operation,
		Status:     status,
		DurationMS: float64(tc.duration.Nanoseconds()) / 1e6,
	}

	if tc.promptTokens > 0 || tc.completionTokens > 0 {
		summary.Tokens = map[string]interface{}{
			"prompt_tokens":     tc.promptTokens,
			"completion_tokens": tc.completionTokens,
			"total_tokens":      tc.promptTokens + tc.completionTokens,
		}
	}

	if tc.vectorSearchCount > 0 || tc.vectorResultCount > 0 {
		summary.Vector = map[string]interface{}{
			"search_count": tc.vectorSearchCount,
			"result_count": tc.vectorResultCount,
		}
	}

	if tc.skillCatalogCount > 0 || tc.skillLoadedCount > 0 {
		summary.Skill = map[string]interface{}{
			"catalog_count": tc.skillCatalogCount,
			"loaded_count":  tc.skillLoadedCount,
		}
	}

	if tc.compactTriggered {
		summary.Compact = map[string]interface{}{
			"triggered": tc.compactTriggered,
			"task_id":   tc.compactTaskID,
		}
	}

	if tc.profileLoaded {
		summary.Profile = map[string]interface{}{
			"loaded": tc.profileLoaded,
		}
	}

	if len(tc.errors) > 0 {
		summary.Errors = tc.errors
	}

	id := generateTelemetryID()
	return &types.OperationTelemetry{
		ID:      id,
		Summary: summary,
	}
}

// generateTelemetryID produces a random hex ID for telemetry correlation.
func generateTelemetryID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "tel_" + hex.EncodeToString(b)
}
