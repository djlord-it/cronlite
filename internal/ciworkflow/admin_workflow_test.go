package ciworkflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

const (
	checkoutAction = "actions/checkout@11d5960a326750d5838078e36cf38b85af677262"
	setupGoAction  = "actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16"
	uploadAction   = "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"
	postgresImage  = "postgres:16-alpine@sha256:4e6e670bb069649261c9c18031f0aded7bb249a5b6664ddec29c013a89310d50"
)

type adminWorkflow struct {
	Name        string            `yaml:"name"`
	Triggers    map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Env      map[string]string `yaml:"env"`
	Defaults struct {
		Run struct {
			Shell string `yaml:"shell"`
		} `yaml:"run"`
	} `yaml:"defaults"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	RunsOn          string                     `yaml:"runs-on"`
	TimeoutMinutes  int                        `yaml:"timeout-minutes"`
	Permissions     any                        `yaml:"permissions"`
	ContinueOnError any                        `yaml:"continue-on-error"`
	Env             map[string]string          `yaml:"env"`
	Services        map[string]workflowService `yaml:"services"`
	Steps           []workflowStep             `yaml:"steps"`
	hasPermissions  bool
	hasContinueFail bool
}

func (j *workflowJob) UnmarshalYAML(unmarshal func(any) error) error {
	type plainJob workflowJob
	var decoded plainJob
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*j = workflowJob(decoded)

	var fields yaml.MapSlice
	if err := unmarshal(&fields); err != nil {
		return err
	}
	for _, field := range fields {
		key, ok := field.Key.(string)
		if !ok {
			continue
		}
		switch key {
		case "permissions":
			j.hasPermissions = true
		case "continue-on-error":
			j.hasContinueFail = true
		}
	}
	return nil
}

type workflowService struct {
	Image   string         `yaml:"image"`
	Env     map[string]any `yaml:"env"`
	Ports   []string       `yaml:"ports"`
	Options string         `yaml:"options"`
}

type workflowStep struct {
	Name            string         `yaml:"name"`
	If              string         `yaml:"if"`
	Uses            string         `yaml:"uses"`
	With            map[string]any `yaml:"with"`
	Env             map[string]any `yaml:"env"`
	Run             string         `yaml:"run"`
	ContinueOnError any            `yaml:"continue-on-error"`
	hasContinueFail bool
}

func (s *workflowStep) UnmarshalYAML(unmarshal func(any) error) error {
	type plainStep workflowStep
	var decoded plainStep
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*s = workflowStep(decoded)

	var fields yaml.MapSlice
	if err := unmarshal(&fields); err != nil {
		return err
	}
	for _, field := range fields {
		if key, ok := field.Key.(string); ok && key == "continue-on-error" {
			s.hasContinueFail = true
		}
	}
	return nil
}

type contractViolations []string

func (v *contractViolations) addf(format string, args ...any) {
	*v = append(*v, fmt.Sprintf(format, args...))
}

func (v contractViolations) err() error {
	if len(v) == 0 {
		return nil
	}
	return errors.New(strings.Join(v, "\n"))
}

func TestAdminWorkflowContract(t *testing.T) {
	data := readAdminWorkflow(t)
	if err := validateAdminWorkflow(data); err != nil {
		t.Fatal(err)
	}
}

func TestAdminWorkflowContractRejectsMutations(t *testing.T) {
	data := readAdminWorkflow(t)

	tests := []struct {
		name    string
		mutate  func([]byte) []byte
		wantErr string
	}{
		{
			name: "job permissions override",
			mutate: func(input []byte) []byte {
				return replaceOnce(
					t,
					input,
					"    runs-on: ubuntu-latest",
					"    permissions: write-all\n    runs-on: ubuntu-latest",
				)
			},
			wantErr: "must not override permissions",
		},
		{
			name: "missing smoke cleanup",
			mutate: func(input []byte) []byte {
				output := bytes.Replace(
					input,
					[]byte("down -v --remove-orphans"),
					[]byte("ps"),
					1,
				)
				if bytes.Equal(output, input) {
					output = bytes.Replace(
						input,
						[]byte("down --volumes --remove-orphans"),
						[]byte("ps"),
						1,
					)
				}
				if bytes.Equal(output, input) {
					t.Fatal("cleanup command to mutate was not found")
				}
				return output
			},
			wantErr: "cleanup must remove volumes and orphans",
		},
		{
			name: "floating checkout action",
			mutate: func(input []byte) []byte {
				return replaceOnce(t, input, checkoutAction, "actions/checkout@v4")
			},
			wantErr: "full commit SHA",
		},
		{
			name: "missing checkout action",
			mutate: func(input []byte) []byte {
				return replaceOnce(
					t,
					input,
					"      - name: Checkout\n        uses: "+checkoutAction+" # v4\n        with:\n          persist-credentials: false\n\n",
					"",
				)
			},
			wantErr: "must have exactly one checkout action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAdminWorkflow(tt.mutate(data))
			if err == nil {
				t.Fatalf("mutated workflow unexpectedly passed contract")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("contract error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func readAdminWorkflow(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", "admin-ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin workflow: %v", err)
	}
	return data
}

func replaceOnce(t *testing.T, input []byte, old, replacement string) []byte {
	t.Helper()
	output := bytes.Replace(input, []byte(old), []byte(replacement), 1)
	if bytes.Equal(output, input) {
		t.Fatalf("mutation target %q was not found", old)
	}
	return output
}

func validateAdminWorkflow(data []byte) error {
	var workflow adminWorkflow
	if err := yaml.UnmarshalStrict(data, &workflow); err != nil {
		return fmt.Errorf("parse admin workflow: %w", err)
	}

	var violations contractViolations
	validateWorkflowRoot(&violations, workflow)
	validateJobs(&violations, workflow.Jobs)
	validateActions(&violations, workflow.Jobs)
	validateUnitJob(&violations, workflow.Jobs["admin-unit-security"])
	validateIntegrationJob(&violations, workflow.Jobs["admin-postgres-integration"])
	validateAssetsJob(&violations, workflow.Jobs["admin-assets-launcher"])
	validateSmokeJob(&violations, workflow.Jobs["admin-smoke"])
	return violations.err()
}

func validateWorkflowRoot(v *contractViolations, workflow adminWorkflow) {
	if workflow.Name != "Admin CI" {
		v.addf("workflow name must be Admin CI")
	}
	if got := sortedKeys(workflow.Triggers); !equalStrings(got, []string{"pull_request", "push"}) {
		v.addf("workflow triggers must be exactly push and pull_request, got %v", got)
	}
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		v.addf("root permissions must be exactly contents: read")
	}
	if workflow.Concurrency.Group != "admin-ci-${{ github.workflow }}-${{ github.ref }}" {
		v.addf("concurrency group must be scoped to workflow and ref")
	}
	if !workflow.Concurrency.CancelInProgress {
		v.addf("concurrency must cancel superseded runs")
	}
	if workflow.Env["GO_VERSION"] != "1.25.8" {
		v.addf("GO_VERSION must be 1.25.8")
	}
	if workflow.Defaults.Run.Shell != "bash" {
		v.addf("default run shell must be bash")
	}
}

func validateJobs(v *contractViolations, jobs map[string]workflowJob) {
	expected := map[string]int{
		"admin-assets-launcher":      15,
		"admin-postgres-integration": 15,
		"admin-smoke":                25,
		"admin-unit-security":        15,
	}
	if got := sortedKeys(jobs); !equalStrings(got, sortedKeys(expected)) {
		v.addf("top-level jobs must be exactly %v, got %v", sortedKeys(expected), got)
	}
	for name, timeout := range expected {
		job, ok := jobs[name]
		if !ok {
			continue
		}
		if job.RunsOn != "ubuntu-latest" {
			v.addf("%s must run on ubuntu-latest", name)
		}
		if job.TimeoutMinutes != timeout {
			v.addf("%s timeout must be %d minutes", name, timeout)
		}
		if job.hasPermissions {
			v.addf("%s must not override permissions", name)
		}
		if job.hasContinueFail {
			v.addf("%s must not set continue-on-error", name)
		}
		for _, step := range job.Steps {
			if step.hasContinueFail {
				v.addf("%s step %q must not set continue-on-error", name, step.Name)
			}
		}
	}
}

func validateActions(v *contractViolations, jobs map[string]workflowJob) {
	fullSHA := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	expectedUploads := map[string]int{
		"admin-assets-launcher":      0,
		"admin-postgres-integration": 1,
		"admin-smoke":                1,
		"admin-unit-security":        1,
	}
	for jobName, job := range jobs {
		checkoutCount := 0
		setupGoCount := 0
		uploadCount := 0
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			if !fullSHA.MatchString(step.Uses) {
				v.addf("%s step %q uses reference must be a full commit SHA", jobName, step.Name)
				continue
			}
			switch {
			case strings.HasPrefix(step.Uses, "actions/checkout@"):
				checkoutCount++
				if step.Uses != checkoutAction {
					v.addf("%s checkout action must use the approved SHA", jobName)
				}
				persist, exists := step.With["persist-credentials"]
				if !exists {
					v.addf("%s checkout must set persist-credentials: false", jobName)
				} else if value, ok := persist.(bool); !ok || value {
					v.addf("%s checkout persist-credentials must be false", jobName)
				}
			case strings.HasPrefix(step.Uses, "actions/setup-go@"):
				setupGoCount++
				if step.Uses != setupGoAction {
					v.addf("%s setup-go action must use the approved SHA", jobName)
				}
				if got := fmt.Sprint(step.With["go-version"]); got != "${{ env.GO_VERSION }}" {
					v.addf("%s setup-go must use the root GO_VERSION", jobName)
				}
				cache, ok := step.With["cache"].(bool)
				if !ok || !cache {
					v.addf("%s setup-go cache must be enabled", jobName)
				}
			case strings.HasPrefix(step.Uses, "actions/upload-artifact@"):
				uploadCount++
				if step.Uses != uploadAction {
					v.addf("%s upload-artifact action must use the approved SHA", jobName)
				}
			}
		}
		if checkoutCount != 1 {
			v.addf("%s must have exactly one checkout action", jobName)
		}
		if setupGoCount != 1 {
			v.addf("%s must have exactly one setup-go action", jobName)
		}
		if uploadCount != expectedUploads[jobName] {
			v.addf("%s must have exactly %d artifact uploads", jobName, expectedUploads[jobName])
		}
	}
}

func validateUnitJob(v *contractViolations, job workflowJob) {
	run := findStep(job, "Run admin unit and security suite")
	requireRunContains(v, "unit suite", run,
		"ADMIN_COVERAGE_FILE=admin-coverage.out ./scripts/admin_ci_test.sh")

	upload := findStep(job, "Upload admin coverage")
	requireAction(v, "unit coverage upload", upload, uploadAction)
	if upload.If != "always()" {
		v.addf("unit coverage upload must run always")
	}
	requireWith(v, "unit coverage upload", upload, "path", "admin-coverage.out")
	requireWith(v, "unit coverage upload", upload, "if-no-files-found", "error")
	requireWith(v, "unit coverage upload", upload, "retention-days", "7")
}

func validateIntegrationJob(v *contractViolations, job workflowJob) {
	postgres, ok := job.Services["postgres"]
	if !ok || len(job.Services) != 1 {
		v.addf("integration job must define exactly one postgres service")
	} else {
		if postgres.Image != postgresImage {
			v.addf("integration postgres service must use the approved pinned digest")
		}
		if !equalStrings(postgres.Ports, []string{"5432/tcp"}) {
			v.addf("integration postgres service must expose dynamic 5432/tcp")
		}
		if !strings.Contains(postgres.Options, "--health-cmd") {
			v.addf("integration postgres service must have a health check")
		}
	}

	migration := findStep(job, "Create fresh database and apply all schemas")
	requireRunContains(v, "integration migration", migration,
		"CREATE DATABASE cronlite_admin_ci",
		"schema/*.sql",
		"ON_ERROR_STOP=1",
		"docker exec -i",
	)

	integration := findStep(job, "Run PostgreSQL integration suite")
	requireRunContains(v, "integration suite", integration,
		"set -euo pipefail",
		"go test -json -tags=integration -race",
		"tee /tmp/admin-integration.json",
		"ADMIN_INTEGRATION_OK",
		"selected",
		"passed",
		"skipped",
		"marker",
		"cmp -s",
	)
	if strings.Contains(integration.Run, "-ne 5") || strings.Contains(integration.Run, "of 5") {
		v.addf("integration suite must not hard-code the marker count")
	}

	diagnostics := findStep(job, "Collect sanitized integration diagnostics")
	if diagnostics.If != "failure()" {
		v.addf("integration diagnostics collection must run only on failure")
	}
	requireRunContains(v, "integration diagnostics", diagnostics,
		"admin-integration-diagnostics",
		"jq -r",
		"REDACTED_API_KEY",
		"REDACTED_DATABASE_URL",
		"REDACTED_PASSWORD",
		"integration.log",
		"database.txt",
	)

	upload := findStep(job, "Upload integration diagnostics")
	requireFailureUpload(v, "integration diagnostics upload", upload)
	requireWith(v, "integration diagnostics upload", upload, "path", "/tmp/admin-integration-diagnostics")
}

func validateAssetsJob(v *contractViolations, job workflowJob) {
	syntax := findStep(job, "Check launcher and smoke shell syntax")
	requireRunContains(v, "asset shell syntax", syntax,
		"bash -n",
		"scripts/admin-local.sh",
		"scripts/admin_smoke_test.sh",
	)
	requireRunContains(v, "launcher contract", findStep(job, "Run launcher contract"),
		"./scripts/admin_local_test.sh --all")
	requireRunContains(v, "smoke contract", findStep(job, "Run smoke contract"),
		"./scripts/admin_smoke_test_test.sh")
	requireRunContains(v, "actionlint", findStep(job, "Lint GitHub Actions workflow"),
		"github.com/rhysd/actionlint/cmd/actionlint@v1.7.7",
		".github/workflows/admin-ci.yml",
	)
	requireRunContains(v, "focused assets", findStep(job, "Run focused template, asset, and security header tests"),
		"go test ./internal/webadmin",
		"Templates|Assets|SecurityHeaders",
	)
}

func validateSmokeJob(v *contractViolations, job workflowJob) {
	if job.Env["COMPOSE_PROJECT_NAME"] != "cronlite-admin-${{ github.run_id }}-${{ github.run_attempt }}" {
		v.addf("smoke compose project must be unique to run_id and run_attempt")
	}

	builds := findStep(job, "Cross-compile admin binaries")
	for _, target := range []string{
		"GOOS=linux GOARCH=amd64",
		"GOOS=linux GOARCH=arm64",
		"GOOS=darwin GOARCH=amd64",
		"GOOS=darwin GOARCH=arm64",
	} {
		requireRunContains(v, "smoke cross-compile", builds, "CGO_ENABLED=0", target)
	}
	requireRunContains(v, "smoke image build", findStep(job, "Build admin smoke image"),
		"docker build --tag cronlite:admin-ci .")

	token := findStep(job, "Generate masked bootstrap token")
	requireRunContains(v, "smoke bootstrap token", token,
		"openssl rand -hex 32",
		"::add-mask::",
		"GITHUB_ENV",
	)
	if strings.Contains(token.Run, "echo ") || strings.Contains(token.Run, "set -x") {
		v.addf("smoke bootstrap token step must not echo or trace the token")
	}

	requireRunContains(v, "smoke stack start", findStep(job, "Start admin smoke stack"),
		"up --detach --wait --no-build")
	requireRunContains(v, "smoke lifecycle", findStep(job, "Run admin smoke lifecycle"),
		"port cronlite 8080",
		"./scripts/admin_smoke_test.sh",
	)

	diagnostics := findStep(job, "Collect sanitized smoke diagnostics")
	if diagnostics.If != "failure()" {
		v.addf("smoke diagnostics collection must run only on failure")
	}
	requireRunContains(v, "smoke diagnostics", diagnostics,
		"admin-smoke-diagnostics",
		"compose",
		"ps",
		"logs --no-color",
		"REDACTED_BOOTSTRAP_TOKEN",
		"REDACTED_API_KEY",
	)
	if strings.Contains(diagnostics.Run, "docker inspect") {
		v.addf("smoke diagnostics must not collect docker inspect output")
	}
	upload := findStep(job, "Upload smoke diagnostics")
	requireFailureUpload(v, "smoke diagnostics upload", upload)
	requireWith(v, "smoke diagnostics upload", upload, "path", "/tmp/admin-smoke-diagnostics")

	cleanup := findStep(job, "Stop admin smoke stack")
	if cleanup.If != "always()" {
		v.addf("smoke cleanup must run always")
	}
	if !strings.Contains(cleanup.Run, "down -v --remove-orphans") {
		v.addf("smoke cleanup must remove volumes and orphans")
	}
}

func requireFailureUpload(v *contractViolations, label string, step workflowStep) {
	requireAction(v, label, step, uploadAction)
	if step.If != "failure()" {
		v.addf("%s must run only on failure", label)
	}
	requireWith(v, label, step, "if-no-files-found", "error")
	requireWith(v, label, step, "retention-days", "7")
}

func requireAction(v *contractViolations, label string, step workflowStep, action string) {
	if step.Uses != action {
		v.addf("%s must use %s", label, action)
	}
}

func requireWith(v *contractViolations, label string, step workflowStep, key, want string) {
	if got := fmt.Sprint(step.With[key]); got != want {
		v.addf("%s with.%s must be %s, got %s", label, key, want, got)
	}
}

func requireRunContains(v *contractViolations, label string, step workflowStep, needles ...string) {
	if step.Name == "" {
		v.addf("%s step is missing", label)
		return
	}
	for _, needle := range needles {
		if !strings.Contains(step.Run, needle) {
			v.addf("%s run command must contain %q", label, needle)
		}
	}
}

func findStep(job workflowJob, name string) workflowStep {
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	return workflowStep{}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
