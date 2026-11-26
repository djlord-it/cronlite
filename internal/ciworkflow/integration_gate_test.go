package ciworkflow

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestCoreIntegrationGateRequiresEveryPackage(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required to exercise the Actions integration gate")
	}
	var workflow adminWorkflow
	if err := yaml.UnmarshalStrict(readAdminWorkflow(t), &workflow); err != nil {
		t.Fatal(err)
	}
	run := findStep(workflow.Jobs["admin-postgres-integration"], "Run dispatch, leader election and migration integration suites").Run
	match := regexp.MustCompile(`(?s)jq -e -s --argjson expected '([^']+)' '([^']+)'`).FindStringSubmatch(run)
	if len(match) != 3 {
		t.Fatal("core integration result query missing")
	}
	var packages []string
	if err := json.Unmarshal([]byte(match[1]), &packages); err != nil {
		t.Fatal(err)
	}
	const prefix = "github.com/djlord-it/cronlite/"
	expected := []string{prefix + "internal/store/postgres", prefix + "internal/leaderelection", prefix + "internal/migrate", prefix + "cmd/cronlite"}
	if !equalStrings(packages, expected) {
		t.Fatalf("core gate must require all database packages: %v", packages)
	}
	var passing []string
	for _, pkg := range packages {
		for _, event := range []map[string]string{
			{"Package": pkg, "Action": "run", "Test": "TestNewDatabaseScenario"},
			{"Package": pkg, "Action": "pass", "Test": "TestNewDatabaseScenario"},
			{"Package": pkg, "Action": "pass"},
		} {
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			passing = append(passing, string(data))
		}
	}
	valid := strings.Join(passing, "\n")
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"all packages pass", valid, true},
		{"empty stream", "", false},
		{"missing package", strings.Join(passing[3:], "\n"), false},
		{"incomplete test", valid + "\n" + `{"Package":"` + packages[0] + `","Action":"run","Test":"TestUnfinished"}`, false},
		{"skipped subtest", valid + "\n" + `{"Action":"skip","Test":"TestNewDatabaseScenario/branch"}`, false},
		{"failed package", valid + "\n" + `{"Action":"fail"}`, false},
		{"malformed stream", "not JSON", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(jq, "-e", "-s", "--argjson", "expected", match[1], match[2])
			cmd.Stdin = strings.NewReader(tc.input)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%t want %t: %s", err == nil, tc.want, output)
			}
		})
	}
}

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
