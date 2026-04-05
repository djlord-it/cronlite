package mcp

import (
	"context"
	"encoding/json"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/djlord-it/easy-cron/internal/service"
	"github.com/google/uuid"
)

// RegisterTools registers all Phase 1 MCP tools on the given MCPServer.
func RegisterTools(s *server.MCPServer, svc *service.JobService) {
	s.AddTool(createJobTool(), handleCreateJob(svc))
	s.AddTool(listJobsTool(), handleListJobs(svc))
	s.AddTool(getJobTool(), handleGetJob(svc))
	s.AddTool(updateJobTool(), handleUpdateJob(svc))
	s.AddTool(deleteJobTool(), handleDeleteJob(svc))
	s.AddTool(pauseJobTool(), handlePauseJob(svc))
	s.AddTool(resumeJobTool(), handleResumeJob(svc))
	s.AddTool(triggerJobTool(), handleTriggerJob(svc))
	s.AddTool(nextRunTool(), handleNextRun(svc))
	s.AddTool(resolveScheduleTool(), handleResolveSchedule(svc))
}

// ── Tool definitions ────────────────────────────────────────────────────────

func createJobTool() mcpgo.Tool {
	return mcpgo.NewTool("create-job",
		mcpgo.WithDescription(
			"Create a new scheduled cron job that fires a webhook on the given schedule. "+
				"Requires name, cron_expression, timezone, and webhook_url. "+
				"Use resolve-schedule first if you have a natural-language schedule description instead of a cron expression.",
		),
		mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("Human-readable job name")),
		mcpgo.WithString("cron_expression", mcpgo.Required(), mcpgo.Description("Cron expression (5-field). Use resolve-schedule to convert natural language.")),
		mcpgo.WithString("timezone", mcpgo.Required(), mcpgo.Description("IANA timezone, e.g. \"UTC\", \"America/New_York\"")),
		mcpgo.WithString("webhook_url", mcpgo.Required(), mcpgo.Description("Fully qualified HTTP/HTTPS URL to call on each trigger")),
		mcpgo.WithString("webhook_secret", mcpgo.Description("Optional HMAC secret for webhook signature verification")),
		mcpgo.WithNumber("webhook_timeout_seconds", mcpgo.Description("Webhook delivery timeout in seconds (default: 30)")),
		mcpgo.WithObject("tags", mcpgo.Description("Optional key-value tags for organizing jobs, e.g. {\"env\": \"prod\"}")),
	)
}

func listJobsTool() mcpgo.Tool {
	return mcpgo.NewTool("list-jobs",
		mcpgo.WithDescription(
			"List all jobs in the current namespace. "+
				"Supports optional filtering by name substring and enabled status.",
		),
		mcpgo.WithString("name", mcpgo.Description("Filter by name substring (case-insensitive)")),
		mcpgo.WithBoolean("enabled", mcpgo.Description("Filter by enabled status (true/false)")),
	)
}

func getJobTool() mcpgo.Tool {
	return mcpgo.NewTool("get-job",
		mcpgo.WithDescription(
			"Get detailed information about a job including its schedule, tags, and the 5 most recent executions. "+
				"Use list-jobs first to find the job ID.",
		),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID")),
	)
}

func updateJobTool() mcpgo.Tool {
	return mcpgo.NewTool("update-job",
		mcpgo.WithDescription(
			"Partially update an existing job. Only provided fields are changed; omitted fields remain unchanged. "+
				"To change the schedule, provide cron_expression and/or timezone. "+
				"Use resolve-schedule to convert natural language to a cron expression before updating.",
		),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to update")),
		mcpgo.WithString("name", mcpgo.Description("New job name")),
		mcpgo.WithString("cron_expression", mcpgo.Description("New cron expression (5-field)")),
		mcpgo.WithString("timezone", mcpgo.Description("New IANA timezone")),
		mcpgo.WithString("webhook_url", mcpgo.Description("New webhook URL")),
		mcpgo.WithString("webhook_secret", mcpgo.Description("New HMAC secret")),
		mcpgo.WithNumber("webhook_timeout_seconds", mcpgo.Description("New webhook timeout in seconds")),
		mcpgo.WithBoolean("enabled", mcpgo.Description("Set enabled/disabled status")),
		mcpgo.WithObject("tags", mcpgo.Description("Replace all tags with this map, e.g. {\"env\": \"staging\"}")),
	)
}

func deleteJobTool() mcpgo.Tool {
	return mcpgo.NewTool("delete-job",
		mcpgo.WithDescription(
			"Permanently delete a job and all its associated data (schedule, executions, tags). This cannot be undone.",
		),
		mcpgo.WithDestructiveHintAnnotation(true),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to delete")),
	)
}

func pauseJobTool() mcpgo.Tool {
	return mcpgo.NewTool("pause-job",
		mcpgo.WithDescription(
			"Pause a job so it stops being scheduled. The job is not deleted and can be resumed later with resume-job.",
		),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to pause")),
	)
}

func resumeJobTool() mcpgo.Tool {
	return mcpgo.NewTool("resume-job",
		mcpgo.WithDescription(
			"Resume a paused job so it starts being scheduled again. Use get-job to verify current status.",
		),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to resume")),
	)
}

func triggerJobTool() mcpgo.Tool {
	return mcpgo.NewTool("trigger-job",
		mcpgo.WithDescription(
			"Immediately trigger a job execution outside of its regular schedule. "+
				"The job must be enabled; use resume-job first if it is paused.",
		),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to trigger")),
	)
}

func nextRunTool() mcpgo.Tool {
	return mcpgo.NewTool("next-run",
		mcpgo.WithDescription(
			"Get the next scheduled run time and the upcoming 5 run times for a job. "+
				"Useful for verifying a job's schedule is configured correctly.",
		),
		mcpgo.WithReadOnlyHintAnnotation(true),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID")),
	)
}

func resolveScheduleTool() mcpgo.Tool {
	return mcpgo.NewTool("resolve-schedule",
		mcpgo.WithDescription(
			"Convert a natural-language schedule description (e.g. \"every weekday at 9am\") or a raw cron expression "+
				"into a validated cron expression with a human-readable description and the next 5 run times. "+
				"Call this before create-job or update-job when you have a natural-language schedule.",
		),
		mcpgo.WithReadOnlyHintAnnotation(true),
		mcpgo.WithString("description", mcpgo.Required(), mcpgo.Description("Natural language description or cron expression to resolve")),
		mcpgo.WithString("timezone", mcpgo.Description("IANA timezone (default: \"UTC\")")),
	)
}

// ── Tool handlers ───────────────────────────────────────────────────────────

func handleCreateJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := request.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: name"), nil
		}
		cronExpr, err := request.RequireString("cron_expression")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: cron_expression"), nil
		}
		tz, err := request.RequireString("timezone")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: timezone"), nil
		}
		webhookURL, err := request.RequireString("webhook_url")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: webhook_url"), nil
		}

		secret := request.GetString("webhook_secret", "")
		timeoutSec := request.GetFloat("webhook_timeout_seconds", 0)

		var timeout time.Duration
		if timeoutSec > 0 {
			timeout = time.Duration(timeoutSec) * time.Second
		}

		tags := parseTags(request.GetArguments()["tags"])

		input := service.CreateJobInput{
			Name:           name,
			CronExpression: cronExpr,
			Timezone:       tz,
			WebhookURL:     webhookURL,
			Secret:         secret,
			Timeout:        timeout,
			Tags:           tags,
		}

		job, schedule, err := svc.CreateJob(ctx, input)
		if err != nil {
			return toolError(err)
		}

		result := map[string]any{
			"id":              job.ID.String(),
			"namespace":       job.Namespace.String(),
			"name":            job.Name,
			"enabled":         job.Enabled,
			"cron_expression": schedule.CronExpression,
			"timezone":        schedule.Timezone,
			"webhook_url":     job.Delivery.WebhookURL,
			"created_at":      job.CreatedAt.Format(time.RFC3339),
			"updated_at":      job.UpdatedAt.Format(time.RFC3339),
		}
		if len(tags) > 0 {
			tagMap := make(map[string]string, len(tags))
			for _, t := range tags {
				tagMap[t.Key] = t.Value
			}
			result["tags"] = tagMap
		}

		return jsonResult(result)
	}
}

func handleListJobs(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		filter := domain.JobFilter{}

		if name := request.GetString("name", ""); name != "" {
			filter.Name = name
		}

		args := request.GetArguments()
		if _, ok := args["enabled"]; ok {
			enabled := request.GetBool("enabled", true)
			filter.Enabled = &enabled
		}

		jobs, err := svc.ListJobs(ctx, filter)
		if err != nil {
			return toolError(err)
		}

		jobList := make([]map[string]any, len(jobs))
		for i := range jobs {
			j := &jobs[i]
			jobList[i] = map[string]any{
				"id":         j.ID.String(),
				"namespace":  j.Namespace.String(),
				"name":       j.Name,
				"enabled":    j.Enabled,
				"created_at": j.CreatedAt.Format(time.RFC3339),
				"updated_at": j.UpdatedAt.Format(time.RFC3339),
			}
		}

		return jsonResult(map[string]any{
			"jobs":  jobList,
			"count": len(jobs),
		})
	}
}

func handleGetJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		job, schedule, tags, executions, err := svc.GetJob(ctx, id)
		if err != nil {
			return toolError(err)
		}

		result := map[string]any{
			"id":              job.ID.String(),
			"namespace":       job.Namespace.String(),
			"name":            job.Name,
			"enabled":         job.Enabled,
			"cron_expression": schedule.CronExpression,
			"timezone":        schedule.Timezone,
			"webhook_url":     job.Delivery.WebhookURL,
			"created_at":      job.CreatedAt.Format(time.RFC3339),
			"updated_at":      job.UpdatedAt.Format(time.RFC3339),
		}

		if len(tags) > 0 {
			tagMap := make(map[string]string, len(tags))
			for _, t := range tags {
				tagMap[t.Key] = t.Value
			}
			result["tags"] = tagMap
		}

		if len(executions) > 0 {
			execList := make([]map[string]any, len(executions))
			for i := range executions {
				execList[i] = executionToMap(executions[i])
			}
			result["recent_executions"] = execList
		}

		return jsonResult(result)
	}
}

func handleUpdateJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		args := request.GetArguments()
		input := service.UpdateJobInput{}

		if v, ok := args["name"]; ok {
			s := v.(string)
			input.Name = &s
		}
		if v, ok := args["cron_expression"]; ok {
			s := v.(string)
			input.CronExpression = &s
		}
		if v, ok := args["timezone"]; ok {
			s := v.(string)
			input.Timezone = &s
		}
		if v, ok := args["webhook_url"]; ok {
			s := v.(string)
			input.WebhookURL = &s
		}
		if v, ok := args["webhook_secret"]; ok {
			s := v.(string)
			input.Secret = &s
		}
		if v, ok := args["webhook_timeout_seconds"]; ok {
			f, _ := v.(float64)
			d := time.Duration(f) * time.Second
			input.Timeout = &d
		}
		if v, ok := args["tags"]; ok {
			tags := parseTags(v)
			input.Tags = &tags
		}

		job, schedule, err := svc.UpdateJob(ctx, id, input)
		if err != nil {
			return toolError(err)
		}

		result := map[string]any{
			"id":              job.ID.String(),
			"namespace":       job.Namespace.String(),
			"name":            job.Name,
			"enabled":         job.Enabled,
			"cron_expression": schedule.CronExpression,
			"timezone":        schedule.Timezone,
			"webhook_url":     job.Delivery.WebhookURL,
			"created_at":      job.CreatedAt.Format(time.RFC3339),
			"updated_at":      job.UpdatedAt.Format(time.RFC3339),
		}

		return jsonResult(result)
	}
}

func handleDeleteJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		if err := svc.DeleteJob(ctx, id); err != nil {
			return toolError(err)
		}

		return mcpgo.NewToolResultText("Job " + idStr + " deleted successfully."), nil
	}
}

func handlePauseJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		job, err := svc.PauseJob(ctx, id)
		if err != nil {
			return toolError(err)
		}

		result := map[string]any{
			"id":      job.ID.String(),
			"name":    job.Name,
			"enabled": job.Enabled,
			"message": "Job paused successfully. Use resume-job to re-enable.",
		}

		return jsonResult(result)
	}
}

func handleResumeJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		job, err := svc.ResumeJob(ctx, id)
		if err != nil {
			return toolError(err)
		}

		result := map[string]any{
			"id":      job.ID.String(),
			"name":    job.Name,
			"enabled": job.Enabled,
			"message": "Job resumed successfully.",
		}

		return jsonResult(result)
	}
}

func handleTriggerJob(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		exec, err := svc.TriggerNow(ctx, id)
		if err != nil {
			return toolError(err)
		}

		return jsonResult(executionToMap(exec))
	}
}

func handleNextRun(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		idStr, err := request.RequireString("id")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: id"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcpgo.NewToolResultError("Invalid job ID format. Expected a UUID."), nil
		}

		nextRun, runs, schedule, err := svc.GetNextRunTime(ctx, id)
		if err != nil {
			return toolError(err)
		}

		runStrs := make([]string, len(runs))
		for i, r := range runs {
			runStrs[i] = r.Format(time.RFC3339)
		}

		result := map[string]any{
			"cron_expression": schedule.CronExpression,
			"timezone":        schedule.Timezone,
			"next_run_at":     nextRun.Format(time.RFC3339),
			"next_runs":       runStrs,
		}

		return jsonResult(result)
	}
}

func handleResolveSchedule(svc *service.JobService) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		desc, err := request.RequireString("description")
		if err != nil {
			return mcpgo.NewToolResultError("Missing required parameter: description"), nil
		}

		tz := request.GetString("timezone", "UTC")

		result, err := svc.ResolveSchedule(ctx, desc, tz)
		if err != nil {
			return toolError(err)
		}

		runStrs := make([]string, len(result.NextRuns))
		for i, r := range result.NextRuns {
			runStrs[i] = r.Format(time.RFC3339)
		}

		return jsonResult(map[string]any{
			"cron_expression": result.CronExpression,
			"description":     result.Description,
			"timezone":        result.Timezone,
			"next_runs":       runStrs,
		})
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// jsonResult marshals v as indented JSON and returns it as a tool result text.
func jsonResult(v any) (*mcpgo.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpgo.NewToolResultError("Failed to serialize result: " + err.Error()), nil
	}
	return mcpgo.NewToolResultText(string(data)), nil
}

// parseTags converts a raw tags argument (expected to be map[string]any) to
// a slice of domain.Tag. Returns nil for nil or non-map input.
func parseTags(raw any) []domain.Tag {
	if raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	tags := make([]domain.Tag, 0, len(m))
	for k, v := range m {
		s, _ := v.(string)
		tags = append(tags, domain.Tag{Key: k, Value: s})
	}
	return tags
}

// executionToMap converts a domain.Execution to a map for JSON serialization.
func executionToMap(e domain.Execution) map[string]any {
	m := map[string]any{
		"id":           e.ID.String(),
		"job_id":       e.JobID.String(),
		"trigger_type": string(e.TriggerType),
		"scheduled_at": e.ScheduledAt.Format(time.RFC3339),
		"fired_at":     e.FiredAt.Format(time.RFC3339),
		"status":       string(e.Status),
		"created_at":   e.CreatedAt.Format(time.RFC3339),
	}
	if e.AcknowledgedAt != nil {
		m["acknowledged_at"] = e.AcknowledgedAt.Format(time.RFC3339)
	}
	return m
}
