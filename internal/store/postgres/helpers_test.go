package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
)

func TestIsDuplicateKeyError_PqError(t *testing.T) {
	err := &pq.Error{Code: "23505", Message: "duplicate key value violates unique constraint"}
	if !isDuplicateKeyError(err) {
		t.Error("expected true for *pq.Error with code 23505")
	}
}

func TestIsDuplicateKeyError_WrappedPqError(t *testing.T) {
	inner := &pq.Error{Code: "23505", Message: "duplicate key"}
	wrapped := fmt.Errorf("insert failed: %w", inner)
	if !isDuplicateKeyError(wrapped) {
		t.Error("expected true for wrapped *pq.Error with code 23505")
	}
}

func TestIsDuplicateKeyError_PqErrorOtherCode(t *testing.T) {
	err := &pq.Error{Code: "23503", Message: "foreign key violation"}
	if isDuplicateKeyError(err) {
		t.Error("expected false for *pq.Error with non-23505 code")
	}
}

func TestIsDuplicateKeyError_StringFallback(t *testing.T) {
	// Non-pq error that contains the code string — fallback path.
	err := errors.New("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)")
	if !isDuplicateKeyError(err) {
		t.Error("expected true for string-matched error containing 23505")
	}
}

func TestIsDuplicateKeyError_Nil(t *testing.T) {
	if isDuplicateKeyError(nil) {
		t.Error("expected false for nil error")
	}
}

func TestIsDuplicateKeyError_UnrelatedError(t *testing.T) {
	err := errors.New("connection refused")
	if isDuplicateKeyError(err) {
		t.Error("expected false for unrelated error")
	}
}
