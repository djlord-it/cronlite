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

// GetExecution retrieves a single execution with its delivery attempts.
func (s *JobService) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, []domain.DeliveryAttempt, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Execution{}, nil, domain.ErrNamespaceRequired
	}

	exec, err := s.executions.GetExecutionScoped(ctx, id, ns)
	if err != nil {
		return domain.Execution{}, nil, domain.ErrExecutionNotFound
	}

	attempts, err := s.attempts.GetAttempts(ctx, id)
	if err != nil {
		return domain.Execution{}, nil, fmt.Errorf("get delivery attempts: %w", err)
	}

	return exec, attempts, nil
}
