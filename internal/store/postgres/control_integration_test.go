package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func TestControlRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CRONLITE_TEST_DATABASE_URL is required")
	}
	db, cleanup := openIsolatedPostgresSchema(t, dsn)
	defer cleanup()
	migration, err := os.ReadFile("../../../schema/010_job_control_center.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	store := New(db, 5*time.Second)
	ctx := context.Background()
	actor := domain.APIKey{ID: uuid.New(), Namespace: "control-a", TokenHash: "test-owner-hash", Enabled: true, CreatedAt: time.Now()}
	other := domain.APIKey{ID: uuid.New(), Namespace: "control-b", TokenHash: "test-other-hash", Enabled: true, CreatedAt: time.Now()}
	for _, key := range []domain.APIKey{actor, other} {
		if err = store.InsertAPIKey(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	create := func(ns domain.Namespace, name string, enabled bool) domain.Job {
		t.Helper()
		now := time.Now().UTC()
		schedule := domain.Schedule{ID: uuid.New(), CronExpression: "*/5 * * * *", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
		job := domain.Job{ID: uuid.New(), Namespace: ns, Name: name, Enabled: enabled, ScheduleID: schedule.ID, Delivery: domain.DeliveryConfig{Type: domain.DeliveryTypeWebhook, WebhookURL: "https://example.com/hook", Secret: "private", Timeout: time.Second}, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateJobAggregate(ctx, job, schedule, []domain.Tag{{Key: "team", Value: "payments"}}); err != nil {
			t.Fatal(err)
		}
		return job
	}
	active := create(actor.Namespace, "Invoice %_ reconciliation", true)
	paused := create(actor.Namespace, "Invoice paused", false)
	foreign := create(other.Namespace, "Invoice foreign", true)
	fleet, err := store.Fleet(ctx, domain.JobFilter{Namespace: actor.Namespace, Name: "INVOICE", Tags: []domain.Tag{{Key: "team", Value: "payments"}}})
	if err != nil || fleet.Total != 2 || fleet.Active != 1 || fleet.Paused != 1 || fleet.Matched != 2 || len(fleet.Jobs) != 2 {
		t.Fatalf("inaccurate fleet: %+v %v", fleet, err)
	}
	fleet, err = store.Fleet(ctx, domain.JobFilter{Namespace: actor.Namespace, Name: "%_"})
	if err != nil || fleet.Matched != 1 || fleet.Jobs[0].Job.ID != active.ID {
		t.Fatal("search was not literal", err)
	}
	fleet, err = store.Fleet(ctx, domain.JobFilter{Namespace: actor.Namespace, ListParams: domain.ListParams{Limit: 25, Offset: 25}})
	if err != nil || fleet.Matched != 2 || len(fleet.Jobs) != 0 {
		t.Fatal("empty page lost counts", err)
	}
	if _, err = store.PreviewBulk(ctx, actor.Namespace, other.ID, "pause", []uuid.UUID{active.ID}); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatal("foreign actor accepted", err)
	}
	if _, err = store.PreviewBulk(ctx, actor.Namespace, actor.ID, "pause", []uuid.UUID{foreign.ID}); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatal("foreign job accepted", err)
	}
	preview, err := store.PreviewBulk(ctx, actor.Namespace, actor.ID, "pause", []uuid.UUID{active.ID, paused.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ConfirmBulk(ctx, actor.Namespace, actor.ID, preview.ID)
	if err != nil || result.ConfirmedAt == nil {
		t.Fatal("confirmation not audited", err)
	}
	results := map[uuid.UUID]string{}
	for _, item := range result.Items {
		results[item.ID] = item.Result
	}
	if results[active.ID] != "applied" || results[paused.ID] != "unchanged" {
		t.Fatal(results)
	}
	replay, err := store.ConfirmBulk(ctx, actor.Namespace, actor.ID, preview.ID)
	if err != nil || !replay.ConfirmedAt.Equal(*result.ConfirmedAt) {
		t.Fatal("confirmation not idempotent", err)
	}
	if _, err = store.GetBulk(ctx, other.Namespace, other.ID, preview.ID); !errors.Is(err, domain.ErrBulkNotFound) {
		t.Fatal("foreign audit visible", err)
	}
	if err = store.SaveView(ctx, actor.Namespace, actor.ID, "Payments", "tag=team%3Dpayments"); err != nil {
		t.Fatal(err)
	}
	views, err := store.ListSavedViews(ctx, other.Namespace, other.ID)
	if err != nil || len(views) != 0 {
		t.Fatal("foreign saved view visible", err)
	}
}
