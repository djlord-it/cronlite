package mcp

import (
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestToolDefinitions(t *testing.T) {
	tools := []struct {
		name string
		fn   func() mcpgo.Tool
	}{
		{"create-job", createJobTool},
		{"list-jobs", listJobsTool},
		{"get-job", getJobTool},
		{"update-job", updateJobTool},
		{"delete-job", deleteJobTool},
		{"pause-job", pauseJobTool},
		{"resume-job", resumeJobTool},
		{"trigger-job", triggerJobTool},
		{"next-run", nextRunTool},
		{"resolve-schedule", resolveScheduleTool},
	}
	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			tool := tt.fn()
			if tool.Name != tt.name {
				t.Errorf("expected tool name %q, got %q", tt.name, tool.Name)
			}
		})
	}
}

func TestRegisterTools(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	s := server.NewMCPServer("test", "0.1.0")
	RegisterTools(s, svc)
	// If we get here without panic, registration succeeded.
}

func TestNewServer(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	s := NewServer(svc)
	if s == nil {
		t.Fatal("expected non-nil server")
	}
}
