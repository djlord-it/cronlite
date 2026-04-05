package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/google/uuid"
)

// CreateJobInput holds the parameters for creating a new job.
type CreateJobInput struct {
	Name           string
	CronExpression string
	Timezone       string
	WebhookURL     string
	Secret         string
	Timeout        time.Duration
	Tags           []domain.Tag
}

// UpdateJobInput holds the optional parameters for updating a job. Only
// non-nil fields are applied.
type UpdateJobInput struct {
	Name           *string
	CronExpression *string
	Timezone       *string
	WebhookURL     *string
	Secret         *string
	Timeout        *time.Duration
	Tags           *[]domain.Tag
}

// ResolveResult is returned by ResolveSchedule after parsing a human-readable
// description or cron expression.
type ResolveResult struct {
	CronExpression string
	Description    string
	Timezone       string
	NextRuns       []time.Time
}

// CreateJob creates a new job with its schedule.
func (s *JobService) CreateJob(ctx context.Context, input CreateJobInput) (domain.Job, domain.Schedule, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Job{}, domain.Schedule{}, domain.ErrNamespaceRequired
	}

	// Validate cron expression.
	_, err := s.parser.Parse(input.CronExpression, input.Timezone)
	if err != nil {
		return domain.Job{}, domain.Schedule{}, domain.ErrInvalidCronExpression
	}

	// Validate timezone.
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return domain.Job{}, domain.Schedule{}, domain.ErrInvalidTimezone
	}

	// Default timeout.
	timeout := input.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	now := time.Now().UTC()
	scheduleID := uuid.New()
	jobID := uuid.New()

	schedule := domain.Schedule{
		ID:             scheduleID,
		CronExpression: input.CronExpression,
		Timezone:       input.Timezone,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	job := domain.Job{
		ID:         jobID,
		Namespace:  ns,
		Name:       input.Name,
		Enabled:    true,
		ScheduleID: scheduleID,
		Delivery: domain.DeliveryConfig{
			Type:       domain.DeliveryTypeWebhook,
			WebhookURL: input.WebhookURL,
			Secret:     input.Secret,
			Timeout:    timeout,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.jobs.InsertJob(ctx, job, schedule); err != nil {
		return domain.Job{}, domain.Schedule{}, fmt.Errorf("insert job: %w", err)
	}

	if len(input.Tags) > 0 {
		if err := s.tags.UpsertTags(ctx, jobID, input.Tags); err != nil {
			return domain.Job{}, domain.Schedule{}, fmt.Errorf("upsert tags: %w", err)
		}
	}

	return job, schedule, nil
}

// GetJob retrieves a job with its schedule, tags, and recent executions.
func (s *JobService) GetJob(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, []domain.Tag, []domain.Execution, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Job{}, domain.Schedule{}, nil, nil, domain.ErrNamespaceRequired
	}

	job, schedule, err := s.jobs.GetJobWithScheduleScoped(ctx, id, ns)
	if err != nil {
		return domain.Job{}, domain.Schedule{}, nil, nil, domain.ErrJobNotFound
	}

	tags, err := s.tags.GetTags(ctx, id)
	if err != nil {
		return domain.Job{}, domain.Schedule{}, nil, nil, fmt.Errorf("get tags: %w", err)
	}

	executions, err := s.executions.GetRecentExecutions(ctx, id, 5)
	if err != nil {
		return domain.Job{}, domain.Schedule{}, nil, nil, fmt.Errorf("get recent executions: %w", err)
	}

	return job, schedule, tags, executions, nil
}

// ListJobs returns jobs matching the filter, scoped to the namespace from ctx.
func (s *JobService) ListJobs(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return nil, domain.ErrNamespaceRequired
	}

	filter.Namespace = ns
	filter.ListParams = filter.ListParams.WithDefaults()

	return s.jobs.ListJobs(ctx, filter)
}

// UpdateJob applies partial updates to an existing job.
func (s *JobService) UpdateJob(ctx context.Context, id uuid.UUID, input UpdateJobInput) (domain.Job, domain.Schedule, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Job{}, domain.Schedule{}, domain.ErrNamespaceRequired
	}

	job, schedule, err := s.jobs.GetJobWithScheduleScoped(ctx, id, ns)
	if err != nil {
		return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
	}

	now := time.Now().UTC()

	if input.Name != nil {
		job.Name = *input.Name
	}
	if input.WebhookURL != nil {
		job.Delivery.WebhookURL = *input.WebhookURL
	}
	if input.Secret != nil {
		job.Delivery.Secret = *input.Secret
	}
	if input.Timeout != nil {
		job.Delivery.Timeout = *input.Timeout
	}

	// If cron or timezone changed, validate and update schedule.
	cronExpr := schedule.CronExpression
	tz := schedule.Timezone
	scheduleChanged := false

	if input.CronExpression != nil {
		cronExpr = *input.CronExpression
		scheduleChanged = true
	}
	if input.Timezone != nil {
		tz = *input.Timezone
		scheduleChanged = true
	}

	if scheduleChanged {
		if _, err := s.parser.Parse(cronExpr, tz); err != nil {
			return domain.Job{}, domain.Schedule{}, domain.ErrInvalidCronExpression
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return domain.Job{}, domain.Schedule{}, domain.ErrInvalidTimezone
		}
		schedule.CronExpression = cronExpr
		schedule.Timezone = tz
		schedule.UpdatedAt = now

		if err := s.schedules.UpdateSchedule(ctx, schedule); err != nil {
			return domain.Job{}, domain.Schedule{}, fmt.Errorf("update schedule: %w", err)
		}
	}

	job.UpdatedAt = now
	if err := s.jobs.UpdateJob(ctx, job); err != nil {
		return domain.Job{}, domain.Schedule{}, fmt.Errorf("update job: %w", err)
	}

	// If tags changed, delete and re-upsert.
	if input.Tags != nil {
		if err := s.tags.DeleteTags(ctx, id); err != nil {
			return domain.Job{}, domain.Schedule{}, fmt.Errorf("delete tags: %w", err)
		}
		if len(*input.Tags) > 0 {
			if err := s.tags.UpsertTags(ctx, id, *input.Tags); err != nil {
				return domain.Job{}, domain.Schedule{}, fmt.Errorf("upsert tags: %w", err)
			}
		}
	}

	return job, schedule, nil
}

// DeleteJob removes a job by ID, scoped to the namespace from ctx.
func (s *JobService) DeleteJob(ctx context.Context, id uuid.UUID) error {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.ErrNamespaceRequired
	}

	return s.jobs.DeleteJob(ctx, id, ns)
}

// PauseJob disables a job so it won't be scheduled.
func (s *JobService) PauseJob(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Job{}, domain.ErrNamespaceRequired
	}

	job, _, err := s.jobs.GetJobWithScheduleScoped(ctx, id, ns)
	if err != nil {
		return domain.Job{}, domain.ErrJobNotFound
	}

	job.Enabled = false
	job.UpdatedAt = time.Now().UTC()

	if err := s.jobs.UpdateJob(ctx, job); err != nil {
		return domain.Job{}, fmt.Errorf("update job: %w", err)
	}

	return job, nil
}

// ResumeJob enables a previously paused job.
func (s *JobService) ResumeJob(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Job{}, domain.ErrNamespaceRequired
	}

	job, _, err := s.jobs.GetJobWithScheduleScoped(ctx, id, ns)
	if err != nil {
		return domain.Job{}, domain.ErrJobNotFound
	}

	job.Enabled = true
	job.UpdatedAt = time.Now().UTC()

	if err := s.jobs.UpdateJob(ctx, job); err != nil {
		return domain.Job{}, fmt.Errorf("update job: %w", err)
	}

	return job, nil
}

// TriggerNow creates a manual execution for the given job.
func (s *JobService) TriggerNow(ctx context.Context, jobID uuid.UUID) (domain.Execution, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.Execution{}, domain.ErrNamespaceRequired
	}

	job, _, err := s.jobs.GetJobWithScheduleScoped(ctx, jobID, ns)
	if err != nil {
		return domain.Execution{}, domain.ErrJobNotFound
	}
	if !job.Enabled {
		return domain.Execution{}, domain.ErrJobDisabled
	}

	now := time.Now().UTC()
	exec := domain.Execution{
		ID:          uuid.New(),
		JobID:       jobID,
		Namespace:   ns,
		TriggerType: domain.TriggerTypeManual,
		ScheduledAt: now,
		FiredAt:     now,
		Status:      domain.ExecutionStatusEmitted,
		CreatedAt:   now,
	}

	if err := s.executions.InsertExecution(ctx, exec); err != nil {
		return domain.Execution{}, fmt.Errorf("insert execution: %w", err)
	}

	return exec, nil
}

// GetNextRunTime computes the next 5 scheduled run times for a job.
func (s *JobService) GetNextRunTime(ctx context.Context, jobID uuid.UUID) (time.Time, []time.Time, domain.Schedule, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return time.Time{}, nil, domain.Schedule{}, domain.ErrNamespaceRequired
	}

	_, schedule, err := s.jobs.GetJobWithScheduleScoped(ctx, jobID, ns)
	if err != nil {
		return time.Time{}, nil, domain.Schedule{}, domain.ErrJobNotFound
	}

	sched, err := s.parser.Parse(schedule.CronExpression, schedule.Timezone)
	if err != nil {
		return time.Time{}, nil, domain.Schedule{}, domain.ErrInvalidCronExpression
	}

	var runs []time.Time
	cursor := time.Now().UTC()
	for i := 0; i < 5; i++ {
		next := sched.Next(cursor)
		runs = append(runs, next)
		cursor = next
	}

	return runs[0], runs, schedule, nil
}

// ResolveSchedule parses a natural-language schedule description or raw cron
// expression and returns a normalised cron expression along with the next 5
// run times.
func (s *JobService) ResolveSchedule(ctx context.Context, description, timezone string) (ResolveResult, error) {
	// Validate timezone.
	if _, err := time.LoadLocation(timezone); err != nil {
		return ResolveResult{}, domain.ErrInvalidTimezone
	}

	// Try as a raw cron expression first (pass-through).
	if sched, err := s.parser.Parse(description, timezone); err == nil {
		runs := computeNextRuns(sched, 5)
		return ResolveResult{
			CronExpression: description,
			Description:    description,
			Timezone:       timezone,
			NextRuns:       runs,
		}, nil
	}

	// Try natural language.
	cronExpr, desc, err := parseNaturalLanguage(description)
	if err != nil {
		return ResolveResult{}, err
	}

	sched, err := s.parser.Parse(cronExpr, timezone)
	if err != nil {
		return ResolveResult{}, domain.ErrScheduleParseFailure
	}

	runs := computeNextRuns(sched, 5)
	return ResolveResult{
		CronExpression: cronExpr,
		Description:    desc,
		Timezone:       timezone,
		NextRuns:       runs,
	}, nil
}

// computeNextRuns returns the next n run times from now.
func computeNextRuns(sched interface{ Next(time.Time) time.Time }, n int) []time.Time {
	var runs []time.Time
	cursor := time.Now().UTC()
	for i := 0; i < n; i++ {
		next := sched.Next(cursor)
		runs = append(runs, next)
		cursor = next
	}
	return runs
}

// Natural language patterns.
var (
	reEveryNMinutes  = regexp.MustCompile(`^every\s+(\d+)\s+minutes?$`)
	reEveryNHours    = regexp.MustCompile(`^every\s+(\d+)\s+hours?$`)
	reEveryHour      = regexp.MustCompile(`^every\s+hour$`)
	reHourly         = regexp.MustCompile(`^hourly$`)
	reDailyAt        = regexp.MustCompile(`^(?:every\s+day|daily)\s+at\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
	reEveryWeekdayAt = regexp.MustCompile(`^every\s+weekday\s+at\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
	reDayOfWeekAt    = regexp.MustCompile(`^every\s+(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\s+at\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
)

var dayMap = map[string]int{
	"sunday":    0,
	"monday":    1,
	"tuesday":   2,
	"wednesday": 3,
	"thursday":  4,
	"friday":    5,
	"saturday":  6,
}

// parseNaturalLanguage converts a human-readable schedule description into a
// cron expression. Returns (cronExpr, description, error).
func parseNaturalLanguage(input string) (string, string, error) {
	lower := strings.ToLower(strings.TrimSpace(input))

	// "every N minutes"
	if m := reEveryNMinutes.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 59 {
			return "", "", domain.ErrScheduleParseFailure
		}
		expr := fmt.Sprintf("*/%d * * * *", n)
		return expr, fmt.Sprintf("every %d minutes", n), nil
	}

	// "every N hours"
	if m := reEveryNHours.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 23 {
			return "", "", domain.ErrScheduleParseFailure
		}
		expr := fmt.Sprintf("0 */%d * * *", n)
		return expr, fmt.Sprintf("every %d hours", n), nil
	}

	// "every hour" or "hourly"
	if reEveryHour.MatchString(lower) || reHourly.MatchString(lower) {
		return "0 * * * *", "every hour", nil
	}

	// "every day at HH:MM" or "daily at HH:MM"
	if m := reDailyAt.FindStringSubmatch(lower); m != nil {
		hour, minute := parseTime(m[1], m[2], m[3])
		if hour < 0 {
			return "", "", domain.ErrScheduleParseFailure
		}
		expr := fmt.Sprintf("%d %d * * *", minute, hour)
		return expr, fmt.Sprintf("daily at %02d:%02d", hour, minute), nil
	}

	// "every weekday at HH:MM"
	if m := reEveryWeekdayAt.FindStringSubmatch(lower); m != nil {
		hour, minute := parseTime(m[1], m[2], m[3])
		if hour < 0 {
			return "", "", domain.ErrScheduleParseFailure
		}
		expr := fmt.Sprintf("%d %d * * 1-5", minute, hour)
		return expr, fmt.Sprintf("every weekday at %02d:%02d", hour, minute), nil
	}

	// "every Monday/Tuesday/... at HH:MM"
	if m := reDayOfWeekAt.FindStringSubmatch(lower); m != nil {
		dayNum := dayMap[m[1]]
		hour, minute := parseTime(m[2], m[3], m[4])
		if hour < 0 {
			return "", "", domain.ErrScheduleParseFailure
		}
		expr := fmt.Sprintf("%d %d * * %d", minute, hour, dayNum)
		return expr, fmt.Sprintf("every %s at %02d:%02d", m[1], hour, minute), nil
	}

	return "", "", domain.ErrScheduleParseFailure
}

// parseTime converts hour/minute/ampm strings into 24-hour format.
// Returns (hour, minute). hour is -1 on error.
func parseTime(hourStr, minuteStr, ampm string) (int, int) {
	hour, err := strconv.Atoi(hourStr)
	if err != nil {
		return -1, 0
	}

	minute := 0
	if minuteStr != "" {
		minute, err = strconv.Atoi(minuteStr)
		if err != nil {
			return -1, 0
		}
	}

	if ampm != "" {
		ampm = strings.ToLower(ampm)
		if ampm == "pm" && hour != 12 {
			hour += 12
		}
		if ampm == "am" && hour == 12 {
			hour = 0
		}
	}

	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return -1, 0
	}

	return hour, minute
}
