package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// SyncItem represents a pending write to PostgreSQL.
type SyncItem struct {
	SessionID    string
	TenantID     string
	UserID       string
	Message      types.Message
	Session      *types.Session
	UsageRecords []types.UsageRecord
	EnqueueAt    time.Time
}

// SyncQueue batches session writes and flushes them to PostgreSQL asynchronously.
type SyncQueue struct {
	ch            chan *SyncItem
	batchSize     int
	flushInterval time.Duration
	dlq           chan *SyncItem
	store         types.SessionStore
	done          chan struct{}
	stopped       chan struct{}
}

// NewSyncQueue creates a SyncQueue with the given parameters.
func NewSyncQueue(store types.SessionStore, size, batchSize int, flushInterval time.Duration) *SyncQueue {
	if size <= 0 {
		size = 10000
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	if flushInterval <= 0 {
		flushInterval = 500 * time.Millisecond
	}
	return &SyncQueue{
		ch:            make(chan *SyncItem, size),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		dlq:           make(chan *SyncItem, size),
		store:         store,
		done:          make(chan struct{}),
		stopped:       make(chan struct{}),
	}
}

// Enqueue sends an item to the sync queue. Blocks if the queue is full (backpressure).
func (q *SyncQueue) Enqueue(item *SyncItem) error {
	select {
	case q.ch <- item:
		return nil
	case <-q.done:
		return fmt.Errorf("sync_queue: queue is stopped")
	}
}

// Start launches the background worker goroutine that collects and flushes batches.
func (q *SyncQueue) Start() {
	go q.worker()
}

// Stop signals the worker to stop and waits for it to flush remaining items.
func (q *SyncQueue) Stop() {
	close(q.done)
	<-q.stopped
}

// DLQLen returns the number of items in the dead letter queue.
func (q *SyncQueue) DLQLen() int {
	return len(q.dlq)
}

func (q *SyncQueue) worker() {
	defer close(q.stopped)

	ticker := time.NewTicker(q.flushInterval)
	defer ticker.Stop()

	batch := make([]*SyncItem, 0, q.batchSize)

	for {
		select {
		case item := <-q.ch:
			batch = append(batch, item)
			if len(batch) >= q.batchSize {
				q.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				q.flush(batch)
				batch = batch[:0]
			}

		case <-q.done:
			// Drain remaining items from the channel.
			for {
				select {
				case item := <-q.ch:
					batch = append(batch, item)
				default:
					goto drained
				}
			}
		drained:
			if len(batch) > 0 {
				q.flush(batch)
			}
			return
		}
	}
}

// flush persists a batch of items in enqueue order.
// Retries up to 3 times with exponential backoff; failures go to DLQ.
func (q *SyncQueue) flush(batch []*SyncItem) {
	ctx := context.Background()
	for _, item := range batch {
		if item == nil {
			continue
		}
		if err := q.saveWithRetry(ctx, item); err != nil {
			// Send to dead letter queue.
			select {
			case q.dlq <- item:
			default:
				// DLQ full, item is dropped. In production this would be logged.
			}
		}
	}
}

func (q *SyncQueue) saveWithRetry(ctx context.Context, item *SyncItem) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 100ms, 200ms, 400ms
			time.Sleep(time.Duration(100<<uint(attempt)) * time.Millisecond)
		}

		if err := q.persistItem(ctx, item); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("sync_queue: save failed after %d retries: %w", maxRetries, lastErr)
}

func (q *SyncQueue) persistItem(ctx context.Context, item *SyncItem) error {
	if item.Message.Role != "" {
		if appender, ok := q.store.(types.MessageAppender); ok {
			if err := appender.AppendMessage(ctx, item.TenantID, item.UserID, item.SessionID, item.Message); err != nil {
				return err
			}
		} else {
			sess, err := q.ensureSession(ctx, item)
			if err != nil {
				return err
			}
			sess.Messages = append(sess.Messages, item.Message)
			sess.UpdatedAt = time.Now()
			if err := q.store.Save(ctx, sess); err != nil {
				return err
			}
		}
	}

	if len(item.UsageRecords) > 0 {
		if usageStore, ok := q.store.(types.UsageRecordStore); ok {
			if err := usageStore.SaveUsageRecords(ctx, item.TenantID, item.UserID, item.SessionID, item.UsageRecords); err != nil {
				return err
			}
		}
	}

	if item.Session != nil {
		item.Session.UpdatedAt = time.Now()
		if err := q.store.Save(ctx, item.Session); err != nil {
			return err
		}
	}

	return nil
}

func (q *SyncQueue) ensureSession(ctx context.Context, item *SyncItem) (*types.Session, error) {
	sess, err := q.store.Load(ctx, item.TenantID, item.UserID, item.SessionID)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		return sess, nil
	}

	sess = &types.Session{
		ID:       item.SessionID,
		TenantID: item.TenantID,
		UserID:   item.UserID,
		Messages: []types.Message{},
		Metadata: map[string]interface{}{},
	}
	return sess, nil
}
