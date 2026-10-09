package ciworkflow

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

// Exercise the actual CI query so changes cannot turn a skipped/empty suite green,
// or reject a newly added passing test merely because it lacks a log message.
func TestIntegrationGateUsesTestResults(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required to exercise the Actions integration gate")
	}
	var workflow adminWorkflow
	if err := yaml.UnmarshalStrict(readAdminWorkflow(t), &workflow); err != nil {
		t.Fatal(err)
	}
	run := findStep(workflow.Jobs["admin-postgres-integration"], "Run PostgreSQL integration suite").Run
	match := regexp.MustCompile(`(?s)jq -e -s '([^']+)'`).FindStringSubmatch(run)
	if len(match) != 2 {
		t.Fatal("integration result query missing")
	}
	const passing = `{"Action":"run","Test":"TestIntegrationNewWorkflow"}
{"Action":"pass","Test":"TestIntegrationNewWorkflow"}
{"Action":"pass","Package":"webadmin"}`
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"passing test without custom marker", passing, true},
		{"no selected tests", `{"Action":"pass","Package":"webadmin"}`, false},
		{"failed test", `{"Action":"run","Test":"TestIntegrationNewWorkflow"}
{"Action":"fail","Test":"TestIntegrationNewWorkflow"}`, false},
		{"skipped subtest", passing + "\n" + `{"Action":"skip","Test":"TestIntegrationNewWorkflow/branch"}`, false},
		{"skipped top-level test", `{"Action":"run","Test":"TestIntegrationNewWorkflow"}
{"Action":"skip","Test":"TestIntegrationNewWorkflow"}`, false},
		{"truncated results", `{"Action":"run","Test":"TestIntegrationNewWorkflow"}`, false},
		{"malformed stream", "not JSON", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(jq, "-e", "-s", match[1])
			cmd.Stdin = strings.NewReader(tc.input)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%t want %t: %s", err == nil, tc.want, output)
			}
		})
	}
}
