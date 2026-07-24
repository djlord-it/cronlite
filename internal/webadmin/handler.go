package webadmin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/google/uuid"
)

//go:embed templates/*.html assets/admin.css
var embeddedFiles embed.FS

const publicCSRFCookieName = "cronlite_admin_public_csrf"

type AdminBootstrapService interface {
	HasAnyAPIKeys(ctx context.Context) (bool, error)
	BootstrapFirstAPIKey(ctx context.Context, namespace, label string) (service.CreateAPIKeyResult, error)
}

type AdminJobService interface {
	ListJobsWithSchedules(ctx context.Context, filter domain.JobFilter) ([]domain.JobWithSchedule, error)
	CreateJob(ctx context.Context, input service.CreateJobInput) (domain.Job, domain.Schedule, error)
	GetJob(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, []domain.Tag, []domain.Execution, error)
	UpdateJob(ctx context.Context, id uuid.UUID, input service.UpdateJobInput) (domain.Job, domain.Schedule, error)
	DeleteJob(ctx context.Context, id uuid.UUID) error
}

type AdminJobActionService interface {
	PauseJob(ctx context.Context, id uuid.UUID) (domain.Job, error)
	ResumeJob(ctx context.Context, id uuid.UUID) (domain.Job, error)
	TriggerNow(ctx context.Context, id uuid.UUID) (domain.Execution, error)
	GetNextRunTime(ctx context.Context, id uuid.UUID) (time.Time, []time.Time, domain.Schedule, error)
}

type AdminExecutionService interface {
	ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error)
	GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, []domain.DeliveryAttempt, error)
}

type AdminService interface {
	AdminBootstrapService
	AdminJobService
	AdminJobActionService
	AdminExecutionService
}

type Config struct {
	Service        AdminService
	Sessions       domain.AdminSessionRepository
	Keys           keyLookup
	BootstrapToken string
	SessionTTL     time.Duration
	CookieSecure   bool
	Now            func() time.Time
	Logger         *log.Logger
}

type Handler struct {
	service        AdminService
	sessions       *sessionManager
	bootstrapToken string
	cookieSecure   bool
	templates      *template.Template
	css            []byte
	logger         *log.Logger
	mux            *http.ServeMux
}

type pageData struct {
	Title         string
	Namespace     string
	CSRFToken     string
	Notice        string
	Error         string
	APIKey        string
	Jobs          []domain.JobWithSchedule
	Job           domain.Job
	Schedule      domain.Schedule
	Tags          []domain.Tag
	Executions    []domain.Execution
	Execution     domain.Execution
	Attempts      []domain.DeliveryAttempt
	NextRuns      []time.Time
	Form          jobFormValues
	Edit          bool
	EnabledFilter string
	Page          int
	PreviousURL   string
	NextURL       string
}

func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Service == nil || cfg.Sessions == nil || cfg.Keys == nil {
		return nil, errors.New("webadmin requires service, session repository, and key lookup")
	}
	if cfg.SessionTTL <= 0 {
		return nil, errors.New("webadmin session TTL must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	templates, err := template.New("admin").Funcs(template.FuncMap{
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"tagsText": tagsText,
	}).ParseFS(embeddedFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}
	css, err := fs.ReadFile(embeddedFiles, "assets/admin.css")
	if err != nil {
		return nil, fmt.Errorf("read admin CSS: %w", err)
	}

	h := &Handler{
		service:        cfg.Service,
		sessions:       newSessionManager(cfg.Sessions, cfg.Keys, cfg.SessionTTL, cfg.CookieSecure, cfg.Now),
		bootstrapToken: cfg.BootstrapToken,
		cookieSecure:   cfg.CookieSecure,
		templates:      templates,
		css:            css,
		logger:         cfg.Logger,
		mux:            http.NewServeMux(),
	}
	h.routes()
	return securityHeaders(h.mux), nil
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /admin/assets/admin.css", h.serveCSS)
	h.mux.HandleFunc("GET /admin/login", h.loginPage)
	h.mux.HandleFunc("POST /admin/login", h.login)
	h.mux.HandleFunc("GET /admin/setup", h.setupPage)
	h.mux.HandleFunc("POST /admin/setup", h.setup)
	h.mux.HandleFunc("POST /admin/logout", h.logout)
	h.mux.HandleFunc("GET /admin/jobs", h.jobsPage)
	h.mux.HandleFunc("GET /admin/jobs/new", h.createJobPage)
	h.mux.HandleFunc("POST /admin/jobs/new", h.createJob)
	h.mux.HandleFunc("GET /admin/jobs/{id}", h.jobPage)
	h.mux.HandleFunc("GET /admin/jobs/{id}/edit", h.editJobPage)
	h.mux.HandleFunc("POST /admin/jobs/{id}/edit", h.editJob)
	h.mux.HandleFunc("GET /admin/jobs/{id}/delete", h.deleteJobPage)
	h.mux.HandleFunc("POST /admin/jobs/{id}/delete", h.deleteJob)
	h.mux.HandleFunc("POST /admin/jobs/{id}/pause", h.pauseJob)
	h.mux.HandleFunc("POST /admin/jobs/{id}/resume", h.resumeJob)
	h.mux.HandleFunc("POST /admin/jobs/{id}/trigger", h.triggerJob)
	h.mux.HandleFunc("GET /admin/executions/{id}", h.executionPage)
	h.mux.HandleFunc("GET /admin", h.adminRoot)
	h.mux.HandleFunc("GET /admin/", h.adminRoot)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if !strings.HasPrefix(r.URL.Path, "/admin/assets/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) serveCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(h.css)
}

func (h *Handler) adminRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin/jobs", http.StatusTemporaryRedirect)
}

func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) (authState, bool) {
	auth, err := h.sessions.authenticate(w, r)
	if err != nil {
		if errors.Is(err, errUnauthenticated) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return authState{}, false
		}
		h.internalError(w, r, err)
		return authState{}, false
	}
	ctx := domain.NamespaceToContext(r.Context(), auth.Key.Namespace)
	*r = *r.WithContext(ctx)
	return auth, true
}

func (h *Handler) requireMutation(w http.ResponseWriter, r *http.Request) (authState, bool) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return authState{}, false
	}
	if !validateCSRF(auth.Session.CSRFToken, r.FormValue("csrf_token")) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return authState{}, false
	}
	return auth, true
}

func (h *Handler) authPage(auth authState, title string) pageData {
	return pageData{
		Title: title, Namespace: auth.Key.Namespace.String(), CSRFToken: auth.Session.CSRFToken,
	}
}

func (h *Handler) renderPublicForm(w http.ResponseWriter, _ *http.Request, name string, data pageData) {
	data.CSRFToken = h.issuePublicCSRF(w)
	w.Header().Set("Cache-Control", "no-store")
	h.render(w, name, data)
}

func (h *Handler) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Printf("webadmin: render %s: %v", name, err)
	}
}

func (h *Handler) renderStatus(w http.ResponseWriter, name string, data pageData, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	h.render(w, name, data)
}

func (h *Handler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	id, tokenErr := randomToken()
	if tokenErr != nil {
		id = "unavailable"
	}
	if len(id) > 12 {
		id = id[:12]
	}
	h.logger.Printf("webadmin: request_id=%s method=%s path=%s error=%v", id, r.Method, r.URL.Path, err)
	h.renderStatus(w, "error", pageData{Title: "Something went wrong", Error: id}, http.StatusInternalServerError)
}

func (h *Handler) handleServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrJobNotFound) || errors.Is(err, domain.ErrExecutionNotFound) {
		h.renderStatus(w, "error", pageData{Title: "Not found", Error: "The requested item does not exist."}, http.StatusNotFound)
		return
	}
	if isUserError(err) {
		h.renderStatus(w, "error", pageData{Title: "Action unavailable", Error: userErrorText(err)}, http.StatusConflict)
		return
	}
	h.internalError(w, r, err)
}

func pathUUID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return uuid.Nil, false
	}
	return id, true
}

func positivePage(raw string) int {
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		return 1
	}
	return page
}

func paginationURLs(r *http.Request, page int, hasNext bool) (string, string) {
	var previous, next string
	if page > 1 {
		previous = withPage(r, page-1)
	}
	if hasNext {
		next = withPage(r, page+1)
	}
	return previous, next
}

func withPage(r *http.Request, page int) string {
	query := r.URL.Query()
	query.Set("page", strconv.Itoa(page))
	return r.URL.Path + "?" + query.Encode()
}

func tagsText(tags []domain.Tag) string {
	lines := make([]string, len(tags))
	for i, tag := range tags {
		lines[i] = tag.Key + "=" + tag.Value
	}
	return strings.Join(lines, "\n")
}

func noticeText(code string) string {
	return map[string]string{
		"created":   "Job created.",
		"updated":   "Job updated.",
		"deleted":   "Job deleted.",
		"paused":    "Job paused.",
		"resumed":   "Job resumed.",
		"triggered": "Execution queued.",
	}[code]
}

func isUserError(err error) bool {
	return errors.Is(err, domain.ErrInvalidCronExpression) ||
		errors.Is(err, domain.ErrInvalidTimezone) ||
		errors.Is(err, domain.ErrInvalidWebhookURL) ||
		errors.Is(err, domain.ErrJobDisabled)
}

func userErrorText(err error) string {
	switch {
	case errors.Is(err, domain.ErrInvalidCronExpression):
		return "The cron expression is invalid."
	case errors.Is(err, domain.ErrInvalidTimezone):
		return "The timezone is invalid."
	case errors.Is(err, domain.ErrInvalidWebhookURL):
		return "The webhook URL is invalid."
	case errors.Is(err, domain.ErrJobDisabled):
		return "Resume this job before triggering it."
	default:
		return "The action could not be completed."
	}
}
