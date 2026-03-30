package domain

import (
	"time"

	"github.com/google/uuid"
)

// TriggerEvent is emitted by the scheduler when a job fires.
type TriggerEvent struct {
	ExecutionID uuid.UUID
	JobID       uuid.UUID
	Namespace   Namespace

	ScheduledAt time.Time // intended fire time (UTC)
	FiredAt     time.Time // actual emission time

	CreatedAt time.Time
}
