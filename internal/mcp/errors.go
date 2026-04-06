package mcp

import (
	"errors"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/djlord-it/cronlite/internal/domain"
)

// toolError maps domain errors to MCP CallToolResult error responses.
// Messages are written for AI agents with actionable hints.
func toolError(err error) (*mcpgo.CallToolResult, error) {
	switch {
	case errors.Is(err, domain.ErrJobNotFound):
		return mcpgo.NewToolResultError("Job not found. Verify the job ID is correct and belongs to your namespace."), nil
	case errors.Is(err, domain.ErrExecutionNotFound):
		return mcpgo.NewToolResultError("Execution not found. Verify the execution ID is correct."), nil
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		return mcpgo.NewToolResultError("API key not found."), nil
	case errors.Is(err, domain.ErrInvalidCronExpression):
		return mcpgo.NewToolResultError("Invalid cron expression. Use resolve-schedule to convert natural language to a valid cron expression."), nil
	case errors.Is(err, domain.ErrInvalidTimezone):
		return mcpgo.NewToolResultError("Invalid timezone. Use an IANA timezone name like \"UTC\", \"America/New_York\", or \"Europe/London\"."), nil
	case errors.Is(err, domain.ErrInvalidWebhookURL):
		return mcpgo.NewToolResultError("Invalid webhook URL. Provide a fully qualified HTTP or HTTPS URL."), nil
	case errors.Is(err, domain.ErrJobDisabled):
		return mcpgo.NewToolResultError("Job is disabled. Use resume-job first to re-enable it before triggering."), nil
	case errors.Is(err, domain.ErrScheduleParseFailure):
		return mcpgo.NewToolResultError("Could not parse schedule. Try a different phrasing or use a cron expression directly (e.g. \"*/5 * * * *\")."), nil
	case errors.Is(err, domain.ErrNamespaceRequired):
		return mcpgo.NewToolResultError("Authentication required. Set CRONLITE_API_KEY or provide a Bearer token."), nil
	case errors.Is(err, domain.ErrNamespaceMismatch):
		return mcpgo.NewToolResultError("Job not found. The job may belong to a different namespace."), nil
	case errors.Is(err, domain.ErrDuplicateExecution):
		return mcpgo.NewToolResultError("Duplicate execution. This execution already exists."), nil
	case errors.Is(err, domain.ErrDuplicateAPIKey):
		return mcpgo.NewToolResultError("Duplicate API key."), nil
	default:
		return mcpgo.NewToolResultError("Internal error: " + err.Error()), nil
	}
}
