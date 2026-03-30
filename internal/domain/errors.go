package domain

import "errors"

var (
	ErrJobNotFound           = errors.New("job not found")
	ErrExecutionNotFound     = errors.New("execution not found")
	ErrAPIKeyNotFound        = errors.New("api key not found")
	ErrNamespaceRequired     = errors.New("namespace required")
	ErrNamespaceMismatch     = errors.New("namespace mismatch")
	ErrInvalidCronExpression = errors.New("invalid cron expression")
	ErrInvalidTimezone       = errors.New("invalid timezone")
	ErrInvalidWebhookURL     = errors.New("invalid webhook URL")
	ErrJobDisabled           = errors.New("job is disabled")
	ErrDuplicateExecution    = errors.New("execution already exists")
	ErrDuplicateAPIKey       = errors.New("api key already exists")
	ErrScheduleParseFailure  = errors.New("could not parse schedule description")
)
