package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AdminSession is a revocable browser session linked to an API key.
// TokenHash is persisted; the plaintext token exists only in the cookie.
type AdminSession struct {
	TokenHash  string
	APIKeyID   uuid.UUID
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type AdminSessionRepository interface {
	CreateAdminSession(ctx context.Context, session AdminSession) error
	GetAdminSession(ctx context.Context, tokenHash string, now time.Time) (AdminSession, APIKey, error)
	RefreshAdminSession(ctx context.Context, tokenHash string, lastSeenAt, expiresAt time.Time) error
	DeleteAdminSession(ctx context.Context, tokenHash string) error
}
