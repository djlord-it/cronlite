package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Execution leases are independent of webhook timeout and retry duration.
const DefaultExecutionLease = 2 * time.Minute
const OwnershipOperationTimeout = 5 * time.Second

var ErrExecutionClaimLost = errors.New("execution claim lost or expired")

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

	CreatedAt       time.Time
	ClaimedAt       *time.Time // set when dequeued, nil otherwise
	ClaimGeneration int64
	LeaseUntil      *time.Time
}
