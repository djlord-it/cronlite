//go:build integration

package webadmin

import (
	"context"
	"database/sql"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	postgresstore "github.com/djlord-it/cronlite/internal/store/postgres"
	_ "github.com/lib/pq"
)

const (
	integrationBootstrapToken       = "integration-bootstrap-token"
	integrationDBLockKey      int64 = 0x43524f4e4c495445
	integrationTimeout              = 5 * time.Second
	integrationLockTimeout          = 30 * time.Second
)

var (
	integrationTestsMu sync.Mutex
	csrfInputPattern   = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
)

type integrationRuntime struct {
	db      *sql.DB
	store   *postgresstore.Store
	service *service.JobService
}

type integrationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *integrationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *integrationClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func integrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), integrationTimeout)
}

func integrationDB(t *testing.T) *sql.DB {
	t.Helper()

	databaseURL := os.Getenv("ADMIN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ADMIN_TEST_DATABASE_URL is not set; skipping PostgreSQL admin integration tests")
	}

	// Every integration test truncates the same disposable database. The
	// process mutex protects tests in this binary; the PostgreSQL advisory lock
	// protects the same database from other concurrently running test binaries.
	integrationTestsMu.Lock()
	t.Cleanup(integrationTestsMu.Unlock)

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatalf("open ADMIN_TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close integration database: %v", err)
		}
	})

	pingCtx, pingCancel := integrationContext(t)
	if err := db.PingContext(pingCtx); err != nil {
		pingCancel()
		t.Fatalf("ping ADMIN_TEST_DATABASE_URL: %v", err)
	}
	pingCancel()

	lockCtx, lockCancel := context.WithTimeout(context.Background(), integrationLockTimeout)
	lockConnection, err := db.Conn(lockCtx)
	if err != nil {
		lockCancel()
		t.Fatalf("reserve integration lock connection: %v", err)
	}
	if _, err := lockConnection.ExecContext(
		lockCtx,
		`SELECT pg_advisory_lock($1)`,
		integrationDBLockKey,
	); err != nil {
		lockCancel()
		_ = lockConnection.Close()
		t.Fatalf("acquire integration database lock: %v", err)
	}
	lockCancel()
	t.Cleanup(func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer unlockCancel()
		if _, err := lockConnection.ExecContext(
			unlockCtx,
			`SELECT pg_advisory_unlock($1)`,
			integrationDBLockKey,
		); err != nil {
			t.Errorf("release integration database lock: %v", err)
		}
		if err := lockConnection.Close(); err != nil {
			t.Errorf("close integration lock connection: %v", err)
		}
	})

	truncateIntegrationTables(t, db)
	return db
}

func integrationExec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("execute integration database statement: %v", err)
	}
	return result
}

func integrationScanRow(
	t *testing.T,
	db *sql.DB,
	query string,
	args []any,
	dest ...any,
) {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	if err := db.QueryRowContext(ctx, query, args...).Scan(dest...); err != nil {
		t.Fatalf("scan integration database row: %v", err)
	}
}

func truncateIntegrationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	integrationExec(t, db, `
TRUNCATE delivery_attempts, executions, tags, jobs, schedules, admin_sessions, api_keys CASCADE
`)
}

func newIntegrationRuntime(t *testing.T) *integrationRuntime {
	t.Helper()
	db := integrationDB(t)
	store := postgresstore.New(db, integrationTimeout)
	return &integrationRuntime{
		db:    db,
		store: store,
		service: service.NewJobService(
			store,
			store,
			store,
			store,
			store,
			store,
			cron.NewParser(),
		),
	}
}

func (rt *integrationRuntime) reset(t *testing.T) {
	t.Helper()
	truncateIntegrationTables(t, rt.db)
}

func newIntegrationHandler(
	t *testing.T,
	rt *integrationRuntime,
	now func() time.Time,
	idleTTL time.Duration,
	absoluteTTL time.Duration,
) http.Handler {
	t.Helper()
	handler, err := NewHandler(Config{
		Service:            rt.service,
		Sessions:           rt.store,
		Keys:               rt.store,
		BootstrapToken:     integrationBootstrapToken,
		SessionTTL:         idleTTL,
		SessionAbsoluteTTL: absoluteTTL,
		CookieSecure:       false,
		Now:                now,
		Logger:             log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("create integration admin handler: %v", err)
	}
	return handler
}

func newIntegrationServer(t *testing.T, rt *integrationRuntime) *httptest.Server {
	t.Helper()
	return httptest.NewServer(newIntegrationHandler(
		t,
		rt,
		time.Now,
		30*time.Minute,
		12*time.Hour,
	))
}

func newIntegrationClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: integrationTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func integrationGet(t *testing.T, client *http.Client, rawURL string) (*http.Response, string) {
	t.Helper()
	response, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("GET integration admin page: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read integration admin response: %v", err)
	}
	return response, string(body)
}

func integrationPostForm(
	t *testing.T,
	client *http.Client,
	rawURL string,
	values url.Values,
) (*http.Response, string) {
	t.Helper()
	response, err := client.PostForm(rawURL, values)
	if err != nil {
		t.Fatalf("POST integration admin form: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read integration admin response: %v", err)
	}
	return response, string(body)
}

func csrfFromHTML(t *testing.T, body string) string {
	t.Helper()
	match := csrfInputPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("admin response did not contain a CSRF input")
	}
	return html.UnescapeString(match[1])
}

func mustBootstrap(
	t *testing.T,
	rt *integrationRuntime,
	namespace string,
) service.CreateAPIKeyResult {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	result, err := rt.service.BootstrapFirstAPIKey(ctx, namespace, "owner")
	if err != nil {
		t.Fatalf("bootstrap first API key: %v", err)
	}
	return result
}

func mustCreateKey(
	t *testing.T,
	rt *integrationRuntime,
	namespace domain.Namespace,
) service.CreateAPIKeyResult {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	ctx = domain.NamespaceToContext(ctx, namespace)
	result, err := rt.service.CreateAPIKey(ctx, service.CreateAPIKeyInput{Label: "owner"})
	if err != nil {
		t.Fatalf("create namespace API key: %v", err)
	}
	return result
}

func loginIntegrationClient(
	t *testing.T,
	client *http.Client,
	serverURL string,
	plaintextAPIKey string,
) {
	t.Helper()
	response, body := integrationGet(t, client, serverURL+"/admin/login")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login page status = %d, want 200", response.StatusCode)
	}
	response, _ = integrationPostForm(t, client, serverURL+"/admin/login", url.Values{
		"csrf_token": {csrfFromHTML(t, body)},
		"api_key":    {plaintextAPIKey},
	})
	if response.StatusCode != http.StatusSeeOther ||
		response.Header.Get("Location") != "/admin/jobs" {
		t.Fatalf(
			"login response = %d location %q, want 303 to /admin/jobs",
			response.StatusCode,
			response.Header.Get("Location"),
		)
	}
}

func authenticatedCSRF(
	t *testing.T,
	client *http.Client,
	serverURL string,
) string {
	t.Helper()
	response, body := integrationGet(t, client, serverURL+"/admin/jobs")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated jobs page status = %d, want 200", response.StatusCode)
	}
	return csrfFromHTML(t, body)
}

func responseClearsSessionCookie(response *http.Response) bool {
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

func jarHasSessionCookie(t *testing.T, client *http.Client, serverURL string) bool {
	t.Helper()
	target, err := url.Parse(serverURL + "/admin/jobs")
	if err != nil {
		t.Fatalf("parse integration server URL: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(target) {
		if cookie.Name == sessionCookieName {
			return true
		}
	}
	return false
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	integrationScanRow(t, db, query, args, &count)
	return count
}
