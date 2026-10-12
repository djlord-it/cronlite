package domain

import (
	"time"

	"github.com/google/uuid"
)

type DeliveryType string

const (
	DeliveryTypeWebhook DeliveryType = "webhook"
)

type DeliveryConfig struct {
	Type       DeliveryType
	WebhookURL string
	Secret     string // HMAC secret
	Timeout    time.Duration
}

type DeliveryAttempt struct {
	ID              uuid.UUID
	ExecutionID     uuid.UUID
	Attempt         int
	ClaimGeneration int64 // identifies the owner that observed this HTTP result

	StatusCode int
	Error      string

	StartedAt  time.Time
	FinishedAt time.Time
}
