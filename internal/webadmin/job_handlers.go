package webadmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func (h *Handler) jobsPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 4096 {
		h.controlError(w, r, domain.ErrInvalidControlInput)
		return
	}
	page, filter, view, err := fleetQuery(query)
	if err != nil {
		h.controlError(w, r, err)
		return
	}
	filter.Namespace = auth.Key.Namespace
	if view == "timeline" {
		filter.Limit, filter.Offset = 501, 0
	}
	fleet, err := h.service.Fleet(r.Context(), filter)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if view != "timeline" && page > 1 && filter.Offset >= fleet.Matched {
		last := (fleet.Matched + 24) / 25
		if last < 1 {
			last = 1
		}
		http.Redirect(w, r, withPage(r, last), http.StatusSeeOther)
		return
	}
	data := h.authPage(auth, "Job Control Center")
	data.Fleet, data.Page, data.Form.Name = fleet, page, filter.Name
	data.Jobs = fleet.Jobs
	data.EnabledFilter, data.TagFilter, data.View = r.URL.Query().Get("enabled"), r.URL.Query().Get("tag"), view
	data.ReturnURL, data.StateQuery = fleetState(r), savedQuery(r.URL.Query())
	data.TableURL, data.BoardURL, data.TimelineURL = viewURL(r, "table"), viewURL(r, "board"), viewURL(r, "timeline")
	data.SavedViews, err = h.service.ListSavedViews(r.Context(), auth.Key.ID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if view == "timeline" {
		h.fillTimeline(&data)
	} else {
		hasNext := len(data.Jobs) > 25
		if hasNext {
			data.Jobs = data.Jobs[:25]
		}
		data.PreviousURL, data.NextURL = paginationURLs(r, page, hasNext)
	}
	data.Notice = noticeText(r.URL.Query().Get("notice"))
	h.render(w, "jobs", data)
}

func (h *Handler) createJobPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	data := h.authPage(auth, "Create job")
	data.Form = jobFormValues{Timezone: "UTC", TimeoutSeconds: "30", CronExpression: r.URL.Query().Get("cron_expression")}
	if timezone := r.URL.Query().Get("timezone"); timezone != "" {
		data.Form.Timezone = timezone
	}
	h.render(w, "job_form", data)
}

func (h *Handler) createJob(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	input, values, err := parseCreateJobForm(r)
	if err != nil {
		data := h.authPage(auth, "Create job")
		data.Form, data.Error = values, err.Error()
		h.renderStatus(w, "job_form", data, http.StatusUnprocessableEntity)
		return
	}
	job, _, err := h.service.CreateJob(r.Context(), input)
	if err != nil {
		if isUserError(err) {
			data := h.authPage(auth, "Create job")
			data.Form, data.Error = values, userErrorText(err)
			h.renderStatus(w, "job_form", data, http.StatusUnprocessableEntity)
			return
		}
		h.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+job.ID.String()+"?notice=created", http.StatusSeeOther)
}

func (h *Handler) jobPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	job, schedule, tags, _, err := h.service.GetJob(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	page, pageErr := controlPage(r.URL.Query().Get("page"))
	if pageErr != nil {
		h.controlError(w, r, pageErr)
		return
	}
	filter := domain.ExecutionFilter{
		JobID: id, Namespace: auth.Key.Namespace,
		ListParams: domain.ListParams{Limit: 26, Offset: (page - 1) * 25},
	}
	if value := r.URL.Query().Get("status"); value != "" {
		status := domain.ExecutionStatus(value)
		switch status {
		case domain.ExecutionStatusEmitted, domain.ExecutionStatusInProgress, domain.ExecutionStatusDelivered, domain.ExecutionStatusFailed:
		default:
			h.controlError(w, r, domain.ErrInvalidControlInput)
			return
		}
		filter.Status = &status
	}
	if value := r.URL.Query().Get("trigger_type"); value != "" {
		if value != "manual" && value != "scheduled" {
			h.controlError(w, r, domain.ErrInvalidControlInput)
			return
		}
		filter.TriggerType = &value
	}
	executions, err := h.service.ListExecutions(r.Context(), filter)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	_, nextRuns, _, nextErr := h.service.GetNextRunTime(r.Context(), id)
	if nextErr != nil {
		if !errors.Is(nextErr, domain.ErrJobDisabled) && !errors.Is(nextErr, domain.ErrInvalidCronExpression) {
			h.internalError(w, r, nextErr)
			return
		}
		nextRuns = nil
	}
	data := h.authPage(auth, job.Name)
	data.ReturnURL = safeReturn(r.URL.Query().Get("return"))
	data.Job, data.Schedule, data.Tags = job, schedule, tags
	data.Notice, data.NextRuns = noticeText(r.URL.Query().Get("notice")), nextRuns
	if errors.Is(nextErr, domain.ErrInvalidCronExpression) {
		data.Error = "This schedule has no calculable next run. Check the cron expression and timezone."
	}
	data.ExecutionStatusFilter = r.URL.Query().Get("status")
	data.TriggerTypeFilter = r.URL.Query().Get("trigger_type")
	if len(executions) > 25 {
		executions = executions[:25]
		data.NextURL = withPage(r, page+1)
	}
	if page > 1 {
		data.PreviousURL = withPage(r, page-1)
	}
	data.Executions = executions
	h.render(w, "job_detail", data)
}

func (h *Handler) editJobPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	job, schedule, tags, _, err := h.service.GetJob(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	data := h.authPage(auth, "Edit "+job.Name)
	data.ReturnURL = safeReturn(r.URL.Query().Get("return"))
	data.Job, data.Edit = job, true
	data.Form = jobFormValues{
		Name: job.Name, CronExpression: schedule.CronExpression, Timezone: schedule.Timezone,
		WebhookURL: job.Delivery.WebhookURL, TimeoutSeconds: strconv.Itoa(int(job.Delivery.Timeout.Seconds())),
		Tags: tagsText(tags),
	}
	h.render(w, "job_form", data)
}

func (h *Handler) editJob(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	input, values, err := parseUpdateJobForm(r)
	if err != nil {
		data := h.authPage(auth, "Edit job")
		data.ReturnURL = safeReturn(r.FormValue("return"))
		data.Job.ID, data.Edit, data.Form, data.Error = id, true, values, err.Error()
		h.renderStatus(w, "job_form", data, http.StatusUnprocessableEntity)
		return
	}
	if _, _, err := h.service.UpdateJob(r.Context(), id, input); err != nil {
		if isUserError(err) {
			data := h.authPage(auth, "Edit job")
			data.ReturnURL = safeReturn(r.FormValue("return"))
			data.Job.ID, data.Edit, data.Form, data.Error = id, true, values, userErrorText(err)
			h.renderStatus(w, "job_form", data, http.StatusUnprocessableEntity)
			return
		}
		h.handleServiceError(w, r, err)
		return
	}
	http.Redirect(w, r, actionRedirect(id, "updated", r.FormValue("return")), http.StatusSeeOther)
}

func (h *Handler) deleteJobPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	job, schedule, _, _, err := h.service.GetJob(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	data := h.authPage(auth, "Delete "+job.Name)
	data.ReturnURL = safeReturn(r.URL.Query().Get("return"))
	data.Job, data.Schedule = job, schedule
	h.render(w, "delete_job", data)
}

func (h *Handler) deleteJob(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireMutation(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteJob(r.Context(), id); err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	http.Redirect(w, r, deleteRedirect(r.FormValue("return")), http.StatusSeeOther)
}

func (h *Handler) pauseJob(w http.ResponseWriter, r *http.Request) {
	h.jobAction(w, r, "paused", func(ctx context.Context, id uuid.UUID) error {
		_, err := h.service.PauseJob(ctx, id)
		return err
	})
}

func (h *Handler) resumeJob(w http.ResponseWriter, r *http.Request) {
	h.jobAction(w, r, "resumed", func(ctx context.Context, id uuid.UUID) error {
		_, err := h.service.ResumeJob(ctx, id)
		return err
	})
}

func (h *Handler) triggerJob(w http.ResponseWriter, r *http.Request) {
	h.jobAction(w, r, "triggered", func(ctx context.Context, id uuid.UUID) error {
		_, err := h.service.TriggerNow(ctx, id)
		return err
	})
}

func (h *Handler) jobAction(w http.ResponseWriter, r *http.Request, notice string, action func(context.Context, uuid.UUID) error) {
	if _, ok := h.requireMutation(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if err := action(r.Context(), id); err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	http.Redirect(w, r, actionRedirect(id, notice, r.FormValue("return")), http.StatusSeeOther)
}

func (h *Handler) executionPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	execution, attempts, err := h.service.GetExecution(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	data := h.authPage(auth, "Execution "+execution.ID.String())
	data.ReturnURL = safeReturn(r.URL.Query().Get("return"))
	data.Execution, data.Attempts = execution, attempts
	h.render(w, "execution", data)
}
