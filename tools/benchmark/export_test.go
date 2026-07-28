package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCSVHasOneRowPerAttemptAndStableHeader(t *testing.T) {
	result := fixtureRunResult()
	result.Scenarios[0].Executions[0].Diagnostic = &DiagnosticExecution{
		Attempts: []DiagnosticAttempt{
			{ID: "attempt-1", Attempt: 1, StatusCode: 500, Error: "server"},
			{ID: "attempt-2", Attempt: 2, StatusCode: 204},
		},
	}
	var out bytes.Buffer
	if err := writeCSV(&out, result); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(out.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("want header plus two attempts, got %d rows", len(rows))
	}
	wantPrefix := []string{
		"run_id",
		"scenario",
		"correlation_id",
		"job_id",
		"execution_id",
		"attempt_id",
		"attempt",
		"status_code",
	}
	for index, want := range wantPrefix {
		if rows[0][index] != want {
			t.Fatalf("header[%d]: want %q got %q", index, want, rows[0][index])
		}
	}
}

func TestWriteOutputsCreatesAllRequiredFiles(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeOutputs(dir, fixtureRunResult())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.JSON, paths.CSV, paths.Markdown} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestJSONOutputDoesNotExposeSecrets(t *testing.T) {
	result := fixtureRunResult()
	result.Command = `benchmark --api-key "secret-api-key"`
	result.Scenarios[0].Reason = "postgres://user:password@localhost/db"
	var out bytes.Buffer
	if err := writeJSON(&out, result); err != nil {
		t.Fatal(err)
	}
	if json.Valid(out.Bytes()) == false {
		t.Fatalf("invalid JSON: %s", out.Bytes())
	}
	for _, secret := range []string{"secret-api-key", "password"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("output exposed %q: %s", secret, out.String())
		}
	}
}

func TestAtomicWriteReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "result.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, func(out *bytes.Buffer) error {
		out.WriteString("new")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Fatalf("body = %q", body)
	}
}
