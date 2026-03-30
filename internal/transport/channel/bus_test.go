package channel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/domain"
)

func newTestEvent() domain.TriggerEvent {
	return domain.TriggerEvent{
		ExecutionID: uuid.New(),
		JobID:       uuid.New(),
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
		CreatedAt:   time.Now().UTC(),
	}
}

func TestEventBus_EmitAndReceive(t *testing.T) {
	bus := NewEventBus(10)
	event := newTestEvent()

	ctx := context.Background()
	if err := bus.Emit(ctx, event); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	select {
	case got := <-bus.Channel():
		if got.ExecutionID != event.ExecutionID {
			t.Errorf("ExecutionID = %v, want %v", got.ExecutionID, event.ExecutionID)
		}
		if got.JobID != event.JobID {
			t.Errorf("JobID = %v, want %v", got.JobID, event.JobID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event on channel")
	}
}

func TestEventBus_BufferFull(t *testing.T) {
	bus := NewEventBus(1, WithEmitTimeout(50*time.Millisecond))

	ctx := context.Background()

	// Fill the buffer
	if err := bus.Emit(ctx, newTestEvent()); err != nil {
		t.Fatalf("first Emit failed: %v", err)
	}

	// Second emit should timeout and return ErrBufferFull
	err := bus.Emit(ctx, newTestEvent())
	if err != ErrBufferFull {
		t.Errorf("expected ErrBufferFull, got: %v", err)
	}
}

func TestEventBus_ContextCancelled(t *testing.T) {
	bus := NewEventBus(1, WithEmitTimeout(5*time.Second))

	ctx := context.Background()

	// Fill the buffer
	if err := bus.Emit(ctx, newTestEvent()); err != nil {
		t.Fatalf("first Emit failed: %v", err)
	}

	// Cancel context before second emit
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := bus.Emit(cancelledCtx, newTestEvent())
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestEventBus_ConcurrentEmit(t *testing.T) {
	bus := NewEventBus(1000)
	ctx := context.Background()

	const numGoroutines = 10
	const eventsPerGoroutine = 100

	var wg sync.WaitGroup
	var emitErrors atomic.Int64

	// Consumers
	var received atomic.Int64
	done := make(chan struct{})
	go func() {
		for range bus.Channel() {
			received.Add(1)
			if received.Load() >= numGoroutines*eventsPerGoroutine {
				close(done)
				return
			}
		}
	}()

	// Producers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				if err := bus.Emit(ctx, newTestEvent()); err != nil {
					emitErrors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	// Wait for all events to be consumed
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Logf("received %d of %d events", received.Load(), numGoroutines*eventsPerGoroutine)
	}

	if emitErrors.Load() > 0 {
		t.Errorf("had %d emit errors", emitErrors.Load())
	}
}

func TestEventBus_WithEmitTimeout(t *testing.T) {
	timeout := 100 * time.Millisecond
	bus := NewEventBus(1, WithEmitTimeout(timeout))

	if bus.emitTimeout != timeout {
		t.Errorf("emitTimeout = %v, want %v", bus.emitTimeout, timeout)
	}
}

func TestEventBus_DefaultEmitTimeout(t *testing.T) {
	bus := NewEventBus(10)

	if bus.emitTimeout != DefaultEmitTimeout {
		t.Errorf("emitTimeout = %v, want %v", bus.emitTimeout, DefaultEmitTimeout)
	}
}

// mockBusMetrics tracks calls to MetricsSink methods.
type mockBusMetrics struct {
	mu                    sync.Mutex
	bufferSizeCalls       []int
	bufferCapacityCalls   []int
	bufferSaturationCalls []float64
	emitErrorCalls        int
}

func (m *mockBusMetrics) BufferSizeUpdate(size int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bufferSizeCalls = append(m.bufferSizeCalls, size)
}

func (m *mockBusMetrics) BufferCapacitySet(capacity int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bufferCapacityCalls = append(m.bufferCapacityCalls, capacity)
}

func (m *mockBusMetrics) BufferSaturationUpdate(saturation float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bufferSaturationCalls = append(m.bufferSaturationCalls, saturation)
}

func (m *mockBusMetrics) EmitError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitErrorCalls++
}

func TestEventBus_WithMetrics(t *testing.T) {
	metrics := &mockBusMetrics{}
	bus := NewEventBus(10, WithMetrics(metrics))

	// BufferCapacitySet should be called on init
	metrics.mu.Lock()
	capCalls := len(metrics.bufferCapacityCalls)
	metrics.mu.Unlock()
	if capCalls != 1 {
		t.Errorf("BufferCapacitySet should be called once on init, got %d calls", capCalls)
	}

	// Emit an event
	ctx := context.Background()
	if err := bus.Emit(ctx, newTestEvent()); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	metrics.mu.Lock()
	sizeCalls := len(metrics.bufferSizeCalls)
	satCalls := len(metrics.bufferSaturationCalls)
	metrics.mu.Unlock()

	if sizeCalls != 1 {
		t.Errorf("BufferSizeUpdate should be called once after emit, got %d", sizeCalls)
	}
	if satCalls != 1 {
		t.Errorf("BufferSaturationUpdate should be called once after emit, got %d", satCalls)
	}
}

func TestNopEmitter_ReturnsNil(t *testing.T) {
	var emitter NopEmitter
	err := emitter.Emit(context.Background(), newTestEvent())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEventBus_MetricsOnBufferFull(t *testing.T) {
	metrics := &mockBusMetrics{}
	bus := NewEventBus(1, WithEmitTimeout(50*time.Millisecond), WithMetrics(metrics))

	ctx := context.Background()

	// Fill the buffer
	bus.Emit(ctx, newTestEvent())

	// This should fail
	bus.Emit(ctx, newTestEvent())

	metrics.mu.Lock()
	errCalls := metrics.emitErrorCalls
	metrics.mu.Unlock()

	if errCalls != 1 {
		t.Errorf("EmitError should be called once on buffer full, got %d", errCalls)
	}
}
