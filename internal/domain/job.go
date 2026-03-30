package domain

import (
	"time"

	"github.com/google/uuid"
)

type Job struct {
	ID        uuid.UUID
	Namespace Namespace

	Name    string
	Enabled bool

	ScheduleID uuid.UUID
	Delivery   DeliveryConfig
	Analytics  AnalyticsConfig

	CreatedAt time.Time
	UpdatedAt time.Time
}
