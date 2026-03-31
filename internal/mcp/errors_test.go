package mcp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/djlord-it/easy-cron/internal/domain"
)

func TestToolError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantSubstr  string
		wantIsError bool
	}{
		{
			name:        "ErrJobNotFound",
			err:         domain.ErrJobNotFound,
			wantSubstr:  "Job not found",
			wantIsError: true,
		},
		{
			name:        "ErrExecutionNotFound",
			err:         domain.ErrExecutionNotFound,
			wantSubstr:  "Execution not found",
			wantIsError: true,
		},
		{
			name:        "ErrAPIKeyNotFound",
			err:         domain.ErrAPIKeyNotFound,
			wantSubstr:  "API key not found",
			wantIsError: true,
		},
		{
			name:        "ErrInvalidCronExpression",
			err:         domain.ErrInvalidCronExpression,
			wantSubstr:  "Invalid cron expression",
			wantIsError: true,
		},
		{
			name:        "ErrInvalidTimezone",
			err:         domain.ErrInvalidTimezone,
			wantSubstr:  "Invalid timezone",
			wantIsError: true,
		},
		{
			name:        "ErrInvalidWebhookURL",
			err:         domain.ErrInvalidWebhookURL,
			wantSubstr:  "Invalid webhook URL",
			wantIsError: true,
		},
		{
			name:        "ErrJobDisabled",
			err:         domain.ErrJobDisabled,
			wantSubstr:  "Job is disabled",
			wantIsError: true,
		},
		{
			name:        "ErrScheduleParseFailure",
			err:         domain.ErrScheduleParseFailure,
			wantSubstr:  "Could not parse schedule",
			wantIsError: true,
		},
		{
			name:        "ErrNamespaceRequired",
			err:         domain.ErrNamespaceRequired,
			wantSubstr:  "Authentication required",
			wantIsError: true,
		},
		{
			name:        "ErrNamespaceMismatch",
			err:         domain.ErrNamespaceMismatch,
			wantSubstr:  "different namespace",
			wantIsError: true,
		},
		{
			name:        "ErrDuplicateExecution",
			err:         domain.ErrDuplicateExecution,
			wantSubstr:  "Duplicate execution",
			wantIsError: true,
		},
		{
			name:        "ErrDuplicateAPIKey",
			err:         domain.ErrDuplicateAPIKey,
			wantSubstr:  "Duplicate API key",
			wantIsError: true,
		},
		{
			name:        "wrapped domain error",
			err:         fmt.Errorf("repo layer: %w", domain.ErrJobNotFound),
			wantSubstr:  "Job not found",
			wantIsError: true,
		},
		{
			name:        "unknown error falls through to default",
			err:         errors.New("something unexpected"),
			wantSubstr:  "Internal error: something unexpected",
			wantIsError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toolError(tt.err)
			if err != nil {
				t.Fatalf("toolError() returned unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("toolError() returned nil result")
			}
			if got := result.IsError; got != tt.wantIsError {
				t.Errorf("IsError = %v, want %v", got, tt.wantIsError)
			}
			if len(result.Content) == 0 {
				t.Fatal("result.Content is empty, want at least one item")
			}
			tc, ok := result.Content[0].(mcpgo.TextContent)
			if !ok {
				t.Fatalf("result.Content[0] is %T, want mcpgo.TextContent", result.Content[0])
			}
			if !strings.Contains(tc.Text, tt.wantSubstr) {
				t.Errorf("text = %q, want substring %q", tc.Text, tt.wantSubstr)
			}
		})
	}
}
