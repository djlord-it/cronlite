package service

import (
	"context"
	"fmt"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

// ListExecutions returns executions matching the filter, scoped to the
// namespace from ctx.
func (s *JobService) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return nil, domain.ErrNamespaceRequired
	}

	filter.Namespace = ns
	filter.ListParams = filter.WithDefaults()

	return s.executions.ListExecutions(ctx, filter)
}

// ListPendingAck returns terminal executions that have not been acknowledged,
// scoped to the namespace from ctx.
func (s *JobService) ListPendingAck(ctx context.Context, jobID *uuid.UUID, limit int) ([]domain.Execution, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return nil, domain.ErrNamespaceRequired
	}

	params := domain.ListParams{Limit: limit}.WithDefaults()
	return s.executions.ListPendingAck(ctx, ns, jobID, params.Limit)
}

// AckExecution marks a pending terminal execution as acknowledged.
func (s *JobService) AckExecution(ctx context.Context, id uuid.UUID) error {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.ErrNamespaceRequired
	}

	return s.executions.AckExecution(ctx, id, ns)
}

// GetExecutionStatus retrieves one execution without loading delivery attempts.
// The public status endpoint uses this path for frequent polling.
func (s *JobService) GetExecutionStatus(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Execution{}, domain.ErrNamespaceRequired
	}

	exec, err := s.executions.GetExecutionScoped(ctx, id, ns)
	if err != nil {
		return domain.Execution{}, domain.ErrExecutionNotFound
	}
	return exec, nil
}

// GetExecution retrieves a single execution with its delivery attempts.
func (s *JobService) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, []domain.DeliveryAttempt, error) {
	exec, err := s.GetExecutionStatus(ctx, id)
	if err != nil {
		return domain.Execution{}, nil, err
	}

	attempts, err := s.attempts.GetAttempts(ctx, id)
	if err != nil {
		return domain.Execution{}, nil, fmt.Errorf("get delivery attempts: %w", err)
	}

	return exec, attempts, nil
}
