// Package main implements a thin MCP stdio client that proxies tool calls
// to the CronLite REST API. It is a separate binary intended to be used by
// AI agents (e.g. Claude Desktop) that communicate over stdin/stdout.
//
// Environment variables:
//
//	CRONLITE_URL     - Base URL of the CronLite server (default: http://localhost:8080)
//	CRONLITE_API_KEY - Bearer token for authentication (required)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	baseURL := os.Getenv("CRONLITE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	apiKey := os.Getenv("CRONLITE_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "CRONLITE_API_KEY is required")
		os.Exit(1)
	}

	s := server.NewMCPServer("CronLite", "1.0.0",
		server.WithToolCapabilities(false),
	)

	client := &http.Client{Timeout: 30 * time.Second}
	registerProxyTools(s, baseURL, apiKey, client)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// registerProxyTools registers all 10 Phase 1 tools. Each handler proxies the
// call to the CronLite REST API and returns the response as tool result text.
func registerProxyTools(s *server.MCPServer, baseURL, apiKey string, client *http.Client) {
	// create-job → POST /jobs
	s.AddTool(
		mcpgo.NewTool("create-job",
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
		),
		proxyHandler(client, baseURL, apiKey, "POST", "/jobs", func(args map[string]any) any {
			body := map[string]any{
				"name":            args["name"],
				"cron_expression": args["cron_expression"],
				"timezone":        args["timezone"],
				"webhook_url":     args["webhook_url"],
			}
			if v, ok := args["webhook_secret"]; ok {
				body["webhook_secret"] = v
			}
			if v, ok := args["webhook_timeout_seconds"]; ok {
				body["webhook_timeout_seconds"] = v
			}
			if v, ok := args["tags"]; ok {
				body["tags"] = v
			}
			return body
		}),
	)

	// list-jobs → GET /jobs
	s.AddTool(
		mcpgo.NewTool("list-jobs",
			mcpgo.WithDescription(
				"List all jobs in the current namespace. "+
					"Supports optional filtering by name substring and enabled status.",
			),
			mcpgo.WithString("name", mcpgo.Description("Filter by name substring (case-insensitive)")),
			mcpgo.WithBoolean("enabled", mcpgo.Description("Filter by enabled status (true/false)")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			args := request.GetArguments()
			query := make(map[string]string)
			if v, ok := args["name"]; ok {
				query["name"] = fmt.Sprintf("%v", v)
			}
			if v, ok := args["enabled"]; ok {
				query["enabled"] = fmt.Sprintf("%v", v)
			}
			return doGet(ctx, client, baseURL+"/jobs", apiKey, query)
		},
	)

	// get-job → GET /jobs/{id}
	s.AddTool(
		mcpgo.NewTool("get-job",
			mcpgo.WithDescription(
				"Get detailed information about a job including its schedule, tags, and the 5 most recent executions. "+
					"Use list-jobs first to find the job ID.",
			),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			return doGet(ctx, client, baseURL+"/jobs/"+id, apiKey, nil)
		},
	)

	// update-job → PATCH /jobs/{id}
	s.AddTool(
		mcpgo.NewTool("update-job",
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
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			args := request.GetArguments()
			body := make(map[string]any)
			for _, key := range []string{"name", "cron_expression", "timezone", "webhook_url", "webhook_secret", "webhook_timeout_seconds", "enabled", "tags"} {
				if v, ok := args[key]; ok {
					body[key] = v
				}
			}
			return doJSON(ctx, client, "PATCH", baseURL+"/jobs/"+id, apiKey, body)
		},
	)

	// delete-job → DELETE /jobs/{id}
	s.AddTool(
		mcpgo.NewTool("delete-job",
			mcpgo.WithDescription(
				"Permanently delete a job and all its associated data (schedule, executions, tags). This cannot be undone.",
			),
			mcpgo.WithDestructiveHintAnnotation(true),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to delete")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			return doRequest(ctx, client, "DELETE", baseURL+"/jobs/"+id, apiKey, nil)
		},
	)

	// pause-job → POST /jobs/{id}/pause
	s.AddTool(
		mcpgo.NewTool("pause-job",
			mcpgo.WithDescription(
				"Pause a job so it stops being scheduled. The job is not deleted and can be resumed later with resume-job.",
			),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to pause")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			return doRequest(ctx, client, "POST", baseURL+"/jobs/"+id+"/pause", apiKey, nil)
		},
	)

	// resume-job → POST /jobs/{id}/resume
	s.AddTool(
		mcpgo.NewTool("resume-job",
			mcpgo.WithDescription(
				"Resume a paused job so it starts being scheduled again. Use get-job to verify current status.",
			),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to resume")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			return doRequest(ctx, client, "POST", baseURL+"/jobs/"+id+"/resume", apiKey, nil)
		},
	)

	// trigger-job → POST /jobs/{id}/trigger
	s.AddTool(
		mcpgo.NewTool("trigger-job",
			mcpgo.WithDescription(
				"Immediately trigger a job execution outside of its regular schedule. "+
					"The job must be enabled; use resume-job first if it is paused.",
			),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID to trigger")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			return doRequest(ctx, client, "POST", baseURL+"/jobs/"+id+"/trigger", apiKey, nil)
		},
	)

	// next-run → GET /jobs/{id}/next-run
	s.AddTool(
		mcpgo.NewTool("next-run",
			mcpgo.WithDescription(
				"Get the next scheduled run time and the upcoming 5 run times for a job. "+
					"Useful for verifying a job's schedule is configured correctly.",
			),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Job UUID")),
		),
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := request.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("Missing required parameter: id"), nil
			}
			return doGet(ctx, client, baseURL+"/jobs/"+id+"/next-run", apiKey, nil)
		},
	)

	// resolve-schedule → POST /schedules/resolve
	s.AddTool(
		mcpgo.NewTool("resolve-schedule",
			mcpgo.WithDescription(
				"Convert a natural-language schedule description (e.g. \"every weekday at 9am\") or a raw cron expression "+
					"into a validated cron expression with a human-readable description and the next 5 run times. "+
					"Call this before create-job or update-job when you have a natural-language schedule.",
			),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithString("description", mcpgo.Required(), mcpgo.Description("Natural language description or cron expression to resolve")),
			mcpgo.WithString("timezone", mcpgo.Description("IANA timezone (default: \"UTC\")")),
		),
		proxyHandler(client, baseURL, apiKey, "POST", "/schedules/resolve", func(args map[string]any) any {
			body := map[string]any{
				"description": args["description"],
			}
			if v, ok := args["timezone"]; ok {
				body["timezone"] = v
			}
			return body
		}),
	)
}

// ── HTTP helpers ────────────────────────────────────────────────────────────

// proxyHandler returns a ToolHandlerFunc that builds a JSON body from tool args
// and sends it to the REST API.
func proxyHandler(
	client *http.Client,
	baseURL, apiKey, method, path string,
	buildBody func(args map[string]any) any,
) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := request.GetArguments()
		body := buildBody(args)
		return doJSON(ctx, client, method, baseURL+path, apiKey, body)
	}
}

// doJSON sends a JSON-encoded body and returns the response as tool result text.
func doJSON(ctx context.Context, client *http.Client, method, url, apiKey string, body any) (*mcpgo.CallToolResult, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return mcpgo.NewToolResultError("Failed to encode request: " + err.Error()), nil
		}
		bodyReader = bytes.NewReader(data)
	}
	return doRequest(ctx, client, method, url, apiKey, bodyReader)
}

// doGet sends a GET request with optional query parameters.
func doGet(ctx context.Context, client *http.Client, url, apiKey string, query map[string]string) (*mcpgo.CallToolResult, error) {
	if len(query) > 0 {
		sep := "?"
		for k, v := range query {
			url += sep + k + "=" + v
			sep = "&"
		}
	}
	return doRequest(ctx, client, "GET", url, apiKey, nil)
}

// doRequest performs an HTTP request and returns the response body as tool result text.
func doRequest(ctx context.Context, client *http.Client, method, url, apiKey string, body io.Reader) (*mcpgo.CallToolResult, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return mcpgo.NewToolResultError("Failed to create request: " + err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return mcpgo.NewToolResultError("HTTP request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return mcpgo.NewToolResultError("Failed to read response: " + err.Error()), nil
	}

	// For successful responses, return the body as text.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// For 204 No Content, return a success message.
		if resp.StatusCode == 204 || len(respBody) == 0 {
			return mcpgo.NewToolResultText("Operation completed successfully."), nil
		}
		// Try to pretty-print JSON.
		var pretty bytes.Buffer
		if json.Indent(&pretty, respBody, "", "  ") == nil {
			return mcpgo.NewToolResultText(pretty.String()), nil
		}
		return mcpgo.NewToolResultText(string(respBody)), nil
	}

	// For error responses, return as tool error.
	return mcpgo.NewToolResultError(fmt.Sprintf("API error (HTTP %d): %s", resp.StatusCode, string(respBody))), nil
}
