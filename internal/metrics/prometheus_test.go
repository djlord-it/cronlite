package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func newTestSink(t *testing.T) (*PrometheusSink, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	sink := NewPrometheusSink(reg)
	return sink, reg
}

func getCounterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			for _, m := range mf.GetMetric() {
				if m.GetCounter() != nil {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func getGaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			for _, m := range mf.GetMetric() {
				if m.GetGauge() != nil {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return 0
}

func getCounterVecValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			for _, m := range mf.GetMetric() {
				if matchLabels(m.GetLabel(), labels) {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func matchLabels(pairs []*dto.LabelPair, want map[string]string) bool {
	if len(pairs) != len(want) {
		return false
	}
	for _, p := range pairs {
		if v, ok := want[p.GetName()]; !ok || v != p.GetValue() {
			return false
		}
	}
	return true
}

func TestPrometheusSink_Registration(t *testing.T) {
	// Should not panic or error with a fresh registry.
	reg := prometheus.NewRegistry()
	sink := NewPrometheusSink(reg)
	if sink == nil {
		t.Fatal("NewPrometheusSink returned nil")
	}
}

func TestPrometheusSink_TickStarted(t *testing.T) {
	sink, reg := newTestSink(t)

	sink.TickStarted()
	sink.TickStarted()

	val := getCounterValue(t, reg, "cronlite_scheduler_ticks_total")
	if val != 2 {
		t.Errorf("ticks_total = %v, want 2", val)
	}
}

func TestPrometheusSink_TickCompleted_WithError(t *testing.T) {
	sink, reg := newTestSink(t)

	// No error
	sink.TickCompleted(100*time.Millisecond, 5, nil)
	errCount := getCounterValue(t, reg, "cronlite_scheduler_tick_errors_total")
	if errCount != 0 {
		t.Errorf("tick_errors_total = %v after success, want 0", errCount)
	}

	// With error
	sink.TickCompleted(100*time.Millisecond, 0, errors.New("db error"))
	errCount = getCounterValue(t, reg, "cronlite_scheduler_tick_errors_total")
	if errCount != 1 {
		t.Errorf("tick_errors_total = %v after error, want 1", errCount)
	}
}

func TestPrometheusSink_DeliveryAttemptLabels(t *testing.T) {
	sink, reg := newTestSink(t)

	sink.DeliveryAttemptCompleted(1, "2xx", 100*time.Millisecond)
	sink.DeliveryAttemptCompleted(2, "5xx", 200*time.Millisecond)

	val1 := getCounterVecValue(t, reg, "cronlite_dispatcher_delivery_attempts_total",
		map[string]string{"attempt": "1", "status_class": "2xx"})
	if val1 != 1 {
		t.Errorf("attempt=1,status=2xx = %v, want 1", val1)
	}

	val2 := getCounterVecValue(t, reg, "cronlite_dispatcher_delivery_attempts_total",
		map[string]string{"attempt": "2", "status_class": "5xx"})
	if val2 != 1 {
		t.Errorf("attempt=2,status=5xx = %v, want 1", val2)
	}
}

func TestPrometheusSink_DeliveryOutcome(t *testing.T) {
	sink, reg := newTestSink(t)

	sink.DeliveryOutcome(OutcomeSuccess)
	sink.DeliveryOutcome(OutcomeFailed)
	sink.DeliveryOutcome(OutcomeSuccess)

	successVal := getCounterVecValue(t, reg, "cronlite_dispatcher_delivery_outcomes_total",
		map[string]string{"outcome": "success"})
	if successVal != 2 {
		t.Errorf("outcome=success = %v, want 2", successVal)
	}

	failedVal := getCounterVecValue(t, reg, "cronlite_dispatcher_delivery_outcomes_total",
		map[string]string{"outcome": "failed"})
	if failedVal != 1 {
		t.Errorf("outcome=failed = %v, want 1", failedVal)
	}
}

func TestPrometheusSink_EventsInFlight(t *testing.T) {
	sink, reg := newTestSink(t)

	sink.EventsInFlightIncr()
	sink.EventsInFlightIncr()
	sink.EventsInFlightDecr()

	val := getGaugeValue(t, reg, "cronlite_dispatcher_events_in_flight")
	if val != 1 {
		t.Errorf("events_in_flight = %v, want 1", val)
	}
}

func TestPrometheusSink_BufferMetrics(t *testing.T) {
	sink, reg := newTestSink(t)

	sink.BufferCapacitySet(100)
	sink.BufferSizeUpdate(42)
	sink.BufferSaturationUpdate(0.42)

	capVal := getGaugeValue(t, reg, "cronlite_eventbus_buffer_capacity")
	if capVal != 100 {
		t.Errorf("buffer_capacity = %v, want 100", capVal)
	}

	sizeVal := getGaugeValue(t, reg, "cronlite_eventbus_buffer_size")
	if sizeVal != 42 {
		t.Errorf("buffer_size = %v, want 42", sizeVal)
	}

	satVal := getGaugeValue(t, reg, "cronlite_eventbus_buffer_saturation")
	if satVal != 0.42 {
		t.Errorf("buffer_saturation = %v, want 0.42", satVal)
	}
}

func TestPrometheusSink_DuplicateRegistration_NoPanic(t *testing.T) {
	// Registering metrics twice with the same registry should not panic.
	// The second registration will fail, but should be handled gracefully.
	reg := prometheus.NewRegistry()

	sink1 := NewPrometheusSink(reg)
	if sink1 == nil {
		t.Fatal("first NewPrometheusSink returned nil")
	}

	// Second registration will fail for all metrics, but should not panic.
	sink2 := NewPrometheusSink(reg)
	if sink2 == nil {
		t.Fatal("second NewPrometheusSink returned nil")
	}
}

// Verify PrometheusSink implements Sink interface.
var _ Sink = (*PrometheusSink)(nil)
