package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Namespace string

func (n Namespace) IsZero() bool {
	return n == ""
}

func (n Namespace) String() string {
	return string(n)
}

type contextKey struct{}

func NamespaceFromContext(ctx context.Context) Namespace {
	ns, _ := ctx.Value(contextKey{}).(Namespace)
	return ns
}

func NamespaceToContext(ctx context.Context, ns Namespace) context.Context {
	return context.WithValue(ctx, contextKey{}, ns)
}

type APIKey struct {
	ID         uuid.UUID
	Namespace  Namespace
	TokenHash  string
	Label      string
	Enabled    bool
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// TailnetLogin is the Tailscale login this key was created for by
	// identity sign-in to the admin UI; empty for ordinary API keys.
	TailnetLogin string
}
