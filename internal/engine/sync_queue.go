package engine

import (
	"context"
	"fmt"
	"log"
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
	RetryCount   int
}

// SyncQueue batches session writes and flushes them to PostgreSQL asynchronously.
type SyncQueue struct {
	ch               chan *SyncItem
	batchSize        int
	flushInterval    time.Duration
	dlq              chan *SyncItem
	store            types.SessionStore
	done             chan struct{}
	stopped          chan struct{}
	dlqDone          chan struct{}
	dlqStopped       chan struct{}
	maxDLQRetries    int
	dlqRetryInterval time.Duration
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
		ch:               make(chan *SyncItem, size),
		batchSize:        batchSize,
		flushInterval:    flushInterval,
		dlq:              make(chan *SyncItem, size),
		store:            store,
		done:             make(chan struct{}),
		stopped:          make(chan struct{}),
		dlqDone:          make(chan struct{}),
		dlqStopped:       make(chan struct{}),
		maxDLQRetries:    3,
		dlqRetryInterval: 30 * time.Second,
	}
}

// Enqueue sends an item to the sync queue. Blocks if the queue is full (backpressure).
func (q *SyncQueue) Enqueue(item *SyncItem) error {
	// Check if stopped first to avoid non-deterministic select behavior
	// when both ch (buffered with capacity) and done are ready.
	select {
	case <-q.done:
		return fmt.Errorf("sync_queue: queue is stopped")
	default:
	}
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
	go q.dlqWorker()
}

// Stop signals the worker to stop and waits for it to flush remaining items.
func (q *SyncQueue) Stop() {
	close(q.done)
	<-q.stopped
	close(q.dlqDone)
	<-q.dlqStopped
}

// DLQLen returns the number of items in the dead letter queue.
func (q *SyncQueue) DLQLen() int {
	return len(q.dlq)
}

// SetDLQRetryInterval overrides the DLQ retry interval. Must be called before Start().
func (q *SyncQueue) SetDLQRetryInterval(d time.Duration) {
	q.dlqRetryInterval = d
}

// SetMaxDLQRetries overrides the maximum DLQ retry count. Must be called before Start().
func (q *SyncQueue) SetMaxDLQRetries(n int) {
	q.maxDLQRetries = n
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
				// DLQ full, item is dropped.
				log.Printf("WARN sync_queue: DLQ full, dropping item session_id=%s tenant_id=%s", item.SessionID, item.TenantID)
			}
		}
	}
}

// dlqWorker periodically drains the DLQ and retries failed items.
func (q *SyncQueue) dlqWorker() {
	defer close(q.dlqStopped)

	ticker := time.NewTicker(q.dlqRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			q.drainDLQ()
		case <-q.dlqDone:
			// Final drain attempt before exiting.
			q.drainDLQ()
			return
		}
	}
}

// drainDLQ performs a non-blocking drain of available DLQ items and retries them.
func (q *SyncQueue) drainDLQ() {
	ctx := context.Background()
	for {
		select {
		case item := <-q.dlq:
			item.RetryCount++
			if item.RetryCount > q.maxDLQRetries {
				log.Printf("WARN sync_queue: discarding item after %d DLQ retries session_id=%s tenant_id=%s",
					item.RetryCount, item.SessionID, item.TenantID)
				continue
			}
			if err := q.persistItem(ctx, item); err != nil {
				// Retry failed, put back in DLQ if space available.
				select {
				case q.dlq <- item:
				default:
					log.Printf("WARN sync_queue: DLQ full on re-enqueue, discarding item session_id=%s tenant_id=%s retry=%d",
						item.SessionID, item.TenantID, item.RetryCount)
				}
			}
		default:
			// DLQ drained, nothing left to process.
			return
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
