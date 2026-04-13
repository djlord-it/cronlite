package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

// Pagination defaults and limits.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// HealthChecker provides database health status for the /health endpoint.
type HealthChecker interface {
	PingContext(ctx context.Context) error
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: json encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
