package service

import (
	"context"
	"errors"
	"testing"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/google/uuid"
)

func TestListExecutions_HappyPath(t *testing.T) {
	jobID := uuid.New()
	var capturedFilter domain.ExecutionFilter
	execRepo := &mockExecutionRepo{
		listExecutionsFn: func(_ context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
			capturedFilter = filter
			return []domain.Execution{
				{ID: uuid.New(), JobID: jobID, Namespace: "t1", Status: domain.ExecutionStatusDelivered},
				{ID: uuid.New(), JobID: jobID, Namespace: "t1", Status: domain.ExecutionStatusEmitted},
			}, nil
		},
	}
	svc := newTestServiceFull(nil, nil, execRepo, nil, nil, nil)

	execs, err := svc.ListExecutions(ctxWithNS("t1"), domain.ExecutionFilter{JobID: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(execs) != 2 {
		t.Errorf("expected 2 executions, got %d", len(execs))
	}
	if capturedFilter.Namespace != "t1" {
		t.Errorf("expected namespace 't1' on filter, got %q", capturedFilter.Namespace)
	}
	if capturedFilter.Limit <= 0 {
		t.Error("expected ListParams defaults to be applied")
	}
}

func TestListExecutions_NoNamespace(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil)

	_, err := svc.ListExecutions(context.Background(), domain.ExecutionFilter{})
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestGetExecution_HappyPath(t *testing.T) {
	execID := uuid.New()
	jobID := uuid.New()
	execRepo := &mockExecutionRepo{
		getExecutionFn: func(_ context.Context, id uuid.UUID) (domain.Execution, error) {
			return domain.Execution{
				ID:        execID,
				JobID:     jobID,
				Namespace: "t1",
				Status:    domain.ExecutionStatusDelivered,
			}, nil
		},
	}
	attemptRepo := &mockAttemptRepo{
		getAttemptsFn: func(_ context.Context, id uuid.UUID) ([]domain.DeliveryAttempt, error) {
			return []domain.DeliveryAttempt{
				{ID: uuid.New(), ExecutionID: execID, Attempt: 1, StatusCode: 200},
			}, nil
		},
	}
	svc := newTestServiceFull(nil, nil, execRepo, nil, nil, attemptRepo)

	exec, attempts, err := svc.GetExecution(ctxWithNS("t1"), execID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.ID != execID {
		t.Errorf("expected execution ID %s, got %s", execID, exec.ID)
	}
	if exec.Namespace != "t1" {
		t.Errorf("expected namespace 't1', got %q", exec.Namespace)
	}
	if len(attempts) != 1 {
		t.Errorf("expected 1 attempt, got %d", len(attempts))
	}
	if attempts[0].StatusCode != 200 {
		t.Errorf("expected status code 200, got %d", attempts[0].StatusCode)
	}
}

func TestGetExecution_NoNamespace(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil)

	_, _, err := svc.GetExecution(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestGetExecution_NotFound(t *testing.T) {
	execRepo := &mockExecutionRepo{
		getExecutionFn: func(_ context.Context, id uuid.UUID) (domain.Execution, error) {
			return domain.Execution{}, errors.New("not found")
		},
	}
	svc := newTestServiceFull(nil, nil, execRepo, nil, nil, nil)

	_, _, err := svc.GetExecution(ctxWithNS("t1"), uuid.New())
	if !errors.Is(err, domain.ErrExecutionNotFound) {
		t.Errorf("expected ErrExecutionNotFound, got %v", err)
	}
}

func TestGetExecution_NamespaceMismatch(t *testing.T) {
	execID := uuid.New()
	execRepo := &mockExecutionRepo{
		getExecutionFn: func(_ context.Context, id uuid.UUID) (domain.Execution, error) {
			return domain.Execution{
				ID:        execID,
				JobID:     uuid.New(),
				Namespace: "tenant-A",
				Status:    domain.ExecutionStatusDelivered,
			}, nil
		},
	}
	svc := newTestServiceFull(nil, nil, execRepo, nil, nil, nil)

	_, _, err := svc.GetExecution(ctxWithNS("tenant-B"), execID)
	if !errors.Is(err, domain.ErrExecutionNotFound) {
		t.Errorf("expected ErrExecutionNotFound for namespace mismatch, got %v", err)
	}
}
