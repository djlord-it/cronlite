package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/djlord-it/easy-cron/internal/domain"
)

func TestMapDomainError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "ErrJobNotFound",
			err:        domain.ErrJobNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "job_not_found",
		},
		{
			name:       "ErrNamespaceMismatch",
			err:        domain.ErrNamespaceMismatch,
			wantStatus: http.StatusNotFound,
			wantCode:   "job_not_found",
		},
		{
			name:       "ErrExecutionNotFound",
			err:        domain.ErrExecutionNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "execution_not_found",
		},
		{
			name:       "ErrAPIKeyNotFound",
			err:        domain.ErrAPIKeyNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "api_key_not_found",
		},
		{
			name:       "ErrInvalidCronExpression",
			err:        domain.ErrInvalidCronExpression,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_cron",
		},
		{
			name:       "ErrInvalidTimezone",
			err:        domain.ErrInvalidTimezone,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_timezone",
		},
		{
			name:       "ErrInvalidWebhookURL",
			err:        domain.ErrInvalidWebhookURL,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_webhook_url",
		},
		{
			name:       "ErrScheduleParseFailure",
			err:        domain.ErrScheduleParseFailure,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "schedule_parse_failure",
		},
		{
			name:       "ErrJobDisabled",
			err:        domain.ErrJobDisabled,
			wantStatus: http.StatusConflict,
			wantCode:   "job_disabled",
		},
		{
			name:       "ErrDuplicateExecution",
			err:        domain.ErrDuplicateExecution,
			wantStatus: http.StatusConflict,
			wantCode:   "duplicate_execution",
		},
		{
			name:       "ErrNamespaceRequired",
			err:        domain.ErrNamespaceRequired,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapDomainError(tt.err)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %d, want %d", got.Status, tt.wantStatus)
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.Message != tt.err.Error() {
				t.Errorf("Message = %q, want %q", got.Message, tt.err.Error())
			}
		})
	}
}

func TestMapDomainError_WrappedError(t *testing.T) {
	wrapped := fmt.Errorf("repo layer: %w", domain.ErrJobNotFound)
	got := mapDomainError(wrapped)

	if got.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusNotFound)
	}
	if got.Code != "job_not_found" {
		t.Errorf("Code = %q, want %q", got.Code, "job_not_found")
	}
	if got.Message != wrapped.Error() {
		t.Errorf("Message = %q, want %q", got.Message, wrapped.Error())
	}
}

func TestMapDomainError_UnknownError(t *testing.T) {
	unknown := errors.New("something unexpected")
	got := mapDomainError(unknown)

	if got.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusInternalServerError)
	}
	if got.Code != "internal_error" {
		t.Errorf("Code = %q, want %q", got.Code, "internal_error")
	}
	if got.Message != "internal server error" {
		t.Errorf("Message = %q, want %q", got.Message, "internal server error")
	}
}

func TestNewErrorResponse(t *testing.T) {
	tests := []struct {
		name        string
		input       httpError
		wantCode    string
		wantMessage string
	}{
		{
			name:        "not_found",
			input:       httpError{Status: http.StatusNotFound, Code: "job_not_found", Message: "job not found"},
			wantCode:    "job_not_found",
			wantMessage: "job not found",
		},
		{
			name:        "internal_error",
			input:       httpError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal server error"},
			wantCode:    "internal_error",
			wantMessage: "internal server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newErrorResponse(tt.input)
			if got.Error.Code != tt.wantCode {
				t.Errorf("Error.Code = %q, want %q", got.Error.Code, tt.wantCode)
			}
			if got.Error.Message != tt.wantMessage {
				t.Errorf("Error.Message = %q, want %q", got.Error.Message, tt.wantMessage)
			}
			if got.Error.Details != nil {
				t.Errorf("Error.Details = %v, want nil", got.Error.Details)
			}
		})
	}
}
