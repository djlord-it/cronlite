//go:build integration

package webadmin

import (
	"errors"
	"testing"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

func TestIntegrationCreateJobRollsBackWhenInitialTagFails(t *testing.T) {
	rt := newIntegrationRuntime(t)
	integrationExec(t, rt.db, `
ALTER TABLE tags
ADD CONSTRAINT integration_reject_initial_tag CHECK (key <> 'reject')
`)
	t.Cleanup(func() {
		integrationExec(t, rt.db, `
ALTER TABLE tags
DROP CONSTRAINT IF EXISTS integration_reject_initial_tag
`)
	})

	ctx, cancel := integrationContext(t)
	defer cancel()
	ctx = domain.NamespaceToContext(ctx, "atomicity")

	_, _, err := rt.service.CreateJob(ctx, service.CreateJobInput{
		Name:           "must-roll-back",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
		Tags:           []domain.Tag{{Key: "reject", Value: "this tag"}},
	})

	if err == nil {
		t.Fatal("CreateJob succeeded, want tag constraint failure")
	}
	if errors.Is(err, domain.ErrInvalidCronExpression) ||
		errors.Is(err, domain.ErrInvalidTimezone) ||
		errors.Is(err, domain.ErrInvalidWebhookURL) {
		t.Fatalf("CreateJob failed before persistence: %v", err)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM jobs WHERE namespace = 'atomicity'`); got != 0 {
		t.Fatalf("jobs remaining after failed aggregate insert = %d, want 0", got)
	}
	if got := countRows(t, rt.db, `
SELECT COUNT(*)
FROM schedules s
LEFT JOIN jobs j ON j.schedule_id = s.id
WHERE j.id IS NULL
`); got != 0 {
		t.Fatalf("orphan schedules remaining after failed aggregate insert = %d, want 0", got)
	}

	t.Log("ADMIN_INTEGRATION_OK")
}
