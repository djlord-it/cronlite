package webadmin

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	hasKeys, err := h.service.HasAnyAPIKeys(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if !hasKeys {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}
	if _, err := h.sessions.authenticate(w, r); err == nil {
		http.Redirect(w, r, "/admin/jobs", http.StatusSeeOther)
		return
	}
	h.renderPublicForm(w, r, "login", pageData{Title: "Sign in"})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.validPublicCSRF(r) {
		h.renderStatus(w, "login", pageData{Title: "Sign in", Error: "The form expired. Please try again.", CSRFToken: h.issuePublicCSRF(w)}, http.StatusForbidden)
		return
	}
	if _, err := h.sessions.login(r.Context(), w, r, r.FormValue("api_key")); err != nil {
		if errors.Is(err, errUnauthenticated) {
			h.renderStatus(w, "login", pageData{Title: "Sign in", Error: "Invalid API key.", CSRFToken: h.issuePublicCSRF(w)}, http.StatusUnauthorized)
			return
		}
		h.internalError(w, r, err)
		return
	}
	h.clearPublicCSRF(w)
	http.Redirect(w, r, "/admin/jobs", http.StatusSeeOther)
}

func (h *Handler) setupPage(w http.ResponseWriter, r *http.Request) {
	hasKeys, err := h.service.HasAnyAPIKeys(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if hasKeys {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	data := pageData{Title: "Set up CronLite"}
	if h.bootstrapToken == "" {
		data.Error = "Set ADMIN_BOOTSTRAP_TOKEN and restart CronLite before continuing."
	}
	h.renderPublicForm(w, r, "setup", data)
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	if !h.validPublicCSRF(r) {
		h.renderStatus(w, "setup", pageData{Title: "Set up CronLite", Error: "The form expired. Please try again.", CSRFToken: h.issuePublicCSRF(w)}, http.StatusForbidden)
		return
	}
	if h.bootstrapToken == "" || !constantTimeEqual(h.bootstrapToken, r.FormValue("bootstrap_token")) {
		h.renderStatus(w, "setup", pageData{Title: "Set up CronLite", Error: "Invalid installation token.", CSRFToken: h.issuePublicCSRF(w)}, http.StatusUnauthorized)
		return
	}
	namespace := strings.TrimSpace(r.FormValue("namespace"))
	label := strings.TrimSpace(r.FormValue("label"))
	if namespace == "" || label == "" {
		h.renderStatus(w, "setup", pageData{Title: "Set up CronLite", Error: "Namespace and key label are required.", CSRFToken: h.issuePublicCSRF(w)}, http.StatusUnprocessableEntity)
		return
	}

	result, err := h.service.BootstrapFirstAPIKey(r.Context(), namespace, label)
	if err != nil {
		if errors.Is(err, domain.ErrBootstrapAlreadyCompleted) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		h.internalError(w, r, err)
		return
	}
	if _, err := h.sessions.login(r.Context(), w, r, result.PlaintextToken); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.clearPublicCSRF(w)
	w.Header().Set("Cache-Control", "no-store")
	h.renderStatus(w, "setup_complete", pageData{
		Title: "Setup complete", Namespace: result.Key.Namespace.String(), APIKey: result.PlaintextToken,
	}, http.StatusOK)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	if !validateCSRF(auth.Session.CSRFToken, r.FormValue("csrf_token")) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := h.sessions.logout(r.Context(), w, r); err != nil {
		h.internalError(w, r, err)
		return
	}
	w.Header().Set("Clear-Site-Data", `"cache"`)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) issuePublicCSRF(w http.ResponseWriter) string {
	token, err := randomToken()
	if err != nil {
		h.logger.Printf("webadmin: generate public CSRF token: %v", err)
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: publicCSRFCookieName, Value: token, Path: "/admin",
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return token
}

func (h *Handler) validPublicCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(publicCSRFCookieName)
	return err == nil && validateCSRF(cookie.Value, r.FormValue("csrf_token"))
}

func (h *Handler) clearPublicCSRF(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: publicCSRFCookieName, Path: "/admin", HttpOnly: true,
		Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func constantTimeEqual(expected, actual string) bool {
	if expected == "" || actual == "" || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
