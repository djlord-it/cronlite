package webadmin

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

type timelineEntry struct {
	Job      domain.Job
	Schedule domain.Schedule
	Next     time.Time
}
type runColumn struct {
	Status string
	Label  string
	Runs   []domain.RunEvidence
}

func controlPage(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p < 1 || p > 10000 {
		return 0, domain.ErrInvalidControlInput
	}
	return p, nil
}
func fleetQuery(q url.Values) (int, domain.JobFilter, string, error) {
	f := domain.JobFilter{Name: strings.TrimSpace(q.Get("name"))}
	p, err := controlPage(q.Get("page"))
	if err != nil {
		return 0, f, "", err
	}
	for _, k := range []string{"name", "enabled", "tag", "view", "page"} {
		if len(q[k]) > 1 {
			return 0, f, "", domain.ErrInvalidControlInput
		}
	}
	if len(f.Name) > 200 || len(q.Get("tag")) > 200 || !utf8.ValidString(f.Name) || !utf8.ValidString(q.Get("tag")) || strings.ContainsRune(f.Name, 0) || strings.ContainsRune(q.Get("tag"), 0) {
		return 0, f, "", domain.ErrInvalidControlInput
	}
	switch q.Get("enabled") {
	case "":
	case "true":
		v := true
		f.Enabled = &v
	case "false":
		v := false
		f.Enabled = &v
	default:
		return 0, f, "", domain.ErrInvalidControlInput
	}
	if tag := q.Get("tag"); tag != "" {
		parts := strings.SplitN(tag, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return 0, f, "", domain.ErrInvalidControlInput
		}
		f.Tags = []domain.Tag{{Key: parts[0], Value: parts[1]}}
	}
	view := q.Get("view")
	if view == "" {
		view = "table"
	}
	switch view {
	case "table", "board", "timeline":
	default:
		return 0, f, "", domain.ErrInvalidControlInput
	}
	f.ListParams = domain.ListParams{Limit: 26, Offset: (p - 1) * 25}
	return p, f, view, nil
}
func savedQuery(q url.Values) string {
	v := url.Values{}
	for _, k := range []string{"name", "enabled", "tag", "view"} {
		if value := q.Get(k); value != "" {
			v.Set(k, value)
		}
	}
	return v.Encode()
}
func fleetState(r *http.Request) string {
	q, _ := url.ParseQuery(savedQuery(r.URL.Query()))
	if p := r.URL.Query().Get("page"); p != "" {
		q.Set("page", p)
	}
	return "/admin/jobs?" + q.Encode()
}
func viewURL(r *http.Request, view string) string {
	q := r.URL.Query()
	q.Set("view", view)
	q.Del("notice")
	if view == "timeline" {
		q.Del("page")
	}
	return r.URL.Path + "?" + q.Encode()
}
func safeReturn(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || len(raw) > 2500 {
		return "/admin/jobs"
	}
	switch u.Path {
	case "/admin/jobs":
		if _, _, _, err = fleetQuery(u.Query()); err != nil {
			return "/admin/jobs"
		}
		q, _ := url.ParseQuery(savedQuery(u.Query()))
		if p := u.Query().Get("page"); p != "" {
			q.Set("page", p)
		}
		u.RawQuery = q.Encode()
	case "/admin/runs":
		if _, _, _, _, err = runsQuery(u.Query(), time.Now()); err != nil {
			return "/admin/jobs"
		}
		q := url.Values{}
		for _, k := range []string{"page", "status", "trigger_type", "window", "view"} {
			if v := u.Query().Get(k); v != "" {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
	default:
		return "/admin/jobs"
	}
	return u.String()
}
func returnQuery(state string) string { return "return=" + url.QueryEscape(safeReturn(state)) }
func jobURL(id uuid.UUID, state string) string {
	return "/admin/jobs/" + id.String() + "?" + returnQuery(state)
}
func executionURL(id uuid.UUID, state string) string {
	return "/admin/executions/" + id.String() + "?" + returnQuery(state)
}
func actionRedirect(id uuid.UUID, notice, state string) string {
	path := "/admin/jobs/" + id.String() + "?notice=" + notice
	if state != "" {
		path += "&" + returnQuery(state)
	}
	return path
}
func deleteRedirect(state string) string {
	if state == "" {
		return "/admin/jobs?notice=deleted"
	}
	return safeReturn(state)
}

func (h *Handler) fillTimeline(data *pageData) {
	now := h.sessions.now()
	data.TimelineUntil = now.Add(24 * time.Hour)
	jobs := data.Jobs
	if len(jobs) > 500 {
		data.TimelineLimited = true
		jobs = jobs[:500]
	}
	parser := cron.NewParser()
	for i := range jobs {
		row := &jobs[i]
		if !row.Job.Enabled {
			continue
		}
		schedule, err := parser.Parse(row.Schedule.CronExpression, row.Schedule.Timezone)
		if err != nil {
			data.TimelineInvalid++
			continue
		}
		next := schedule.Next(now)
		if next.IsZero() {
			data.TimelineInvalid++
			continue
		}
		if next.After(data.TimelineUntil) {
			continue
		}
		data.Timeline = append(data.Timeline, timelineEntry{Job: row.Job, Schedule: row.Schedule, Next: next})
	}
	sort.Slice(data.Timeline, func(i, j int) bool {
		if data.Timeline[i].Next.Equal(data.Timeline[j].Next) {
			return data.Timeline[i].Job.ID.String() < data.Timeline[j].Job.ID.String()
		}
		return data.Timeline[i].Next.Before(data.Timeline[j].Next)
	})
	if len(data.Timeline) > 50 {
		data.TimelineLimited = true
		data.Timeline = data.Timeline[:50]
	}
}
func runsQuery(q url.Values, now time.Time) (int, domain.ExecutionFilter, string, string, error) {
	f := domain.ExecutionFilter{}
	p, err := controlPage(q.Get("page"))
	if err != nil {
		return 0, f, "", "", err
	}
	for _, key := range []string{"page", "status", "trigger_type", "window", "view"} {
		if len(q[key]) > 1 {
			return 0, f, "", "", domain.ErrInvalidControlInput
		}
	}
	status := domain.ExecutionStatus(q.Get("status"))
	switch status {
	case "":
	case domain.ExecutionStatusEmitted, domain.ExecutionStatusInProgress, domain.ExecutionStatusDelivered, domain.ExecutionStatusFailed:
		f.Status = &status
	default:
		return 0, f, "", "", domain.ErrInvalidControlInput
	}
	trigger := q.Get("trigger_type")
	switch trigger {
	case "":
	case "scheduled", "manual":
		f.TriggerType = &trigger
	default:
		return 0, f, "", "", domain.ErrInvalidControlInput
	}
	window := q.Get("window")
	duration := 24 * time.Hour
	switch window {
	case "", "24h":
		window = "24h"
	case "7d":
		duration = 7 * 24 * time.Hour
	default:
		return 0, f, "", "", domain.ErrInvalidControlInput
	}
	view := q.Get("view")
	switch view {
	case "", "board":
		view = "board"
	case "table":
	default:
		return 0, f, "", "", domain.ErrInvalidControlInput
	}
	since := now.Add(-duration)
	f.Since, f.Until = &since, &now
	f.ListParams = domain.ListParams{Limit: 26, Offset: (p - 1) * 25}
	return p, f, window, view, nil
}
func (h *Handler) runsPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	page, filter, window, view, err := runsQuery(r.URL.Query(), h.sessions.now())
	if err != nil {
		h.controlError(w, r, err)
		return
	}
	runs, err := h.service.Runs(r.Context(), filter)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if page > 1 && filter.Offset >= runs.Total {
		last := (runs.Total + 24) / 25
		if last < 1 {
			last = 1
		}
		http.Redirect(w, r, withPage(r, last), http.StatusSeeOther)
		return
	}
	data := h.authPage(auth, "Runs")
	data.Page, data.RunsTotal, data.Window, data.View = page, runs.Total, window, view
	data.ExecutionStatusFilter, data.TriggerTypeFilter = r.URL.Query().Get("status"), r.URL.Query().Get("trigger_type")
	data.ReturnURL = safeReturn(r.URL.RequestURI())
	data.TableURL, data.BoardURL = viewURL(r, "table"), viewURL(r, "board")
	hasNext := len(runs.Runs) > 25
	if hasNext {
		runs.Runs = runs.Runs[:25]
	}
	data.Runs = runs.Runs
	data.PreviousURL, data.NextURL = paginationURLs(r, page, hasNext)
	for _, column := range []runColumn{{Status: "emitted", Label: "Queued (emitted)"}, {Status: "in_progress", Label: "In progress"}, {Status: "delivered", Label: "Delivered"}, {Status: "failed", Label: "Failed"}} {
		for i := range data.Runs {
			run := &data.Runs[i]
			if string(run.Execution.Status) == column.Status {
				column.Runs = append(column.Runs, *run)
			}
		}
		data.RunColumns = append(data.RunColumns, column)
	}
	h.render(w, "runs", data)
}
func (h *Handler) saveView(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	if err := h.service.SaveView(r.Context(), auth.Key.ID, r.PostForm.Get("view_name"), r.PostForm.Get("query")); err != nil {
		h.controlError(w, r, err)
		return
	}
	http.Redirect(w, r, safeReturn(r.PostForm.Get("return")), http.StatusSeeOther)
}
func (h *Handler) deleteView(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteView(r.Context(), auth.Key.ID, id); err != nil {
		h.controlError(w, r, err)
		return
	}
	http.Redirect(w, r, safeReturn(r.PostForm.Get("return")), http.StatusSeeOther)
}
func (h *Handler) previewBulk(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	rawIDs := r.PostForm["job_id"]
	if len(rawIDs) < 1 || len(rawIDs) > domain.MaxBulkJobs {
		h.controlError(w, r, domain.ErrInvalidControlInput)
		return
	}
	ids := make([]uuid.UUID, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			h.controlError(w, r, domain.ErrInvalidControlInput)
			return
		}
		ids = append(ids, id)
	}
	batch, err := h.service.PreviewBulk(r.Context(), auth.Key.ID, r.PostForm.Get("action"), ids)
	if err != nil {
		h.controlError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/bulk/"+batch.ID.String()+"?"+returnQuery(r.PostForm.Get("return")), http.StatusSeeOther)
}
func (h *Handler) bulkPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	batch, err := h.service.GetBulk(r.Context(), auth.Key.ID, id)
	if err != nil {
		h.controlError(w, r, err)
		return
	}
	data := h.authPage(auth, "Bulk "+batch.Action)
	data.Batch = batch
	data.ReturnURL = safeReturn(r.URL.Query().Get("return"))
	for _, item := range batch.Items {
		switch item.Result {
		case "applied":
			data.Applied++
		case "unchanged":
			data.Unchanged++
		case "":
		default:
			data.Failed++
		}
	}
	h.render(w, "bulk", data)
}
func (h *Handler) confirmBulk(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if r.PostForm.Get("confirm") != "yes" {
		h.controlError(w, r, domain.ErrInvalidControlInput)
		return
	}
	if _, err := h.service.ConfirmBulk(r.Context(), auth.Key.ID, id); err != nil {
		h.controlError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/bulk/"+id.String()+"?"+returnQuery(r.PostForm.Get("return")), http.StatusSeeOther)
}
func (h *Handler) bulkHistory(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	batches, err := h.service.ListBulk(r.Context(), auth.Key.ID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	data := h.authPage(auth, "Bulk action history")
	data.Batches = batches
	h.render(w, "bulk_history", data)
}
func (h *Handler) controlError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidControlInput):
		h.renderStatus(w, "error", pageData{Title: "Invalid input", Error: "Check the filters or selection. Select 1–100 distinct jobs and choose pause or resume. Previews expire after 10 minutes; keep at most 20 pending previews."}, http.StatusBadRequest)
	case errors.Is(err, domain.ErrBulkNotFound), errors.Is(err, domain.ErrJobNotFound), errors.Is(err, domain.ErrAPIKeyNotFound):
		h.renderStatus(w, "error", pageData{Title: "Not found", Error: "The requested item is unavailable or the preview has expired."}, http.StatusNotFound)
	case errors.Is(err, domain.ErrSavedViewLimit):
		h.renderStatus(w, "error", pageData{Title: "Saved view limit", Error: "You can save up to 20 views per API key. Remove a view before adding another."}, http.StatusConflict)
	default:
		h.internalError(w, r, err)
	}
}
func bulkResultText(result string) string {
	switch result {
	case "applied":
		return "Applied"
	case "unchanged":
		return "Already in requested state"
	case "conflict":
		return "Changed since preview; select again"
	case "unavailable":
		return "Job no longer available"
	case "failed":
		return "Failed; select again to retry"
	default:
		return "Awaiting confirmation"
	}
}
