package api

// ErrorResponse is used by writeError for middleware error responses.
type ErrorResponse struct {
	Error string `json:"error"`
}
