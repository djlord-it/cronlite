package dispatcher

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/circuitbreaker"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

type executionLookupMock struct {
	*mockStore
	lookupErr error
}

func (s *executionLookupMock) GetExecutionScoped(_ context.Context, id uuid.UUID, _ domain.Namespace) (domain.Execution, error) {
	if s.lookupErr != nil {
		return domain.Execution{}, s.lookupErr
	}
	return domain.Execution{ID: id, Status: s.getExecutionStatus(id)}, nil
}

func TestDispatcher_TerminalReplayHasNoDeliverySideEffects(t *testing.T) {
	for _, status := range []domain.ExecutionStatus{domain.ExecutionStatusDelivered, domain.ExecutionStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			store := &executionLookupMock{mockStore: newMockStore()}
			jobID, executionID := uuid.New(), uuid.New()
			store.addJob(domain.Job{ID: jobID, Namespace: "test", Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com"}})
			store.setExecutionStatus(executionID, status)
			sender := &mockSender{}
			if err := New(store, sender).Dispatch(context.Background(), domain.TriggerEvent{JobID: jobID, ExecutionID: executionID, Namespace: "test"}); err != nil {
				t.Fatal(err)
			}
			if sender.callCount() != 0 || store.getAttemptCount() != 0 || len(store.getStatusUpdates()) != 0 {
				t.Fatalf("terminal replay sent %d webhooks, wrote %d attempts and %d status updates", sender.callCount(), store.getAttemptCount(), len(store.getStatusUpdates()))
			}
		})
	}
}

func TestDispatcher_ExecutionLookupFailureDoesNotSend(t *testing.T) {
	store := &executionLookupMock{mockStore: newMockStore(), lookupErr: errors.New("database unavailable")}
	jobID := uuid.New()
	store.addJob(domain.Job{ID: jobID, Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com"}})
	sender := &mockSender{}
	if err := New(store, sender).Dispatch(context.Background(), domain.TriggerEvent{JobID: jobID}); err == nil {
		t.Fatal("expected lookup error")
	}
	if sender.callCount() != 0 {
		t.Fatal("sent webhook after execution lookup failed")
	}
}

type terminalWriteFailureStore struct{ *mockStore }

func (*terminalWriteFailureStore) UpdateExecutionStatus(context.Context, uuid.UUID, domain.ExecutionStatus) error {
	return errors.New("database unavailable")
}

func TestDispatcher_BreakerRecordsOutcomeDespiteTerminalWriteFailure(t *testing.T) {
	for _, status := range []int{200, 400} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			store := &terminalWriteFailureStore{mockStore: newMockStore()}
			jobID := uuid.New()
			const url = "https://example.com"
			store.addJob(domain.Job{ID: jobID, Delivery: domain.DeliveryConfig{WebhookURL: url}})
			breaker := circuitbreaker.New(1, 0)
			breaker.RecordFailure(url)
			results := make([]WebhookResult, circuitbreaker.DefaultHalfOpenProbes+1)
			for i := range results {
				results[i].StatusCode = status
			}
			sender := &mockSender{results: results}
			dispatcher := New(store, sender).WithCircuitBreaker(breaker)
			for i := 0; i < circuitbreaker.DefaultHalfOpenProbes+1; i++ {
				if err := dispatcher.Dispatch(context.Background(), domain.TriggerEvent{JobID: jobID, ExecutionID: uuid.New()}); err == nil {
					t.Fatalf("probe %d was refused instead of reaching the failed terminal write", i)
				}
			}
			if sender.callCount() != circuitbreaker.DefaultHalfOpenProbes+1 {
				t.Fatalf("breaker stopped permitting downstream probes: %d calls", sender.callCount())
			}
		})
	}
}

func TestDispatcher_RunReturnsWhenChannelCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan domain.TriggerEvent)
	close(ch)
	done := make(chan struct{})
	go func() {
		New(newMockStore(), &mockSender{}).Run(ctx, ch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("closed input left dispatcher running")
	}
}
