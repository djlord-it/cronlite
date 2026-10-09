package domain

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var (
	ErrInvalidControlInput = errors.New("invalid control center input")
	ErrBulkNotFound        = errors.New("bulk preview not found or expired")
	ErrSavedViewLimit      = errors.New("saved view limit reached")
)

const MaxBulkJobs = 100

type Fleet struct {
	Jobs                           []JobWithSchedule
	Total, Active, Paused, Matched int
}

type RunEvidence struct {
	Execution      Execution
	JobName        string
	Attempts       int
	LastStatusCode int
	LastError      string
	LastFinishedAt time.Time
}

type RunsPage struct {
	Runs  []RunEvidence
	Total int
}

// Saved views belong to API keys, the installation's available identity model.
type SavedView struct {
	ID          uuid.UUID
	Name, Query string
	CreatedAt   time.Time
}

// BulkItem is a preview snapshot and, after confirmation, a durable audit result.
// No webhook URLs, secrets, or credentials are stored in this snapshot.
type BulkItem struct {
	ID        uuid.UUID
	Name      string
	Enabled   bool
	UpdatedAt time.Time
	Result    string
}

type BulkBatch struct {
	ID                   uuid.UUID
	Action               string
	Items                []BulkItem
	CreatedAt, ExpiresAt time.Time
	ConfirmedAt          *time.Time
}

type ControlRepository interface {
	Fleet(ctx context.Context, filter JobFilter) (Fleet, error)
	Runs(ctx context.Context, filter ExecutionFilter) (RunsPage, error)
	ListSavedViews(ctx context.Context, ns Namespace, actor uuid.UUID) ([]SavedView, error)
	SaveView(ctx context.Context, ns Namespace, actor uuid.UUID, name, query string) error
	DeleteView(ctx context.Context, ns Namespace, actor, id uuid.UUID) error
	PreviewBulk(ctx context.Context, ns Namespace, actor uuid.UUID, action string, ids []uuid.UUID) (BulkBatch, error)
	ConfirmBulk(ctx context.Context, ns Namespace, actor, id uuid.UUID) (BulkBatch, error)
	GetBulk(ctx context.Context, ns Namespace, actor, id uuid.UUID) (BulkBatch, error)
	ListBulk(ctx context.Context, ns Namespace, actor uuid.UUID) ([]BulkBatch, error)
}
