package webadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

func (h *Handler) keysPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	data := h.authPage(auth, "API keys")
	data.CurrentKeyID = auth.Key.ID
	data.Page = positivePage(r.URL.Query().Get("page"))
	keys, err := h.service.ListAPIKeys(r.Context(), domain.ListParams{Limit: 26, Offset: (data.Page - 1) * 25})
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	hasNext := len(keys) > 25
	if hasNext {
		keys = keys[:25]
	}
	data.APIKeys = keys
	data.PreviousURL, data.NextURL = paginationURLs(r, data.Page, hasNext)
	if r.URL.Query().Get("notice") == "deleted" {
		data.Notice = "API key revoked. Sessions using it can no longer sign in."
	}
	h.render(w, "keys", data)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" || len([]rune(label)) > 128 {
		data := h.authPage(auth, "Create API key")
		data.Error = "Enter a key label of 1–128 characters."
		data.KeyLabel = label
		h.renderStatus(w, "key_new", data, http.StatusUnprocessableEntity)
		return
	}
	result, err := h.service.CreateAPIKey(r.Context(), service.CreateAPIKeyInput{Label: label})
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	data := h.authPage(auth, "API key created")
	data.APIKey = result.PlaintextToken
	data.KeyLabel = result.Key.Label
	h.render(w, "key_created", data)
}

func (h *Handler) deleteKeyPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	// Look up metadata only inside this session's namespace. Never reveal another workspace's key.
	for offset := 0; ; offset += 1000 {
		keys, err := h.service.ListAPIKeys(r.Context(), domain.ListParams{Limit: 1000, Offset: offset})
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		for _, key := range keys {
			if key.ID == id {
				data := h.authPage(auth, "Revoke API key")
				data.SelectedKey = key
				data.CurrentKeyID = auth.Key.ID
				h.render(w, "key_delete", data)
				return
			}
		}
		if len(keys) < 1000 {
			break
		}
	}
	h.handleServiceError(w, r, domain.ErrAPIKeyNotFound)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if id == auth.Key.ID {
		data := h.authPage(auth, "Key in use")
		data.Error = "Sign in with another key before revoking your current key. Create a replacement in API keys first."
		h.renderStatus(w, "error", data, http.StatusConflict)
		return
	}
	if err := h.service.DeleteAPIKey(r.Context(), id); err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/keys?notice=deleted", http.StatusSeeOther)
}

func (h *Handler) onboardingPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	h.render(w, "onboarding", h.authPage(auth, "Welcome to your workspace"))
}
func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	data := h.authPage(auth, "Settings")
	data.RuntimeSettings = h.runtimeSettings
	data.SessionTTL = h.sessions.idleTTL
	data.SessionAbsoluteTTL = h.sessions.absoluteTTL
	data.CookieSecure = h.cookieSecure
	h.render(w, "settings", data)
}
func (h *Handler) schedulePage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	data := h.authPage(auth, "Schedule builder")
	data.Form.Timezone = "UTC"
	h.render(w, "schedule", data)
}
func (h *Handler) resolveSchedule(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	data := h.authPage(auth, "Schedule builder")
	data.Description = strings.TrimSpace(r.FormValue("description"))
	data.Form.Timezone = strings.TrimSpace(r.FormValue("timezone"))
	result, err := h.service.ResolveSchedule(r.Context(), data.Description, data.Form.Timezone)
	if err != nil {
		if errors.Is(err, domain.ErrScheduleParseFailure) || errors.Is(err, domain.ErrInvalidTimezone) || errors.Is(err, domain.ErrInvalidCronExpression) {
			data.Error = "Enter a valid timezone and a schedule such as every 15 minutes, daily at 9am, or 0 9 * * *."
			h.renderStatus(w, "schedule", data, http.StatusUnprocessableEntity)
			return
		}
		h.internalError(w, r, err)
		return
	}
	data.Resolved = result
	h.render(w, "schedule", data)
}

func (h *Handler) executionsPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	data := h.authPage(auth, "Executions")
	data.Page = positivePage(r.URL.Query().Get("page"))
	data.ExecutionStatusFilter = r.URL.Query().Get("status")
	data.TriggerTypeFilter = r.URL.Query().Get("trigger_type")
	filter := domain.ExecutionFilter{ListParams: domain.ListParams{Limit: 26, Offset: (data.Page - 1) * 25}}
	switch data.ExecutionStatusFilter {
	case "emitted", "in_progress", "delivered", "failed":
		status := domain.ExecutionStatus(data.ExecutionStatusFilter)
		filter.Status = &status
	case "pending":
		data.Pending = true
	}
	switch data.TriggerTypeFilter {
	case "scheduled", "manual":
		filter.TriggerType = &data.TriggerTypeFilter
	}
	var executions []domain.Execution
	var err error
	if data.Pending {
		data.Page = 1
		executions, err = h.service.ListPendingAck(r.Context(), nil, 1000)
	} else {
		executions, err = h.service.ListExecutions(r.Context(), filter)
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	hasNext := !data.Pending && len(executions) > 25
	if hasNext {
		executions = executions[:25]
	}
	data.Executions = executions
	data.PreviousURL, data.NextURL = paginationURLs(r, data.Page, hasNext)
	if r.URL.Query().Get("notice") == "acknowledged" {
		data.Notice = "Execution acknowledged."
	}
	h.render(w, "executions", data)
}
func (h *Handler) ackExecution(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireMutation(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if err := h.service.AckExecution(r.Context(), id); err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/executions?status=pending&notice=acknowledged", http.StatusSeeOther)
}

// Keep compile-time enforcement of the full UI service contract.
var _ AdminService = (*service.JobService)(nil)
