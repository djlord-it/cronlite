package service

import (
	"context"
	"fmt"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
	"net/url"
	"strings"
	"time"
)

func (s *JobService) control(ctx context.Context) (domain.ControlRepository, domain.Namespace, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return nil, ns, domain.ErrNamespaceRequired
	}
	repo, ok := s.jobs.(domain.ControlRepository)
	if !ok {
		return nil, ns, fmt.Errorf("repository does not support job control center")
	}
	return repo, ns, nil
}

func (s *JobService) Fleet(ctx context.Context, filter domain.JobFilter) (domain.Fleet, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return domain.Fleet{}, err
	}
	filter.Namespace = ns
	filter.ListParams = filter.WithDefaults()
	return repo.Fleet(ctx, filter)
}
func (s *JobService) Runs(ctx context.Context, filter domain.ExecutionFilter) (domain.RunsPage, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return domain.RunsPage{}, err
	}
	// A finite window is mandatory; arbitrary historical scans are unavailable.
	if filter.Since == nil || filter.Until == nil || filter.Until.Before(*filter.Since) || filter.Until.Sub(*filter.Since) > 7*24*time.Hour {
		return domain.RunsPage{}, domain.ErrInvalidControlInput
	}
	if filter.Status != nil {
		switch *filter.Status {
		case domain.ExecutionStatusEmitted, domain.ExecutionStatusInProgress, domain.ExecutionStatusDelivered, domain.ExecutionStatusFailed:
		default:
			return domain.RunsPage{}, domain.ErrInvalidControlInput
		}
	}
	if filter.TriggerType != nil && *filter.TriggerType != "manual" && *filter.TriggerType != "scheduled" {
		return domain.RunsPage{}, domain.ErrInvalidControlInput
	}
	filter.Namespace = ns
	filter.ListParams = filter.WithDefaults()
	return repo.Runs(ctx, filter)
}
func (s *JobService) ListSavedViews(ctx context.Context, actor uuid.UUID) ([]domain.SavedView, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return nil, err
	}
	return repo.ListSavedViews(ctx, ns, actor)
}
func (s *JobService) SaveView(ctx context.Context, actor uuid.UUID, name, query string) error {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if actor == uuid.Nil || len(name) == 0 || len(name) > 80 || len(query) > 2000 {
		return domain.ErrInvalidControlInput
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return domain.ErrInvalidControlInput
	}
	for key, v := range values {
		if len(v) != 1 {
			return domain.ErrInvalidControlInput
		}
		switch key {
		case "name", "enabled", "tag", "view":
		default:
			return domain.ErrInvalidControlInput
		}
	}
	if values.Get("enabled") != "" && values.Get("enabled") != "true" && values.Get("enabled") != "false" {
		return domain.ErrInvalidControlInput
	}
	switch values.Get("view") {
	case "", "table", "board", "timeline":
	default:
		return domain.ErrInvalidControlInput
	}
	if len(values.Get("name")) > 200 || len(values.Get("tag")) > 200 {
		return domain.ErrInvalidControlInput
	}
	if tag := values.Get("tag"); tag != "" {
		parts := strings.SplitN(tag, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return domain.ErrInvalidControlInput
		}
	}
	return repo.SaveView(ctx, ns, actor, name, values.Encode())
}
func (s *JobService) DeleteView(ctx context.Context, actor, id uuid.UUID) error {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return err
	}
	return repo.DeleteView(ctx, ns, actor, id)
}
func (s *JobService) PreviewBulk(ctx context.Context, actor uuid.UUID, action string, ids []uuid.UUID) (domain.BulkBatch, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return domain.BulkBatch{}, err
	}
	if actor == uuid.Nil || (action != "pause" && action != "resume") || len(ids) < 1 || len(ids) > domain.MaxBulkJobs {
		return domain.BulkBatch{}, domain.ErrInvalidControlInput
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return domain.BulkBatch{}, domain.ErrInvalidControlInput
		}
		seen[id] = true
	}
	return repo.PreviewBulk(ctx, ns, actor, action, ids)
}
func (s *JobService) ConfirmBulk(ctx context.Context, actor, id uuid.UUID) (domain.BulkBatch, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return domain.BulkBatch{}, err
	}
	return repo.ConfirmBulk(ctx, ns, actor, id)
}
func (s *JobService) GetBulk(ctx context.Context, actor, id uuid.UUID) (domain.BulkBatch, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return domain.BulkBatch{}, err
	}
	return repo.GetBulk(ctx, ns, actor, id)
}
func (s *JobService) ListBulk(ctx context.Context, actor uuid.UUID) ([]domain.BulkBatch, error) {
	repo, ns, err := s.control(ctx)
	if err != nil {
		return nil, err
	}
	return repo.ListBulk(ctx, ns, actor)
}
