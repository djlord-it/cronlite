package webadmin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/google/uuid"
)

type fakeAdminSessionStore struct {
	created       domain.AdminSession
	session       domain.AdminSession
	key           domain.APIKey
	createErr     error
	getErr        error
	refreshedAt   time.Time
	refreshedTill time.Time
	deletedHash   string
	operations    []string
}

func (f *fakeAdminSessionStore) CreateAdminSession(_ context.Context, session domain.AdminSession) error {
	f.created = session
	f.operations = append(f.operations, "create:"+session.TokenHash)
	return f.createErr
}

func (f *fakeAdminSessionStore) GetAdminSession(_ context.Context, _ string, _ time.Time) (domain.AdminSession, domain.APIKey, error) {
	return f.session, f.key, f.getErr
}

func (f *fakeAdminSessionStore) RefreshAdminSession(_ context.Context, _ string, lastSeenAt, expiresAt time.Time) error {
	f.refreshedAt = lastSeenAt
	f.refreshedTill = expiresAt
	return nil
}

func (f *fakeAdminSessionStore) DeleteAdminSession(_ context.Context, tokenHash string) error {
	f.deletedHash = tokenHash
	f.operations = append(f.operations, "delete:"+tokenHash)
	return nil
}

type fakeKeyLookup struct {
	wantHash string
	key      domain.APIKey
	err      error
}

func (f *fakeKeyLookup) GetKeyByTokenHash(_ context.Context, tokenHash string) (domain.APIKey, error) {
	f.wantHash = tokenHash
	return f.key, f.err
}

func TestSessionManagerLoginCreatesOpaqueCookie(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	key := domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true}
	store := &fakeAdminSessionStore{}
	keys := &fakeKeyLookup{key: key}
	manager := newSessionManager(store, keys, 30*time.Minute, 12*time.Hour, true, func() time.Time { return now })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/login", nil)

	gotKey, err := manager.login(context.Background(), rec, req, "ec_plaintext")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if gotKey.ID != key.ID {
		t.Fatalf("unexpected key: %#v", gotKey)
	}
	if keys.wantHash != service.HashToken("ec_plaintext") {
		t.Fatalf("lookup received wrong hash: %q", keys.wantHash)
	}
	if store.created.APIKeyID != key.ID || store.created.TokenHash == "" || store.created.CSRFToken == "" {
		t.Fatalf("session was not persisted correctly: %#v", store.created)
	}
	if store.created.TokenHash == "ec_plaintext" {
		t.Fatal("plaintext API key was persisted")
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one session cookie, got %d", len(cookies))
	}
	if cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Path != "/admin" {
		t.Fatalf("unsafe cookie: %#v", cookies[0])
	}
	if service.HashToken(cookies[0].Value) != store.created.TokenHash {
		t.Fatal("cookie token does not match persisted hash")
	}
}

func TestSessionManagerLoginPreservesLookupFailures(t *testing.T) {
	lookupErr := errors.New("database unavailable")
	manager := newSessionManager(
		&fakeAdminSessionStore{},
		&fakeKeyLookup{err: lookupErr},
		30*time.Minute,
		12*time.Hour,
		false,
		time.Now,
	)

	_, err := manager.login(
		context.Background(),
		httptest.NewRecorder(),
		httptest.NewRequest("POST", "/admin/login", nil),
		"ec_token",
	)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestSessionManagerAuthenticatesAndRefreshesNearExpiry(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	rawToken := "browser-session"
	store := &fakeAdminSessionStore{
		session: domain.AdminSession{
			TokenHash:         service.HashToken(rawToken),
			CSRFToken:         "csrf",
			ExpiresAt:         now.Add(5 * time.Hour),
			AbsoluteExpiresAt: now.Add(24 * time.Hour),
		},
		key: domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true},
	}
	manager := newSessionManager(store, &fakeKeyLookup{}, 12*time.Hour, 24*time.Hour, false, func() time.Time { return now })
	req := httptest.NewRequest("GET", "/admin/jobs", nil)
	req.AddCookie(newSessionCookie(rawToken, now, now.Add(5*time.Hour), false))
	rec := httptest.NewRecorder()

	auth, err := manager.authenticate(rec, req)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if auth.Key.Namespace != "team" || auth.Session.CSRFToken != "csrf" {
		t.Fatalf("unexpected auth state: %#v", auth)
	}
	if !store.refreshedAt.Equal(now) || !store.refreshedTill.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("session was not refreshed: at=%v until=%v", store.refreshedAt, store.refreshedTill)
	}
	if len(rec.Result().Cookies()) != 1 {
		t.Fatal("expected refreshed cookie")
	}
}

func TestSessionManagerRejectsMissingOrRevokedSession(t *testing.T) {
	manager := newSessionManager(
		&fakeAdminSessionStore{getErr: domain.ErrAdminSessionNotFound},
		&fakeKeyLookup{},
		30*time.Minute,
		12*time.Hour,
		false,
		time.Now,
	)
	req := httptest.NewRequest("GET", "/admin/jobs", nil)
	now := time.Now()
	req.AddCookie(newSessionCookie("expired", now, now.Add(time.Hour), false))
	rec := httptest.NewRecorder()

	if _, err := manager.authenticate(rec, req); !errors.Is(err, errUnauthenticated) {
		t.Fatalf("expected errUnauthenticated, got %v", err)
	}
	if len(rec.Result().Cookies()) != 1 || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatal("expected browser cookie to be cleared")
	}
}

func TestSessionManagerPreservesSessionStoreFailures(t *testing.T) {
	storeErr := errors.New("database unavailable")
	manager := newSessionManager(
		&fakeAdminSessionStore{getErr: storeErr},
		&fakeKeyLookup{},
		30*time.Minute,
		12*time.Hour,
		false,
		time.Now,
	)
	req := httptest.NewRequest("GET", "/admin/jobs", nil)
	now := time.Now()
	req.AddCookie(newSessionCookie("session", now, now.Add(time.Hour), false))

	_, err := manager.authenticate(httptest.NewRecorder(), req)
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected store error, got %v", err)
	}
}

func TestLoginSetsIdleAndAbsoluteExpiryFromSameClock(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	key := domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true}
	store := &fakeAdminSessionStore{}
	manager := newSessionManager(
		store,
		&fakeKeyLookup{key: key},
		30*time.Minute,
		12*time.Hour,
		false,
		func() time.Time { return now },
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/login", nil)

	if _, err := manager.login(context.Background(), rec, req, "ec_plaintext"); err != nil {
		t.Fatalf("login: %v", err)
	}

	if !store.created.ExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("idle expiry = %v, want %v", store.created.ExpiresAt, now.Add(30*time.Minute))
	}
	if !store.created.AbsoluteExpiresAt.Equal(now.Add(12 * time.Hour)) {
		t.Fatalf("absolute expiry = %v, want %v", store.created.AbsoluteExpiresAt, now.Add(12*time.Hour))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one session cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.MaxAge != 1800 {
		t.Fatalf("cookie MaxAge = %d, want 1800", cookie.MaxAge)
	}
	if cookie.Name != sessionCookieName || cookie.Path != "/admin" || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}
}

func TestRefreshIsCappedByAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	rawToken := "browser-session"
	absoluteExpiry := now.Add(10 * time.Minute)
	store := &fakeAdminSessionStore{
		session: domain.AdminSession{
			TokenHash:         service.HashToken(rawToken),
			CSRFToken:         "csrf",
			ExpiresAt:         now.Add(5 * time.Minute),
			AbsoluteExpiresAt: absoluteExpiry,
		},
		key: domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true},
	}
	manager := newSessionManager(
		store,
		&fakeKeyLookup{},
		30*time.Minute,
		12*time.Hour,
		false,
		func() time.Time { return now },
	)
	req := httptest.NewRequest("GET", "/admin/jobs", nil)
	req.AddCookie(newSessionCookie(rawToken, now, now.Add(5*time.Minute), false))
	rec := httptest.NewRecorder()

	if _, err := manager.authenticate(rec, req); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if !store.refreshedTill.Equal(absoluteExpiry) {
		t.Fatalf("refresh expiry = %v, want %v", store.refreshedTill, absoluteExpiry)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one refreshed cookie, got %d", len(cookies))
	}
	if !cookies[0].Expires.Equal(absoluteExpiry) || cookies[0].MaxAge != 600 {
		t.Fatalf("refreshed cookie was not capped: %#v", cookies[0])
	}
}

func TestLoginDeletesPresentedOldSession(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := &fakeAdminSessionStore{}
	manager := newSessionManager(
		store,
		&fakeKeyLookup{key: domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true}},
		30*time.Minute,
		12*time.Hour,
		false,
		func() time.Time { return now },
	)
	req := httptest.NewRequest("POST", "/admin/login", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "old-session"})

	if _, err := manager.login(context.Background(), httptest.NewRecorder(), req, "ec_plaintext"); err != nil {
		t.Fatalf("login: %v", err)
	}

	oldHash := service.HashToken("old-session")
	if len(store.operations) != 2 {
		t.Fatalf("operations = %#v, want delete then create", store.operations)
	}
	if store.operations[0] != "delete:"+oldHash {
		t.Fatalf("first operation = %q, want old session deletion", store.operations[0])
	}
	if store.operations[1] != "create:"+store.created.TokenHash {
		t.Fatalf("second operation = %q, want new session creation", store.operations[1])
	}
	if store.created.TokenHash == oldHash {
		t.Fatal("login reused the presented session token")
	}
}

func TestValidateCSRF(t *testing.T) {
	if !validateCSRF("expected", "expected") {
		t.Fatal("matching token should pass")
	}
	if validateCSRF("expected", "different") || validateCSRF("", "") {
		t.Fatal("missing or mismatched token should fail")
	}
}
