package postgres

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func TestInsertFirstAPIKeyCreatesOnlyKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	key := domain.APIKey{
		ID: uuid.New(), Namespace: "first-team", TokenHash: "hash",
		Label: "owner", Enabled: true, CreatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(adminBootstrapLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM api_keys)")).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta(queryInsertAPIKey)).
		WithArgs(key.ID, "first-team", "hash", "owner", true, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	store := New(db, time.Second)
	if err := store.InsertFirstAPIKey(context.Background(), key); err != nil {
		t.Fatalf("InsertFirstAPIKey: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertFirstAPIKeyRejectsCompletedBootstrap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(adminBootstrapLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM api_keys)")).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	store := New(db, time.Second)
	err = store.InsertFirstAPIKey(context.Background(), domain.APIKey{})
	if !errors.Is(err, domain.ErrBootstrapAlreadyCompleted) {
		t.Fatalf("expected ErrBootstrapAlreadyCompleted, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHasAnyAPIKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM api_keys)")).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	store := New(db, time.Second)
	got, err := store.HasAnyAPIKeys(context.Background())
	if err != nil {
		t.Fatalf("HasAnyAPIKeys: %v", err)
	}
	if !got {
		t.Fatal("expected an API key to exist")
	}
}

func TestAdminSessionLifecycle(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	keyID := uuid.New()
	session := domain.AdminSession{
		TokenHash: "session-hash", APIKeyID: keyID, CSRFToken: "csrf",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(12 * time.Hour),
		AbsoluteExpiresAt: now.Add(12 * time.Hour),
	}

	mock.ExpectExec(regexp.QuoteMeta(queryInsertAdminSession)).
		WithArgs(
			session.TokenHash,
			keyID,
			session.CSRFToken,
			now,
			now,
			session.ExpiresAt,
			session.AbsoluteExpiresAt,
			session.CreatedAt,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	store := New(db, time.Second)
	if err := store.CreateAdminSession(context.Background(), session); err != nil {
		t.Fatalf("CreateAdminSession: %v", err)
	}

	mock.ExpectQuery(regexp.QuoteMeta(queryGetAdminSession)).
		WithArgs(session.TokenHash, now).
		WillReturnRows(sqlmock.NewRows([]string{
			"token_hash", "api_key_id", "csrf_token", "created_at", "last_seen_at", "expires_at", "absolute_expires_at",
			"id", "namespace", "token_hash", "label", "enabled", "created_at", "last_used_at",
		}).AddRow(
			session.TokenHash, keyID, session.CSRFToken, session.CreatedAt, session.LastSeenAt, session.ExpiresAt, session.AbsoluteExpiresAt,
			keyID, "first-team", "api-hash", "owner", true, now, nil,
		))

	gotSession, gotKey, err := store.GetAdminSession(context.Background(), session.TokenHash, now)
	if err != nil {
		t.Fatalf("GetAdminSession: %v", err)
	}
	if gotSession.CSRFToken != "csrf" || gotKey.Namespace != "first-team" {
		t.Fatalf("unexpected session/key: %#v %#v", gotSession, gotKey)
	}
	if !gotSession.AbsoluteExpiresAt.Equal(session.AbsoluteExpiresAt) {
		t.Fatalf("absolute expiry = %v, want %v", gotSession.AbsoluteExpiresAt, session.AbsoluteExpiresAt)
	}

	refreshedAt := now.Add(7 * time.Hour)
	refreshedExpiry := refreshedAt.Add(12 * time.Hour)
	mock.ExpectExec(regexp.QuoteMeta(queryRefreshAdminSession)).
		WithArgs(refreshedAt, refreshedExpiry, session.TokenHash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.RefreshAdminSession(context.Background(), session.TokenHash, refreshedAt, refreshedExpiry); err != nil {
		t.Fatalf("RefreshAdminSession: %v", err)
	}

	mock.ExpectExec(regexp.QuoteMeta(queryDeleteAdminSession)).
		WithArgs(session.TokenHash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.DeleteAdminSession(context.Background(), session.TokenHash); err != nil {
		t.Fatalf("DeleteAdminSession: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAdminSessionCannotExceedAbsoluteExpiry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	const expectedQuery = `
UPDATE admin_sessions
SET last_seen_at = $1, expires_at = LEAST($2, absolute_expires_at)
WHERE token_hash = $3
  AND expires_at > $1
  AND absolute_expires_at > $1
`
	mock.ExpectExec(regexp.QuoteMeta(expectedQuery)).
		WithArgs(now, now.Add(time.Hour), "session-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	store := New(db, time.Second)
	if err := store.RefreshAdminSession(context.Background(), "session-hash", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("RefreshAdminSession: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAdminSessionMapsMissingRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(queryGetAdminSession)).
		WithArgs("missing", now).
		WillReturnError(sql.ErrNoRows)

	store := New(db, time.Second)
	_, _, err = store.GetAdminSession(context.Background(), "missing", now)
	if !errors.Is(err, domain.ErrAdminSessionNotFound) {
		t.Fatalf("expected ErrAdminSessionNotFound, got %v", err)
	}
}
