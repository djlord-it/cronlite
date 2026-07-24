package webadmin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

const sessionCookieName = "cronlite_admin_session"

var errUnauthenticated = errors.New("admin authentication required")

type keyLookup interface {
	GetKeyByTokenHash(ctx context.Context, tokenHash string) (domain.APIKey, error)
}

type sessionManager struct {
	store        domain.AdminSessionRepository
	keys         keyLookup
	ttl          time.Duration
	cookieSecure bool
	now          func() time.Time
}

type authState struct {
	Session domain.AdminSession
	Key     domain.APIKey
}

func newSessionManager(
	store domain.AdminSessionRepository,
	keys keyLookup,
	ttl time.Duration,
	cookieSecure bool,
	now func() time.Time,
) *sessionManager {
	return &sessionManager{
		store:        store,
		keys:         keys,
		ttl:          ttl,
		cookieSecure: cookieSecure,
		now:          now,
	}
}

func (m *sessionManager) login(ctx context.Context, w http.ResponseWriter, plaintextAPIKey string) (domain.APIKey, error) {
	key, err := m.keys.GetKeyByTokenHash(ctx, service.HashToken(plaintextAPIKey))
	if errors.Is(err, domain.ErrAPIKeyNotFound) || (err == nil && !key.Enabled) {
		return domain.APIKey{}, errUnauthenticated
	}
	if err != nil {
		return domain.APIKey{}, err
	}

	rawSession, err := randomToken()
	if err != nil {
		return domain.APIKey{}, err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return domain.APIKey{}, err
	}

	now := m.now().UTC()
	expiresAt := now.Add(m.ttl)
	session := domain.AdminSession{
		TokenHash:  service.HashToken(rawSession),
		APIKeyID:   key.ID,
		CSRFToken:  csrfToken,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}
	if err := m.store.CreateAdminSession(ctx, session); err != nil {
		return domain.APIKey{}, err
	}

	http.SetCookie(w, newSessionCookie(rawSession, expiresAt, m.cookieSecure))
	return key, nil
}

func (m *sessionManager) authenticate(w http.ResponseWriter, r *http.Request) (authState, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return authState{}, errUnauthenticated
	}

	now := m.now().UTC()
	tokenHash := service.HashToken(cookie.Value)
	session, key, err := m.store.GetAdminSession(r.Context(), tokenHash, now)
	if errors.Is(err, domain.ErrAdminSessionNotFound) {
		clearSessionCookie(w, m.cookieSecure)
		return authState{}, errUnauthenticated
	}
	if err != nil {
		return authState{}, err
	}

	if session.ExpiresAt.Sub(now) <= m.ttl/2 {
		expiresAt := now.Add(m.ttl)
		if err := m.store.RefreshAdminSession(r.Context(), tokenHash, now, expiresAt); err != nil {
			clearSessionCookie(w, m.cookieSecure)
			return authState{}, errUnauthenticated
		}
		session.LastSeenAt = now
		session.ExpiresAt = expiresAt
		http.SetCookie(w, newSessionCookie(cookie.Value, expiresAt, m.cookieSecure))
	}

	return authState{Session: session, Key: key}, nil
}

func (m *sessionManager) logout(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		if err := m.store.DeleteAdminSession(ctx, service.HashToken(cookie.Value)); err != nil {
			return err
		}
	}
	clearSessionCookie(w, m.cookieSecure)
	return nil
}

func newSessionCookie(rawToken string, expiresAt time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    rawToken,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	}
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func validateCSRF(expected, submitted string) bool {
	if expected == "" || submitted == "" || len(expected) != len(submitted) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(submitted)) == 1
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
