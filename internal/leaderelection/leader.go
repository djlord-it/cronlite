// Package leaderelection provides Postgres advisory lock-based leader election.
//
// A single Postgres session-scoped advisory lock determines the leader.
// The lock is held for the lifetime of the dedicated database connection;
// there is no renewal or TTL. If the connection dies, Postgres automatically
// releases the lock server-side (timing depends on TCP keepalive settings).
//
// The heartbeat ping exists solely to detect local connection death so the
// leader can stop its duties promptly. It does NOT renew the lock.
package leaderelection

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// MetricsSink defines the interface for recording leader election metrics.
// All methods must be non-blocking and fire-and-forget.
type MetricsSink interface {
	LeaderStatusChanged(isLeader bool)
	LeaderAcquired()
	LeaderLost(reason string) // reason: "shutdown", "conn_lost", "error"
}

// Elector manages leader election using a Postgres advisory lock.
type Elector struct {
	db                *sql.DB
	lockKey           int64
	retryInterval     time.Duration // follower: how often to attempt lock acquisition
	heartbeatInterval time.Duration // leader: how often to ping dedicated connection
	onElected         func(ctx context.Context)
	onDemoted         func()
	metrics           MetricsSink // optional, nil = disabled
}

// New creates a new Elector.
//
// onElected is called in a new goroutine when this instance acquires the lock.
// The provided context is cancelled when leadership is lost.
// onElected should start leader duties (scheduler, reconciler) and return quickly.
//
// onDemoted is called synchronously when leadership is lost.
// It should stop leader duties and block until they are fully stopped.
// It must be idempotent.
func New(
	db *sql.DB,
	lockKey int64,
	retryInterval, heartbeatInterval time.Duration,
	onElected func(ctx context.Context),
	onDemoted func(),
) *Elector {
	return &Elector{
		db:                db,
		lockKey:           lockKey,
		retryInterval:     retryInterval,
		heartbeatInterval: heartbeatInterval,
		onElected:         onElected,
		onDemoted:         onDemoted,
	}
}

// WithMetrics attaches a metrics sink to the elector.
func (e *Elector) WithMetrics(sink MetricsSink) *Elector {
	e.metrics = sink
	return e
}

// Run starts the leader election loop. It blocks until ctx is cancelled.
func (e *Elector) Run(ctx context.Context) {
	log.Printf("leader: starting election loop (lock_key=%d, retry=%s, heartbeat=%s)",
		e.lockKey, e.retryInterval, e.heartbeatInterval)

	for {
		if ctx.Err() != nil {
			log.Println("leader: election loop stopped")
			return
		}

		reason := e.runOnce(ctx)

		if ctx.Err() != nil {
			log.Println("leader: election loop stopped")
			return
		}

		if reason != "" {
			log.Printf("leader: lost leadership (reason=%s), will retry in %s", reason, e.retryInterval)
		}

		select {
		case <-ctx.Done():
			log.Println("leader: election loop stopped")
			return
		case <-time.After(e.retryInterval):
		}
	}
}

// connAcquireTimeout bounds how long we wait for a connection from the pool.
// Prevents indefinite blocking when the pool is exhausted.
const connAcquireTimeout = 10 * time.Second

// runOnce attempts to acquire the advisory lock and hold it.
// Returns the reason leadership was lost ("" if lock was not acquired).
func (e *Elector) runOnce(ctx context.Context) string {
	// Advisory lock is session-scoped: must use a dedicated connection.
	// Use a bounded timeout to prevent indefinite blocking on pool exhaustion.
	connCtx, connCancel := context.WithTimeout(ctx, connAcquireTimeout)
	conn, err := e.db.Conn(connCtx)
	connCancel()
	if err != nil {
		log.Printf("leader: failed to acquire dedicated connection: %v", err)
		if e.metrics != nil {
			e.metrics.LeaderLost("error")
		}
		return ""
	}
	defer conn.Close()

	// Non-blocking lock attempt.
	var acquired bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", e.lockKey).Scan(&acquired)
	if err != nil {
		log.Printf("leader: advisory lock query failed: %v", err)
		if e.metrics != nil {
			e.metrics.LeaderLost("error")
		}
		return ""
	}
	if !acquired {
		log.Printf("leader: lock %d held by another instance, retrying in %s", e.lockKey, e.retryInterval)
		return ""
	}

	log.Printf("leader: acquired advisory lock %d", e.lockKey)
	if e.metrics != nil {
		e.metrics.LeaderStatusChanged(true)
		e.metrics.LeaderAcquired()
	}

	leaderCtx, cancelLeader := context.WithCancel(ctx)

	go e.onElected(leaderCtx)

	// Ping detects local connection death; it does NOT renew the lock (no TTL).
	reason := e.holdLock(ctx, conn)

	cancelLeader()

	// Recover from panics in onDemoted to prevent uncontrolled process crash.
	// A panic here would leave the advisory lock held until Postgres detects
	// the dead connection via TCP keepalive, delaying failover significantly.
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("leader: CRITICAL: onDemoted panicked: %v", r)
				reason = "panic"
			}
		}()
		e.onDemoted()
	}()

	if e.metrics != nil {
		e.metrics.LeaderStatusChanged(false)
		e.metrics.LeaderLost(reason)
	}

	log.Printf("leader: released advisory lock %d", e.lockKey)
	return reason
}

// holdLock blocks while pinging the dedicated connection.
// Returns the reason the lock was lost.
func (e *Elector) holdLock(ctx context.Context, conn *sql.Conn) string {
	ticker := time.NewTicker(e.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "shutdown"
		case <-ticker.C:
			if err := conn.PingContext(ctx); err != nil {
				if ctx.Err() != nil {
					return "shutdown"
				}
				log.Printf("leader: dedicated connection ping failed: %v", err)
				return "conn_lost"
			}
		}
	}
}
