package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestMCPMalformedTagsCannotMutateJobs(t *testing.T) {
	for _, tool := range []string{"create-job", "update-job"} {
		for _, tags := range []string{`true`, `"wrong"`, `{"env":42}`} {
			t.Run(tool+tags, func(t *testing.T) {
				writes := 0
				jr := &mockJobRepo{
					insertJobFn: func(context.Context, domain.Job, domain.Schedule) error { writes++; return nil },
					updateJobFn: func(context.Context, domain.Job) error { writes++; return nil },
					getJobWithScheduleFn: func(context.Context, uuid.UUID) (domain.Job, domain.Schedule, error) {
						return domain.Job{Namespace: "test"}, domain.Schedule{}, nil
					},
				}
				s := NewServer(newTestService(jr, nil, nil, nil))
				request := json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"id":"12345678-1234-1234-1234-123456789012","name":"test","cron_expression":"* * * * *","timezone":"UTC","webhook_url":"https://example.com","tags":%s}}}`, tool, tags))
				response := s.HandleMessage(domain.NamespaceToContext(context.Background(), "test"), request)
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				if writes != 0 || !strings.Contains(string(encoded), `"isError":true`) {
					t.Fatalf("malformed tags reached persistence: writes=%d response=%s", writes, encoded)
				}
			})
		}
	}
}

func TestMCPMalformedUpdateReturnsToolError(t *testing.T) {
	for _, field := range []string{"name", "cron_expression", "timezone", "webhook_url", "webhook_secret", "webhook_timeout_seconds"} {
		t.Run(field, func(t *testing.T) {
			s := NewServer(newTestService(nil, nil, nil, nil))
			request := json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update-job","arguments":{"id":"12345678-1234-1234-1234-123456789012",%q:true}}}`, field))
			response := s.HandleMessage(domain.NamespaceToContext(context.Background(), "test"), request)
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"isError":true`) || strings.Contains(string(encoded), "panic") {
				t.Fatalf("malformed input was not a controlled tool error: %s", encoded)
			}
		})
	}
}

func TestMCPRejectsUnrepresentableTimeout(t *testing.T) {
	for _, tool := range []string{"create-job", "update-job"} {
		for _, seconds := range []string{`9223372037`, `"NaN"`, `"+Inf"`, `-1`} {
			t.Run(tool+seconds, func(t *testing.T) {
				s := NewServer(newTestService(nil, nil, nil, nil))
				request := json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"id":"12345678-1234-1234-1234-123456789012","name":"test","cron_expression":"* * * * *","timezone":"UTC","webhook_url":"https://example.com","webhook_timeout_seconds":%s}}}`, tool, seconds))
				response := s.HandleMessage(domain.NamespaceToContext(context.Background(), "test"), request)
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(encoded), `"isError":true`) {
					t.Fatalf("unsafe timeout accepted: %s", encoded)
				}
			})
		}
	}
}
