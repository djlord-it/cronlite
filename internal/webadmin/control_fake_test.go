package webadmin

import (
	"context"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func (f *fakeAdminService) Fleet(ctx context.Context, filter domain.JobFilter) (domain.Fleet, error) {
	f.listFilter = filter
	if f.fleet != nil {
		return *f.fleet, f.err
	}
	result := domain.Fleet{Jobs: f.jobs, Total: len(f.jobs), Matched: len(f.jobs) + filter.Offset}
	for i := range f.jobs {
		row := &f.jobs[i]
		if row.Job.Enabled {
			result.Active++
		} else {
			result.Paused++
		}
	}
	return result, f.err
}
func (f *fakeAdminService) Runs(_ context.Context, filter domain.ExecutionFilter) (domain.RunsPage, error) {
	f.executionFilter = filter
	return f.runs, f.controlErr
}
func (f *fakeAdminService) ListSavedViews(_ context.Context, actor uuid.UUID) ([]domain.SavedView, error) {
	f.actor = actor
	return f.views, f.controlErr
}
func (f *fakeAdminService) SaveView(_ context.Context, actor uuid.UUID, name, query string) error {
	f.actor, f.savedName, f.savedQuery = actor, name, query
	return f.controlErr
}
func (f *fakeAdminService) DeleteView(_ context.Context, actor, id uuid.UUID) error {
	f.actor, f.actionID = actor, id
	return f.controlErr
}
func (f *fakeAdminService) PreviewBulk(_ context.Context, actor uuid.UUID, action string, ids []uuid.UUID) (domain.BulkBatch, error) {
	f.actor, f.bulkAction, f.selectedIDs = actor, action, ids
	return f.batch, f.controlErr
}
func (f *fakeAdminService) ConfirmBulk(_ context.Context, actor, id uuid.UUID) (domain.BulkBatch, error) {
	f.actor, f.actionID, f.confirmed = actor, id, true
	return f.batch, f.controlErr
}
func (f *fakeAdminService) GetBulk(_ context.Context, actor, id uuid.UUID) (domain.BulkBatch, error) {
	f.actor, f.actionID = actor, id
	return f.batch, f.controlErr
}
func (f *fakeAdminService) ListBulk(_ context.Context, actor uuid.UUID) ([]domain.BulkBatch, error) {
	f.actor = actor
	return f.batches, f.controlErr
}
