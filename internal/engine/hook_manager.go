package engine

import (
	"context"
	"sync"

	"github.com/contextos/contextos/internal/types"
	"go.uber.org/zap"
)

// HookManager manages lifecycle hook registration and triggering.
// It implements the HookNotifier interface defined in compact_processor.go.
type HookManager struct {
	hooks  map[types.HookEvent][]types.Hook
	mu     sync.RWMutex
	logger *zap.Logger
}

// NewHookManager creates an empty HookManager.
func NewHookManager(logger *zap.Logger) *HookManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HookManager{
		hooks:  make(map[types.HookEvent][]types.Hook),
		logger: logger,
	}
}

// Register adds a hook to all events it declares via hook.Events().
func (m *HookManager) Register(hook types.Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, event := range hook.Events() {
		m.hooks[event] = append(m.hooks[event], hook)
	}
}

// Trigger calls all hooks registered for the given event, in registration
// order. If any hook returns an error, it is logged but execution continues
// for remaining hooks. Returns the last error encountered, or nil.
func (m *HookManager) Trigger(ctx context.Context, hookCtx types.HookContext) error {
	m.mu.RLock()
	handlers := make([]types.Hook, len(m.hooks[hookCtx.Event]))
	copy(handlers, m.hooks[hookCtx.Event])
	m.mu.RUnlock()

	var lastErr error
	for _, hook := range handlers {
		if err := hook.Execute(ctx, hookCtx); err != nil {
			m.logger.Warn("hook execution failed",
				zap.String("hook", hook.Name()),
				zap.String("event", string(hookCtx.Event)),
				zap.Error(err),
			)
			lastErr = err
		}
	}
	return lastErr
}
