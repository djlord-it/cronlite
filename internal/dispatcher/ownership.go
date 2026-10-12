package dispatcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

// One renewal and one independent watchdog per admitted delivery. Joining both
// retains the delivery slot if a database driver ignores cancellation.
type executionLease struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	renewCancel context.CancelFunc
	renewDone   chan struct{}
	expiryDone  chan struct{}
	changed     chan struct{}
	mu          sync.Mutex
	deadline    time.Time
	renewMu     sync.Mutex
	store       Store
	execution   domain.Execution
	duration    time.Duration
}

func newExecutionLease(parent context.Context, store Store, execution domain.Execution, claimStarted time.Time) *executionLease {
	ctx, cancel := context.WithCancelCause(parent)
	renewCtx, renewCancel := context.WithCancel(ctx)
	l := &executionLease{ctx: ctx, cancel: cancel, renewCancel: renewCancel, renewDone: make(chan struct{}), expiryDone: make(chan struct{}), changed: make(chan struct{}, 1), store: store, execution: execution, duration: store.ExecutionLeaseDuration()}
	// Request start precedes the DB grant. Never extend locally from receipt time
	// or subtract an application wall clock from a database timestamp.
	l.deadline = claimStarted.Add(l.duration)
	go l.watchExpiry()
	go func() {
		defer close(l.renewDone)
		ticker := time.NewTicker(min(l.duration/4, 30*time.Second))
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if err := l.renew(renewCtx); err != nil {
					return
				}
			}
		}
	}()
	return l
}

func (l *executionLease) watchExpiry() {
	defer close(l.expiryDone)
	for {
		l.mu.Lock()
		remaining := time.Until(l.deadline)
		if remaining <= 0 {
			l.cancel(domain.ErrExecutionClaimLost)
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		timer := time.NewTimer(remaining)
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return
		case <-l.changed:
			timer.Stop()
		case <-timer.C:
			// Recheck under the lock: a valid renewal may have changed the deadline.
		}
	}
}

// Deadlines revoke work independently of a noncooperative call returning. Normal
// cleanup detaches the callback before cancelling its operation context.
func (l *executionLease) operationContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, domain.OwnershipOperationTimeout)
	stop := context.AfterFunc(ctx, func() {
		if parent.Err() == nil {
			l.cancel(context.DeadlineExceeded)
		}
	})
	return ctx, func() {
		stop()
		// Do not depend on an asynchronously scheduled deadline callback.
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) && parent.Err() == nil {
			l.cancel(context.DeadlineExceeded)
		}
		cancel()
	}
}

func (l *executionLease) renew(parent context.Context) error {
	l.renewMu.Lock()
	defer l.renewMu.Unlock()
	if parent.Err() != nil {
		return context.Cause(parent)
	}
	started := time.Now()
	ctx, done := l.operationContext(parent)
	err := l.store.RenewExecutionClaim(ctx, l.execution)
	done()
	if err != nil {
		if parent.Err() == nil {
			l.cancel(fmt.Errorf("execution ownership uncertain: %w", err))
		}
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return context.Cause(l.ctx)
	}
	if !time.Now().Before(l.deadline) {
		l.cancel(domain.ErrExecutionClaimLost)
		return domain.ErrExecutionClaimLost
	}
	l.deadline = started.Add(l.duration)
	select {
	case l.changed <- struct{}{}:
	default:
	}
	return nil
}

func (l *executionLease) stopRenewal() { l.renewCancel(); <-l.renewDone }

func (l *executionLease) close() {
	l.cancel(context.Canceled)
	l.stopRenewal()
	<-l.expiryDone
}

func (l *executionLease) complete(status domain.ExecutionStatus) error {
	l.stopRenewal()
	if l.ctx.Err() != nil {
		return context.Cause(l.ctx)
	}
	ctx, done := l.operationContext(l.ctx)
	err := l.store.CompleteExecution(ctx, l.execution, status)
	done()
	if l.ctx.Err() != nil {
		return context.Cause(l.ctx)
	}
	return err
}
