package channel

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

// ErrBufferFull is returned when the event buffer is full and emit times out.
var ErrBufferFull = errors.New("event buffer full")

// DefaultEmitTimeout is the default timeout for emit operations.
const DefaultEmitTimeout = 5 * time.Second

// MetricsSink defines the interface for recording event bus metrics.
// All methods must be non-blocking and fire-and-forget.
type MetricsSink interface {
	BufferSizeUpdate(size int)
	BufferCapacitySet(capacity int)
	BufferSaturationUpdate(saturation float64)
	EmitError()
}

type EventBus struct {
	ch          chan domain.TriggerEvent
	capacity    int
	emitTimeout time.Duration
	metrics     MetricsSink // optional, nil = disabled
}

// Option configures an EventBus.
type Option func(*EventBus)

// WithEmitTimeout sets the timeout for emit operations.
func WithEmitTimeout(d time.Duration) Option {
	return func(b *EventBus) {
		b.emitTimeout = d
	}
}

// WithMetrics attaches a metrics sink to the event bus.
func WithMetrics(sink MetricsSink) Option {
	return func(b *EventBus) {
		b.metrics = sink
	}
}

func NewEventBus(buffer int, opts ...Option) *EventBus {
	b := &EventBus{
		ch:          make(chan domain.TriggerEvent, buffer),
		capacity:    buffer,
		emitTimeout: DefaultEmitTimeout,
	}
	for _, opt := range opts {
		opt(b)
	}
	// Report initial capacity if metrics are enabled
	if b.metrics != nil {
		b.metrics.BufferCapacitySet(b.capacity)
	}
	return b
}

func (b *EventBus) Emit(ctx context.Context, event domain.TriggerEvent) error {
	timer := time.NewTimer(b.emitTimeout)
	defer timer.Stop()

	select {
	case b.ch <- event:
		// Record buffer size and saturation after successful emit
		if b.metrics != nil {
			size := len(b.ch)
			b.metrics.BufferSizeUpdate(size)
			b.metrics.BufferSaturationUpdate(float64(size) / float64(b.capacity))
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		// Buffer full: dispatcher is not consuming fast enough.
		// This event will NOT be delivered. The execution is orphaned.
		log.Printf("eventbus: buffer full, event dropped execution=%s job=%s", event.ExecutionID, event.JobID)
		if b.metrics != nil {
			b.metrics.EmitError()
		}
		return ErrBufferFull
	}
}

func (b *EventBus) Channel() <-chan domain.TriggerEvent {
	return b.ch
}

// NopEmitter is an EventEmitter that discards all events.
// Used in DB dispatch mode where the channel is not consumed.
type NopEmitter struct{}

// Emit discards the event and returns nil.
func (NopEmitter) Emit(_ context.Context, _ domain.TriggerEvent) error {
	return nil
}
