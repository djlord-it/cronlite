package service

import (
	"context"
	"errors"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
	"net/url"
	"strings"
	"testing"
	"time"
)

type controlStub struct {
	*mockJobRepo
	domain.ControlRepository
	calls     int
	namespace domain.Namespace
	actor     uuid.UUID
	filter    domain.JobFilter
	query     string
}

func (r *controlStub) Fleet(_ context.Context, f domain.JobFilter) (domain.Fleet, error) {
	r.calls++
	r.filter = f
	return domain.Fleet{}, nil
}
func (r *controlStub) Runs(_ context.Context, f domain.ExecutionFilter) (domain.RunsPage, error) {
	r.calls++
	r.namespace = f.Namespace
	return domain.RunsPage{}, nil
}
func (r *controlStub) SaveView(_ context.Context, ns domain.Namespace, actor uuid.UUID, name, query string) error {
	r.calls++
	r.namespace, r.actor, r.query = ns, actor, query
	return nil
}
func (r *controlStub) PreviewBulk(_ context.Context, ns domain.Namespace, actor uuid.UUID, action string, ids []uuid.UUID) (domain.BulkBatch, error) {
	r.calls++
	r.namespace, r.actor = ns, actor
	return domain.BulkBatch{}, nil
}
func TestControlServiceValidationAndNamespace(t *testing.T) {
	repo := &controlStub{mockJobRepo: &mockJobRepo{}}
	s := &JobService{jobs: repo}
	ctx := ctxWithNS("team-a")
	actor := uuid.New()
	id := uuid.New()
	for _, ids := range [][]uuid.UUID{nil, {id, id}, {uuid.Nil}, make([]uuid.UUID, 101)} {
		if _, err := s.PreviewBulk(ctx, actor, "pause", ids); !errors.Is(err, domain.ErrInvalidControlInput) {
			t.Fatalf("selection accepted: %v", err)
		}
	}
	for _, action := range []string{"delete", "trigger", "", "PAUSE"} {
		if _, err := s.PreviewBulk(ctx, actor, action, []uuid.UUID{id}); !errors.Is(err, domain.ErrInvalidControlInput) {
			t.Fatal(action)
		}
	}
	if _, err := s.PreviewBulk(ctx, uuid.Nil, "pause", []uuid.UUID{id}); err == nil {
		t.Fatal("nil actor")
	}
	if repo.calls != 0 {
		t.Fatal("invalid bulk inputs reached repository")
	}
	if _, err := s.PreviewBulk(ctx, actor, "resume", []uuid.UUID{id}); err != nil || repo.namespace != "team-a" || repo.actor != actor {
		t.Fatal("namespace missing")
	}
	if _, err := s.Fleet(ctx, domain.JobFilter{Namespace: "team-b", ListParams: domain.ListParams{Limit: 50000, Offset: -5}}); err != nil || repo.filter.Namespace != "team-a" || repo.filter.Limit != 1000 || repo.filter.Offset != 0 {
		t.Fatal("namespace override")
	}
	invalidQueries := []string{"redirect=https://evil.example", "name=a&name=b", "enabled=yes", "view=runs", "tag=bad", "tag==bad", "tag=bad=", "name=" + strings.Repeat("x", 201), "tag=" + strings.Repeat("x", 201), "%"}
	for _, query := range invalidQueries {
		if err := s.SaveView(ctx, actor, "Test", query); !errors.Is(err, domain.ErrInvalidControlInput) {
			t.Errorf("accepted %q: %v", query, err)
		}
	}
	for _, name := range []string{"", "  ", strings.Repeat("x", 81)} {
		if err := s.SaveView(ctx, actor, name, ""); err == nil {
			t.Fatal("invalid view name")
		}
	}
	if err := s.SaveView(ctx, actor, "Test", strings.Repeat("x", 2001)); err == nil {
		t.Fatal("query limit")
	}
	if err := s.SaveView(ctx, actor, " Test ", url.Values{"view": {"timeline"}, "enabled": {"true"}, "tag": {"env=prod"}}.Encode()); err != nil || repo.namespace != "team-a" || repo.actor != actor {
		t.Fatal("valid view rejected")
	}
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	after := now.Add(time.Hour)
	tooOld := now.Add(-8 * 24 * time.Hour)
	status := domain.ExecutionStatus("waiting")
	trigger := "api"
	for _, f := range []domain.ExecutionFilter{{}, {Since: &since}, {Since: &after, Until: &now}, {Since: &tooOld, Until: &now}, {Since: &since, Until: &now, Status: &status}, {Since: &since, Until: &now, TriggerType: &trigger}} {
		if _, err := s.Runs(ctx, f); !errors.Is(err, domain.ErrInvalidControlInput) {
			t.Fatalf("unbounded/invalid run query: %v", err)
		}
	}
	if _, err := s.Runs(ctx, domain.ExecutionFilter{Namespace: "team-b", Since: &since, Until: &now}); err != nil || repo.namespace != "team-a" {
		t.Fatal("run namespace")
	}
}
func TestEveryControlServiceOperationRequiresNamespace(t *testing.T) {
	s := &JobService{jobs: &mockJobRepo{}}
	ctx := context.Background()
	id := uuid.New()
	for _, call := range []func() error{
		func() error { _, err := s.Fleet(ctx, domain.JobFilter{}); return err }, func() error { _, err := s.Runs(ctx, domain.ExecutionFilter{}); return err },
		func() error { _, err := s.ListSavedViews(ctx, id); return err }, func() error { return s.SaveView(ctx, id, "v", "") }, func() error { return s.DeleteView(ctx, id, id) },
		func() error { _, err := s.PreviewBulk(ctx, id, "pause", []uuid.UUID{id}); return err }, func() error { _, err := s.ConfirmBulk(ctx, id, id); return err }, func() error { _, err := s.GetBulk(ctx, id, id); return err }, func() error { _, err := s.ListBulk(ctx, id); return err },
	} {
		if err := call(); !errors.Is(err, domain.ErrNamespaceRequired) {
			t.Fatal(err)
		}
	}
	if _, err := s.Fleet(ctxWithNS("team"), domain.JobFilter{}); err == nil {
		t.Fatal("unsupported repository accepted")
	}
}

func TestNextRunNeverReturnsZeroDateForImpossibleCalendar(t *testing.T) {
	repo := &mockJobRepo{getJobWithScheduleFn: func(context.Context, uuid.UUID) (domain.Job, domain.Schedule, error) {
		return domain.Job{Enabled: true}, domain.Schedule{CronExpression: "0 0 31 2 *", Timezone: "UTC"}, nil
	}}
	s := newTestService(repo)
	next, runs, _, err := s.GetNextRunTime(ctxWithNS("team"), uuid.New())
	if !errors.Is(err, domain.ErrInvalidCronExpression) || !next.IsZero() || len(runs) != 0 {
		t.Fatalf("fabricated next run: %v %#v %v", next, runs, err)
	}
}
