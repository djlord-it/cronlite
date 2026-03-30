package api

import (
	"errors"
	"net/http"

	"github.com/djlord-it/easy-cron/internal/domain"
)

// httpError holds the HTTP status code and machine-readable error code
// derived from a domain-layer error.
type httpError struct {
	Status  int
	Code    string
	Message string
}

// mapDomainError converts a domain error into an httpError.
// Unknown errors map to 500 / "internal_error".
func mapDomainError(err error) httpError {
	switch {
	case errors.Is(err, domain.ErrJobNotFound), errors.Is(err, domain.ErrNamespaceMismatch):
		return httpError{Status: http.StatusNotFound, Code: "job_not_found", Message: err.Error()}
	case errors.Is(err, domain.ErrExecutionNotFound):
		return httpError{Status: http.StatusNotFound, Code: "execution_not_found", Message: err.Error()}
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		return httpError{Status: http.StatusNotFound, Code: "api_key_not_found", Message: err.Error()}
	case errors.Is(err, domain.ErrInvalidCronExpression):
		return httpError{Status: http.StatusUnprocessableEntity, Code: "invalid_cron", Message: err.Error()}
	case errors.Is(err, domain.ErrInvalidTimezone):
		return httpError{Status: http.StatusUnprocessableEntity, Code: "invalid_timezone", Message: err.Error()}
	case errors.Is(err, domain.ErrInvalidWebhookURL):
		return httpError{Status: http.StatusUnprocessableEntity, Code: "invalid_webhook_url", Message: err.Error()}
	case errors.Is(err, domain.ErrScheduleParseFailure):
		return httpError{Status: http.StatusUnprocessableEntity, Code: "schedule_parse_failure", Message: err.Error()}
	case errors.Is(err, domain.ErrJobDisabled):
		return httpError{Status: http.StatusConflict, Code: "job_disabled", Message: err.Error()}
	case errors.Is(err, domain.ErrDuplicateExecution):
		return httpError{Status: http.StatusConflict, Code: "duplicate_execution", Message: err.Error()}
	case errors.Is(err, domain.ErrNamespaceRequired):
		return httpError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: err.Error()}
	default:
		return httpError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal server error"}
	}
}

// newErrorResponse builds the generated Error response struct from an httpError.
func newErrorResponse(he httpError) Error {
	return Error{
		Error: struct {
			Code    string                  `json:"code"`
			Details *map[string]interface{} `json:"details,omitempty"`
			Message string                  `json:"message"`
		}{
			Code:    he.Code,
			Message: he.Message,
		},
	}
}
