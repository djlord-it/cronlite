package webadmin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math"
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
	idleTTL      time.Duration
	absoluteTTL  time.Duration
	cookieSecure bool
	now          func() time.Time
	// verifyIdentity re-checks, on every request, a session whose key was
	// created by Tailscale identity sign-in. Nil rejects such sessions.
	verifyIdentity func(r *http.Request, key domain.APIKey) error
}

type authState struct {
	Session domain.AdminSession
	Key     domain.APIKey
	Theme   string // "light", "dark", or "" to follow the system
}

func newSessionManager(
	store domain.AdminSessionRepository,
	keys keyLookup,
	idleTTL time.Duration,
	absoluteTTL time.Duration,
	cookieSecure bool,
	now func() time.Time,
) *sessionManager {
	return &sessionManager{
		store:        store,
		keys:         keys,
		idleTTL:      idleTTL,
		absoluteTTL:  absoluteTTL,
		cookieSecure: cookieSecure,
		now:          now,
	}
}

func (m *sessionManager) login(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	plaintextAPIKey string,
) (domain.APIKey, error) {
	key, err := m.keys.GetKeyByTokenHash(ctx, service.HashToken(plaintextAPIKey))
	if errors.Is(err, domain.ErrAPIKeyNotFound) || (err == nil && (!key.Enabled || key.TailnetLogin != "")) {
		return domain.APIKey{}, errUnauthenticated
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	if err := m.startSession(ctx, w, r, key); err != nil {
		return domain.APIKey{}, err
	}
	return key, nil
}

// startSession replaces the request's session, if any, with a new one for key.
func (m *sessionManager) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, key domain.APIKey) error {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		if err := m.store.DeleteAdminSession(ctx, service.HashToken(cookie.Value)); err != nil {
			return err
		}
	}

	rawSession, err := randomToken()
	if err != nil {
		return err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return err
	}

	now := m.now().UTC()
	expiresAt := now.Add(m.idleTTL)
	session := domain.AdminSession{
		TokenHash:         service.HashToken(rawSession),
		APIKeyID:          key.ID,
		CSRFToken:         csrfToken,
		CreatedAt:         now,
		LastSeenAt:        now,
		ExpiresAt:         expiresAt,
		AbsoluteExpiresAt: now.Add(m.absoluteTTL),
	}
	if err := m.store.CreateAdminSession(ctx, session); err != nil {
		return err
	}

	http.SetCookie(w, newSessionCookie(rawSession, now, expiresAt, m.cookieSecure))
	return nil
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
	if key.TailnetLogin != "" && (m.verifyIdentity == nil || m.verifyIdentity(r, key) != nil) {
		if err := m.store.DeleteAdminSession(r.Context(), tokenHash); err != nil {
			return authState{}, err
		}
		clearSessionCookie(w, m.cookieSecure)
		return authState{}, errUnauthenticated
	}

	if session.ExpiresAt.Sub(now) <= m.idleTTL/2 {
		expiresAt := now.Add(m.idleTTL)
		if session.AbsoluteExpiresAt.Before(expiresAt) {
			expiresAt = session.AbsoluteExpiresAt
		}
		if err := m.store.RefreshAdminSession(r.Context(), tokenHash, now, expiresAt); err != nil {
			clearSessionCookie(w, m.cookieSecure)
			return authState{}, errUnauthenticated
		}
		session.LastSeenAt = now
		session.ExpiresAt = expiresAt
		http.SetCookie(w, newSessionCookie(cookie.Value, now, expiresAt, m.cookieSecure))
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

func newSessionCookie(rawToken string, now, expiresAt time.Time, secure bool) *http.Cookie {
	maxAge := int(math.Ceil(expiresAt.Sub(now).Seconds()))
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    rawToken,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
		MaxAge:   maxAge,
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
