package cluster

import (
	"context"
	"net/http"
	"sync"
)

// GracefulShutdown coordinates orderly shutdown of server components:
// stop accepting requests, flush buffers, and release resources.
type GracefulShutdown struct {
	mu             sync.Mutex
	httpServer     *http.Server
	syncQueueStop  func()
	compactFlush   func()
}

// NewGracefulShutdown creates a new GracefulShutdown coordinator.
func NewGracefulShutdown() *GracefulShutdown {
	return &GracefulShutdown{}
}

// RegisterHTTPServer registers the HTTP server for graceful shutdown.
func (gs *GracefulShutdown) RegisterHTTPServer(srv *http.Server) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.httpServer = srv
}

// RegisterSyncQueue registers a stop function for the sync queue.
func (gs *GracefulShutdown) RegisterSyncQueue(stop func()) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.syncQueueStop = stop
}

// RegisterCompactFlush registers a flush function for the compact processor.
func (gs *GracefulShutdown) RegisterCompactFlush(flush func()) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.compactFlush = flush
}

// Shutdown performs an orderly shutdown:
// 1. Stop accepting new HTTP requests
// 2. Flush compact processor buffers
// 3. Stop the sync queue
func (gs *GracefulShutdown) Shutdown(ctx context.Context) error {
	gs.mu.Lock()
	srv := gs.httpServer
	syncStop := gs.syncQueueStop
	flush := gs.compactFlush
	gs.mu.Unlock()

	// Step 1: Stop accepting new requests.
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}
	}

	// Step 2: Flush compact processor buffers.
	if flush != nil {
		flush()
	}

	// Step 3: Stop the sync queue (drains pending items).
	if syncStop != nil {
		syncStop()
	}

	return nil
}
