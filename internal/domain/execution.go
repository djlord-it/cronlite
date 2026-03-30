package domain

import (
	"time"

	"github.com/google/uuid"
)

type ExecutionStatus string

const (
	ExecutionStatusEmitted    ExecutionStatus = "emitted"
	ExecutionStatusInProgress ExecutionStatus = "in_progress"
	ExecutionStatusDelivered  ExecutionStatus = "delivered"
	ExecutionStatusFailed     ExecutionStatus = "failed"
)

type TriggerType string

const (
	TriggerTypeScheduled TriggerType = "scheduled"
	TriggerTypeManual    TriggerType = "manual"
)

// Execution records that a job fired at a specific time.
type Execution struct {
	ID uuid.UUID

	JobID     uuid.UUID
	Namespace Namespace

	TriggerType    TriggerType
	ScheduledAt    time.Time
	FiredAt        time.Time
	Status         ExecutionStatus
	AcknowledgedAt *time.Time

	CreatedAt time.Time
	ClaimedAt *time.Time // set when dequeued, nil otherwise
}
