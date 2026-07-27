//go:build integration

package webadmin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestIntegrationSessionSurvivesHandlerReconstruction(t *testing.T) {
	rt := newIntegrationRuntime(t)
	key := mustBootstrap(t, rt, "reconstruction-team")
	client := newIntegrationClient(t)

	firstServer := newIntegrationServer(t, rt)
	t.Cleanup(firstServer.Close)
	loginIntegrationClient(t, client, firstServer.URL, key.PlaintextToken)
	response, _ := integrationGet(t, client, firstServer.URL+"/admin/jobs")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first handler jobs status = %d, want 200", response.StatusCode)
	}
	firstServer.Close()

	secondServer := newIntegrationServer(t, rt)
	t.Cleanup(secondServer.Close)
	response, _ = integrationGet(t, client, secondServer.URL+"/admin/jobs")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reconstructed handler jobs status = %d, want 200", response.StatusCode)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 1 {
		t.Fatalf("persisted session count = %d, want 1", got)
	}

	t.Log("ADMIN_INTEGRATION_OK")
}

func TestIntegrationIdleAndAbsoluteExpiry(t *testing.T) {
	rt := newIntegrationRuntime(t)

	t.Run("refresh is capped at absolute expiry", func(t *testing.T) {
		rt.reset(t)
		key := mustBootstrap(t, rt, "refresh-team")
		base := time.Now().UTC().Truncate(time.Second)
		clock := &integrationClock{now: base}
		server := httptest.NewServer(newIntegrationHandler(
			t,
			rt,
			clock.Now,
			30*time.Minute,
			40*time.Minute,
		))
		t.Cleanup(server.Close)
		client := newIntegrationClient(t)
		loginIntegrationClient(t, client, server.URL, key.PlaintextToken)

		clock.Set(base.Add(20 * time.Minute))
		response, _ := integrationGet(t, client, server.URL+"/admin/jobs")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("refresh request status = %d, want 200", response.StatusCode)
		}

		var lastSeenAt, expiresAt, absoluteExpiresAt time.Time
		integrationScanRow(t, rt.db, `
SELECT last_seen_at, expires_at, absolute_expires_at FROM admin_sessions
`, nil, &lastSeenAt, &expiresAt, &absoluteExpiresAt)
		if !lastSeenAt.Equal(base.Add(20 * time.Minute)) {
			t.Fatalf("last seen = %v, want fake clock time", lastSeenAt)
		}
		if !absoluteExpiresAt.Equal(base.Add(40*time.Minute)) ||
			!expiresAt.Equal(absoluteExpiresAt) {
			t.Fatalf(
				"refresh expiry=%v absolute=%v, want both %v",
				expiresAt,
				absoluteExpiresAt,
				base.Add(40*time.Minute),
			)
		}

		var refreshedCookie *http.Cookie
		for _, cookie := range response.Cookies() {
			if cookie.Name == sessionCookieName {
				refreshedCookie = cookie
			}
		}
		if refreshedCookie == nil ||
			!refreshedCookie.Expires.Equal(absoluteExpiresAt) ||
			refreshedCookie.MaxAge != 20*60 {
			t.Fatal("browser session cookie was not capped at absolute expiry")
		}
	})

	t.Run("idle expiry rejects browser and is cleaned by next login", func(t *testing.T) {
		rt.reset(t)
		key := mustBootstrap(t, rt, "idle-team")
		server := newIntegrationServer(t, rt)
		t.Cleanup(server.Close)
		client := newIntegrationClient(t)
		loginIntegrationClient(t, client, server.URL, key.PlaintextToken)

		var expiredHash string
		integrationScanRow(
			t,
			rt.db,
			`SELECT token_hash FROM admin_sessions`,
			nil,
			&expiredHash,
		)
		integrationExec(t, rt.db, `
UPDATE admin_sessions
SET expires_at = $1, absolute_expires_at = $2
WHERE token_hash = $3
`, time.Now().UTC().Add(-time.Minute), time.Now().UTC().Add(time.Hour), expiredHash)

		response, _ := integrationGet(t, client, server.URL+"/admin/jobs")
		if response.StatusCode != http.StatusSeeOther ||
			response.Header.Get("Location") != "/admin/login" ||
			!responseClearsSessionCookie(response) {
			t.Fatal("idle-expired browser session was not rejected and cleared")
		}
		if jarHasSessionCookie(t, client, server.URL) {
			t.Fatal("cookie jar retained idle-expired browser session")
		}
		if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 1 {
			t.Fatalf("expired row count before cleanup = %d, want 1", got)
		}

		loginIntegrationClient(t, client, server.URL, key.PlaintextToken)
		if got := countRows(t, rt.db, `
SELECT COUNT(*) FROM admin_sessions WHERE token_hash = $1
`, expiredHash); got != 0 {
			t.Fatalf("idle-expired session rows remaining = %d, want 0", got)
		}
		if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 1 {
			t.Fatalf("session count after idle cleanup = %d, want 1", got)
		}
	})

	t.Run("absolute expiry rejects browser and is cleaned by next login", func(t *testing.T) {
		rt.reset(t)
		key := mustBootstrap(t, rt, "absolute-team")
		server := newIntegrationServer(t, rt)
		t.Cleanup(server.Close)
		client := newIntegrationClient(t)
		loginIntegrationClient(t, client, server.URL, key.PlaintextToken)

		var expiredHash string
		integrationScanRow(
			t,
			rt.db,
			`SELECT token_hash FROM admin_sessions`,
			nil,
			&expiredHash,
		)
		expiredAt := time.Now().UTC().Add(-time.Minute)
		integrationExec(t, rt.db, `
UPDATE admin_sessions
SET expires_at = $1, absolute_expires_at = $1
WHERE token_hash = $2
`, expiredAt, expiredHash)

		response, _ := integrationGet(t, client, server.URL+"/admin/jobs")
		if response.StatusCode != http.StatusSeeOther ||
			response.Header.Get("Location") != "/admin/login" ||
			!responseClearsSessionCookie(response) {
			t.Fatal("absolute-expired browser session was not rejected and cleared")
		}
		if jarHasSessionCookie(t, client, server.URL) {
			t.Fatal("cookie jar retained absolute-expired browser session")
		}

		loginIntegrationClient(t, client, server.URL, key.PlaintextToken)
		if got := countRows(t, rt.db, `
SELECT COUNT(*) FROM admin_sessions WHERE token_hash = $1
`, expiredHash); got != 0 {
			t.Fatalf("absolute-expired session rows remaining = %d, want 0", got)
		}
		if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 1 {
			t.Fatalf("session count after absolute cleanup = %d, want 1", got)
		}
	})

	if !t.Failed() {
		t.Log("ADMIN_INTEGRATION_OK")
	}
}

func TestIntegrationDeletingKeyRevokesSession(t *testing.T) {
	rt := newIntegrationRuntime(t)
	key := mustBootstrap(t, rt, "revocation-team")
	server := newIntegrationServer(t, rt)
	t.Cleanup(server.Close)
	client := newIntegrationClient(t)
	loginIntegrationClient(t, client, server.URL, key.PlaintextToken)

	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 1 {
		t.Fatalf("session count before key deletion = %d, want 1", got)
	}
	ctx, cancel := integrationContext(t)
	ctx = domain.NamespaceToContext(ctx, key.Key.Namespace)
	err := rt.service.DeleteAPIKey(ctx, key.Key.ID)
	cancel()
	if err != nil {
		t.Fatalf("delete API key: %v", err)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 0 {
		t.Fatalf("session count after key deletion = %d, want 0", got)
	}

	response, _ := integrationGet(t, client, server.URL+"/admin/jobs")
	if response.StatusCode != http.StatusSeeOther ||
		response.Header.Get("Location") != "/admin/login" ||
		!responseClearsSessionCookie(response) {
		t.Fatal("revoked browser session was not rejected and cleared")
	}
	if jarHasSessionCookie(t, client, server.URL) {
		t.Fatal("cookie jar retained key-revoked browser session")
	}

	t.Log("ADMIN_INTEGRATION_OK")
}
