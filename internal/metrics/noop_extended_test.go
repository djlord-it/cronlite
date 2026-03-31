package metrics

import (
	"testing"
	"time"
)

func TestNewNoopSink(t *testing.T) {
	s := NewNoopSink()
	if s == nil {
		t.Fatal("expected non-nil NoopSink")
	}
}

func TestNoopSink_ZeroValues(t *testing.T) {
	s := NewNoopSink()

	// Scheduler metrics with zero/edge values
	s.TickStarted()
	s.TickCompleted(0, 0, nil)
	s.TickDrift(0)

	// Delivery metrics with zero/edge values
	s.DeliveryAttemptCompleted(0, "", 0)
	s.DeliveryOutcome("")
	s.RetryAttempt(false)

	// Flight tracking
	s.EventsInFlightIncr()
	s.EventsInFlightDecr()

	// Buffer metrics with zero values
	s.BufferSizeUpdate(0)
	s.BufferCapacitySet(0)
	s.BufferSaturationUpdate(0)
	s.EmitError()

	// Observability with zero values
	s.OrphanedExecutionsUpdate(0)
	s.ExecutionLatencyObserve(0)
}

func TestNoopSink_LargeValues(t *testing.T) {
	s := NewNoopSink()

	s.TickCompleted(24*time.Hour, 1000000, nil)
	s.TickDrift(time.Hour)
	s.DeliveryAttemptCompleted(999, StatusClass5xx, 5*time.Minute)
	s.BufferSizeUpdate(1 << 20)
	s.BufferCapacitySet(1 << 20)
	s.BufferSaturationUpdate(1.0)
	s.OrphanedExecutionsUpdate(100000)
	s.ExecutionLatencyObserve(86400.0)
}

func TestNoopSink_ImplementsSink(t *testing.T) {
	var _ Sink = (*NoopSink)(nil)
}
