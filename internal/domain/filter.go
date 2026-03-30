package domain

import (
	"time"

	"github.com/google/uuid"
)

type ListParams struct {
	Limit  int
	Offset int
}

func (p ListParams) WithDefaults() ListParams {
	if p.Limit <= 0 {
		p.Limit = 100
	}
	if p.Limit > 1000 {
		p.Limit = 1000
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

type JobFilter struct {
	Namespace Namespace
	Tags      []Tag
	Enabled   *bool
	Name      string // substring match
	ListParams
}

type ExecutionFilter struct {
	JobID       uuid.UUID
	Namespace   Namespace
	Status      *ExecutionStatus
	TriggerType *string
	Since       *time.Time
	Until       *time.Time
	ListParams
}
