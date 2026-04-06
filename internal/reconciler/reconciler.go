// Package reconciler detects and re-emits orphaned executions.
//
// An execution is orphaned when it has status='emitted' but was never
// delivered to the dispatcher (e.g., due to buffer overflow or crash).
//
// The reconciler periodically scans for orphaned executions and re-emits
// them to the event bus. Idempotency is guaranteed by the dispatcher's
// terminal state guards - if an execution was already processed, the
// re-emit is safely ignored.
package reconciler

import (
	"context"
	"log"
	"time"

	"github.com/djlord-it/cronlite/internal/dispatcher"
	"github.com/djlord-it/cronlite/internal/domain"
)

// SafetyMargin is the buffer added on top of the dispatcher's maximum retry
// duration to account for processing overhead, clock drift, and DB latency.
const SafetyMargin = 150 * time.Second

// Store defines the interface for fetching orphaned executions.
type Store interface {
	GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error)
	RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

// EventEmitter defines the interface for emitting trigger events.
type EventEmitter interface {
	Emit(ctx context.Context, event domain.TriggerEvent) error
}

// MetricsSink defines the interface for recording reconciler metrics.
// All methods must be non-blocking and fire-and-forget.
type MetricsSink interface {
	OrphanedExecutionsUpdate(count int)
}

// Config holds reconciler configuration.
type Config struct {
	// Interval is how often the reconciler runs.
	// Default: 5 minutes.
	Interval time.Duration

	// Threshold is the age after which an emitted execution is considered orphaned.
	// Must be >= dispatcher.MaxRetryDuration() to avoid re-emitting executions
	// that are still being actively retried.
	// Default: MaxRetryDuration + SafetyMargin (~15 minutes).
	Threshold time.Duration

	// RequeueThreshold is the age after which an in_progress execution is
	// considered stale and requeued back to emitted (DB dispatch mode crash
	// recovery). This can be much more aggressive than Threshold because the
	// SQL query uses FOR UPDATE SKIP LOCKED — if a dispatcher IS actively
	// processing the row, the lock prevents premature requeue.
	// Default: 2 minutes.
	RequeueThreshold time.Duration

	// BatchSize is the maximum number of orphans to process per cycle.
	// Default: 100.
	BatchSize int
}

// DefaultConfig returns the default reconciler configuration.
// The threshold is derived from the dispatcher's maximum retry duration plus
// a safety margin, ensuring the reconciler never re-emits executions that are
// still being actively retried.
// DefaultRequeueThreshold is the default age after which in_progress executions
// are requeued. Safe to be aggressive because FOR UPDATE SKIP LOCKED prevents
// requeuing rows that a dispatcher is actively holding.
const DefaultRequeueThreshold = 2 * time.Minute

func DefaultConfig() Config {
	return Config{
		Interval:         5 * time.Minute,
		Threshold:        dispatcher.MaxRetryDuration() + SafetyMargin,
		RequeueThreshold: DefaultRequeueThreshold,
		BatchSize:        100,
	}
}

// Reconciler detects orphaned executions and re-emits them.
type Reconciler struct {
	config  Config
	store   Store
	emitter EventEmitter
	metrics MetricsSink // optional, nil = disabled
	clock   func() time.Time
}

// New creates a new Reconciler.
func New(config Config, store Store, emitter EventEmitter) *Reconciler {
	return &Reconciler{
		config:  config,
		store:   store,
		emitter: emitter,
		clock:   time.Now,
	}
}

// WithMetrics attaches a metrics sink to the reconciler.
func (r *Reconciler) WithMetrics(sink MetricsSink) *Reconciler {
	r.metrics = sink
	return r
}

// Run starts the reconciliation loop. It blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()

	log.Printf("reconciler: started (interval=%s, threshold=%s, batch=%d)",
		r.config.Interval, r.config.Threshold, r.config.BatchSize)

	// Run immediately on startup, then on ticker
	r.runCycle(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("reconciler: stopped")
			return
		case <-ticker.C:
			r.runCycle(ctx)
		}
	}
}

// runCycle executes one reconciliation cycle.
func (r *Reconciler) runCycle(ctx context.Context) {
	now := r.clock().UTC()

	// Use separate thresholds for the two recovery operations:
	// - RequeueThreshold (aggressive, default 2m): safe because FOR UPDATE SKIP LOCKED
	//   prevents requeuing rows a dispatcher is actively holding.
	// - Threshold (conservative, default 15m): must exceed MaxRetryDuration to avoid
	//   re-emitting executions still being retried.
	requeueThreshold := r.config.RequeueThreshold
	if requeueThreshold == 0 {
		requeueThreshold = r.config.Threshold // backward compat
	}
	requeueCutoff := now.Add(-requeueThreshold)
	orphanCutoff := now.Add(-r.config.Threshold)

	// Requeue stale in_progress executions (DB dispatch mode crash recovery).
	// In channel mode this is a no-op (no in_progress rows exist).
	requeued, err := r.store.RequeueStaleExecutions(ctx, requeueCutoff, r.config.BatchSize)
	if err != nil {
		log.Printf("reconciler: failed to requeue stale executions: %v", err)
	} else if requeued > 0 {
		log.Printf("reconciler: requeued %d stale in_progress executions", requeued)
	}

	orphans, err := r.store.GetOrphanedExecutions(ctx, orphanCutoff, r.config.BatchSize)
	if err != nil {
		// DB error: log and abort cycle. Will retry next interval.
		log.Printf("reconciler: failed to fetch orphans: %v", err)
		return
	}

	// Report orphan count to metrics (even if zero)
	if r.metrics != nil {
		r.metrics.OrphanedExecutionsUpdate(len(orphans))
	}

	if len(orphans) == 0 {
		// Nothing to do. Silent success.
		return
	}

	log.Printf("reconciler: found %d orphaned executions", len(orphans))

	emitted := 0
	failed := 0

	for i := range orphans {
		exec := &orphans[i]
		// Check context before each emit to allow graceful shutdown
		if ctx.Err() != nil {
			log.Printf("reconciler: cycle interrupted, processed %d/%d orphans", emitted+failed, len(orphans))
			return
		}

		event := domain.TriggerEvent{
			ExecutionID: exec.ID,
			JobID:       exec.JobID,
			Namespace:   exec.Namespace,
			ScheduledAt: exec.ScheduledAt,
			FiredAt:     exec.FiredAt,
			CreatedAt:   now,
		}

		if err := r.emitter.Emit(ctx, event); err != nil {
			// Emit failed (buffer full, context cancelled).
			// Log and continue - will retry next cycle.
			log.Printf("reconciler: failed to re-emit execution=%s job=%s: %v",
				exec.ID, exec.JobID, err)
			failed++
			continue
		}

		log.Printf("reconciler: re-emitted execution=%s job=%s scheduled_at=%s (age=%s)",
			exec.ID, exec.JobID, exec.ScheduledAt.Format(time.RFC3339),
			now.Sub(exec.CreatedAt).Round(time.Second))
		emitted++
	}

	log.Printf("reconciler: cycle complete, re-emitted=%d, failed=%d", emitted, failed)
}
