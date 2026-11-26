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

const postgresImage = "postgres:16-alpine@sha256:4e6e670bb069649261c9c18031f0aded7bb249a5b6664ddec29c013a89310d50"

type adminWorkflow struct {
	Name        string            `yaml:"name"`
	Triggers    map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress string `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Env      map[string]string `yaml:"env"`
	Defaults struct {
		Run struct {
			Shell string `yaml:"shell"`
		} `yaml:"run"`
	} `yaml:"defaults"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

// fullPipelineOnly is the only job condition allowed for jobs that docs-only
// pull requests may skip; see TestDocsOnlySkipsMatchGates.
const fullPipelineOnly = "needs.changes.outputs.full == 'true'"

type workflowJob struct {
	Name            string                     `yaml:"name"`
	If              string                     `yaml:"if"`
	Needs           any                        `yaml:"needs"`
	Outputs         map[string]string          `yaml:"outputs"`
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

func TestCrossBuildPreservesAllAdminTargets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Jobs map[string]yaml.MapSlice `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	fields, ok := root.Jobs["cross-build"]
	if !ok {
		t.Fatal("cross-build job is required")
	}
	for _, field := range fields {
		if field.Key == "if" && field.Value != fullPipelineOnly {
			t.Fatalf("cross-build may only be skipped for docs-only changes, got %v", field.Value)
		}
	}
	encoded, err := yaml.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var job workflowJob
	if err := yaml.UnmarshalStrict(encoded, &job); err != nil {
		t.Fatal(err)
	}
	var violations contractViolations
	validateActions(&violations, map[string]workflowJob{"cross-build": job})
	if job.hasContinueFail || job.TimeoutMinutes <= 0 || job.RunsOn != "ubuntu-latest" {
		violations.addf("cross-build must fail closed on an Ubuntu runner with a timeout")
	}
	builds := findStep(job, "Cross-compile admin binaries")
	if builds.If != "" || builds.hasContinueFail {
		violations.addf("cross-build must execute every target and propagate errors")
	}
	for _, target := range []string{
		"GOOS=linux GOARCH=amd64",
		"GOOS=linux GOARCH=arm64",
		"GOOS=darwin GOARCH=amd64",
		"GOOS=darwin GOARCH=arm64",
	} {
		requireRunContains(&violations, "cross-compile", builds, "CGO_ENABLED=0", target, "./cmd/cronlite")
	}
	if err := violations.err(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminWorkflowContractAcceptsActionPinUpdates(t *testing.T) {
	data := readAdminWorkflow(t)
	updated := regexp.MustCompile(`@[0-9a-f]{40}`).ReplaceAll(data, []byte("@"+strings.Repeat("a", 40)))
	if bytes.Equal(data, updated) {
		t.Fatal("no action pins updated")
	}
	if err := validateAdminWorkflow(updated); err != nil {
		t.Fatalf("updating pinned actions must not require test edits: %v", err)
	}
}

func TestAdminCIScriptRunsEveryFuzzHarness(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "admin_ci_test.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin CI script: %v", err)
	}

	script := string(data)
	for _, harness := range []string{"FuzzParseTags", "FuzzPositivePage", "FuzzPathUUID"} {
		want := "-fuzz=" + harness + " -fuzztime=5s"
		if !strings.Contains(script, want) {
			t.Errorf("admin CI script does not run %s with a fixed fuzz time", harness)
		}
	}
}

func TestAdminPostgresJSONStreamExcludesStderr(t *testing.T) {
	var workflow adminWorkflow
	if err := yaml.UnmarshalStrict(readAdminWorkflow(t), &workflow); err != nil {
		t.Fatalf("parse admin workflow: %v", err)
	}

	step := findStep(workflow.Jobs["admin-postgres-integration"], "Run PostgreSQL integration suite")
	if strings.Contains(step.Run, "2>&1") {
		t.Fatal("PostgreSQL integration suite merges stderr into the go test JSON stream")
	}
}

func TestMainCIPinsOpenAPIGenerator(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read main CI workflow: %v", err)
	}

	workflow := string(data)
	const generator = "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
	if strings.Contains(workflow, generator+"@latest") {
		t.Fatal("main CI installs a floating OpenAPI generator version")
	}
	if !strings.Contains(workflow, generator+"@v2.7.0") {
		t.Fatal("main CI does not pin the OpenAPI generator version used by committed generated code")
	}
}

func TestDocumentedManualMigrationsFailClosed(t *testing.T) {
	for _, name := range []string{"README.md", "OPERATORS.md"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			document := string(data)
			if strings.Contains(document, `psql cronlite < "$f"`) {
				t.Fatalf("%s documents fail-open migrations without ON_ERROR_STOP", name)
			}
			if !strings.Contains(document, `psql -v ON_ERROR_STOP=1 cronlite < "$f"`) {
				t.Fatalf("%s does not document fail-closed psql migrations", name)
			}
		})
	}
}

func TestRateLimitProxyGuidanceIsExplicit(t *testing.T) {
	for _, name := range []string{"README.md", "OPERATORS.md"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			document := string(data)
			for _, want := range []string{"RemoteAddr", "trusted proxy", "defense-in-depth"} {
				if !strings.Contains(document, want) {
					t.Errorf("%s does not contain %q rate-limit guidance", name, want)
				}
			}
		})
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
				return replaceActionReference(t, input, "actions/checkout", "actions/checkout@v4")
			},
			wantErr: "full commit SHA",
		},
		{
			name: "floating setup-go action",
			mutate: func(input []byte) []byte {
				return replaceActionReference(t, input, "actions/setup-go", "actions/setup-go@v7")
			},
			wantErr: "full commit SHA",
		},
		{
			name: "floating upload action",
			mutate: func(input []byte) []byte {
				return replaceActionReference(t, input, "actions/upload-artifact", "actions/upload-artifact@v7")
			},
			wantErr: "full commit SHA",
		},
		{
			name: "foreign checkout action",
			mutate: func(input []byte) []byte {
				return replaceActionReference(t, input, "actions/checkout", "other/checkout@"+strings.Repeat("a", 40))
			},
			wantErr: "must have exactly one checkout action",
		},
		{
			name: "persisted checkout credentials",
			mutate: func(input []byte) []byte {
				return replaceOnce(t, input, "persist-credentials: false", "persist-credentials: true")
			},
			wantErr: "persist-credentials must be false",
		},
		{
			name: "missing checkout action",
			mutate: func(input []byte) []byte {
				pattern := regexp.MustCompile(`(?m)^\s*- name: Checkout\n\s*uses: actions/checkout@[^\n]+\n\s*with:\n\s*persist-credentials: false\n`)
				output := pattern.ReplaceAll(input, nil)
				if bytes.Equal(output, input) {
					t.Fatal("checkout step to remove was not found")
				}
				return output
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
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin workflow: %v", err)
	}
	projected, err := projectAdminWorkflow(data)
	if err != nil {
		t.Fatalf("select admin jobs from CI: %v", err)
	}
	return projected
}

// Decode the complete execution graph generically, then apply the strict admin
// schema to the preserved admin jobs. Other CI jobs have their own contracts.
func projectAdminWorkflow(data []byte) ([]byte, error) {
	var root yaml.MapSlice
	if err := yaml.UnmarshalStrict(data, &root); err != nil {
		return nil, err
	}
	for i := range root {
		// yaml.v2 treats the Actions "on" key as YAML 1.1 boolean true
		// when decoding generically; restore its Actions spelling on projection.
		if root[i].Key == true {
			root[i].Key = "on"
		}
		if root[i].Key != "jobs" {
			continue
		}
		jobs, ok := root[i].Value.(yaml.MapSlice)
		if !ok {
			return nil, fmt.Errorf("jobs must be a mapping")
		}
		var adminJobs yaml.MapSlice
		for _, job := range jobs {
			switch job.Key {
			case "admin-assets-launcher", "admin-postgres-integration", "admin-smoke", "admin-unit-security":
				adminJobs = append(adminJobs, job)
			}
		}
		root[i].Value = adminJobs
	}
	return yaml.Marshal(root)
}

func replaceOnce(t *testing.T, input []byte, old, replacement string) []byte {
	t.Helper()
	output := bytes.Replace(input, []byte(old), []byte(replacement), 1)
	if bytes.Equal(output, input) {
		t.Fatalf("mutation target %q was not found", old)
	}
	return output
}

func replaceActionReference(t *testing.T, input []byte, action, replacement string) []byte {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(action) + `@[^\s]+`)
	old := pattern.Find(input)
	if old == nil {
		t.Fatalf("action %s to mutate was not found", action)
	}
	return replaceOnce(t, input, string(old), replacement)
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
	// Unit/security and assets jobs read README, OPERATORS and schema/README,
	// so they must run for docs-only changes. Image-backed jobs may skip them.
	for name, condition := range map[string]string{
		"admin-unit-security":        "",
		"admin-assets-launcher":      "",
		"admin-postgres-integration": fullPipelineOnly,
		"admin-smoke":                fullPipelineOnly,
	} {
		if workflow.Jobs[name].If != condition {
			violations.addf("%s condition must be %q", name, condition)
		}
	}
	return violations.err()
}

func validateWorkflowRoot(v *contractViolations, workflow adminWorkflow) {
	if workflow.Name != "CI" {
		v.addf("workflow name must be CI")
	}
	if got := sortedKeys(workflow.Triggers); !equalStrings(got, []string{"pull_request", "push", "workflow_dispatch"}) {
		v.addf("workflow triggers must be exactly push, pull_request and workflow_dispatch, got %v", got)
	}
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		v.addf("root permissions must be exactly contents: read")
	}
	if workflow.Concurrency.Group != "ci-${{ github.workflow }}-${{ github.ref }}" {
		v.addf("concurrency group must be scoped to workflow and ref")
	}
	if workflow.Concurrency.CancelInProgress != "${{ github.event_name == 'pull_request' }}" {
		v.addf("concurrency must cancel superseded pull request runs only")
	}
	if workflow.Env["GO_VERSION"] != "1.27.4" {
		v.addf("GO_VERSION must be 1.27.4")
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
	for jobName := range jobs {
		job := jobs[jobName]
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
				persist, exists := step.With["persist-credentials"]
				if !exists {
					v.addf("%s checkout must set persist-credentials: false", jobName)
				} else if value, ok := persist.(bool); !ok || value {
					v.addf("%s checkout persist-credentials must be false", jobName)
				}
			case strings.HasPrefix(step.Uses, "actions/setup-go@"):
				setupGoCount++
				if got := fmt.Sprint(step.With["go-version"]); got != "${{ env.GO_VERSION }}" {
					v.addf("%s setup-go must use the root GO_VERSION", jobName)
				}
				cache, ok := step.With["cache"].(bool)
				if !ok || !cache {
					v.addf("%s setup-go cache must be enabled", jobName)
				}
			case strings.HasPrefix(step.Uses, "actions/upload-artifact@"):
				uploadCount++
			case strings.HasPrefix(step.Uses, "actions/download-artifact@"):
				if jobName != "admin-smoke" {
					v.addf("%s must not download the smoke image", jobName)
				}
			default:
				v.addf("%s step %q uses an unsupported action", jobName, step.Name)
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
	requireAction(v, "unit coverage upload", upload, "actions/upload-artifact")
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
		"ADMIN_TEST_DATABASE_URL:?",
		"jq -e -s",
	)

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
		"scripts/dev.sh",
		"scripts/admin_smoke_test.sh",
	)
	requireRunContains(v, "launcher contract", findStep(job, "Run launcher contract"),
		"./scripts/dev_test.sh --all")
	requireRunContains(v, "smoke contract", findStep(job, "Run smoke contract"),
		"./scripts/admin_smoke_test_test.sh")
	requireRunContains(v, "actionlint", findStep(job, "Lint GitHub Actions workflow"),
		"github.com/rhysd/actionlint/cmd/actionlint@v1.7.7",
	)
	requireRunContains(v, "container contract", findStep(job, "Run container contract"),
		"./scripts/container_contract_test.sh")
}

func validateSmokeJob(v *contractViolations, job workflowJob) {
	if job.Env["COMPOSE_PROJECT_NAME"] != "cronlite-admin-${{ github.run_id }}-${{ github.run_attempt }}" {
		v.addf("smoke compose project must be unique to run_id and run_attempt")
	}

	requireRunContains(v, "smoke image load", findStep(job, "Build admin smoke image"),
		"docker load --input /tmp/cronlite-image.tar",
		"docker tag cronlite:ci cronlite:admin-ci")
	if needs := fmt.Sprint(job.Needs); needs != "[changes image]" {
		v.addf("smoke must require the image build")
	}
	downloads := 0
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") {
			downloads++
			requireWith(v, "smoke image download", step, "name", "cronlite-image")
			requireWith(v, "smoke image download", step, "path", "/tmp")
		}
	}
	if downloads != 1 {
		v.addf("smoke must download exactly one shared image artifact")
	}

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
	requireAction(v, label, step, "actions/upload-artifact")
	if step.If != "failure()" {
		v.addf("%s must run only on failure", label)
	}
	requireWith(v, label, step, "if-no-files-found", "error")
	requireWith(v, label, step, "retention-days", "7")
}

func requireAction(v *contractViolations, label string, step workflowStep, action string) {
	if !strings.HasPrefix(step.Uses, action+"@") {
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

// Every job a docs-only pull request may skip must say so with the exact
// classification condition, and both gates must list exactly those jobs as
// skippable. Any drift could let a skipped verification look green.
func TestDocsOnlySkipsMatchGates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var violations contractViolations
	conditional := map[string]bool{}
	for name := range root.Jobs {
		job := root.Jobs[name]
		switch job.If {
		case fullPipelineOnly:
			conditional[name] = true
			if !strings.Contains(fmt.Sprint(job.Needs), "changes") {
				violations.addf("%s must need changes to read its classification", name)
			}
		case "always()":
			if name != "build" && name != "ci-ok" {
				violations.addf("only aggregate gates may run unconditionally, got %s", name)
			}
		case "":
		default:
			violations.addf("%s has unsupported condition %q", name, job.If)
		}
	}
	if output := root.Jobs["changes"].Outputs["full"]; output != "${{ steps.classify.outputs.full }}" {
		violations.addf("changes must expose the classifier output, got %q", output)
	}
	if root.Jobs["changes"].If != "" {
		violations.addf("changes must always run")
	}
	flag := regexp.MustCompile(`--(required|skippable) (\S+)`)
	gate := func(job, step string) (required, skippable map[string]bool) {
		required, skippable = map[string]bool{}, map[string]bool{}
		for _, match := range flag.FindAllStringSubmatch(findStep(root.Jobs[job], step).Run, -1) {
			target := required
			if match[1] == "skippable" {
				target = skippable
			}
			for _, name := range strings.Split(match[2], ",") {
				target[name] = true
			}
		}
		needs := map[string]bool{}
		for _, name := range root.Jobs[job].Needs.([]any) {
			needs[fmt.Sprint(name)] = true
		}
		if !equalStrings(sortedKeys(required), sortedKeys(needs)) {
			violations.addf("%s --required must equal its needs", job)
		}
		return required, skippable
	}
	required, skippable := gate("ci-ok", "Require every verification to succeed")
	for name := range root.Jobs {
		if name != "ci-ok" && !required[name] {
			violations.addf("CI OK must require %s", name)
		}
	}
	if !equalStrings(sortedKeys(skippable), sortedKeys(conditional)) {
		violations.addf("CI OK skippable %v must equal conditional jobs %v", sortedKeys(skippable), sortedKeys(conditional))
	}
	buildRequired, buildSkippable := gate("build", "Check build verification results")
	for name := range buildRequired {
		if name != "changes" && !buildSkippable[name] {
			violations.addf("build must treat %s like CI OK does", name)
		}
	}
	for name := range buildSkippable {
		if !conditional[name] {
			violations.addf("build may only skip conditional job %s", name)
		}
	}
	if err := violations.err(); err != nil {
		t.Fatal(err)
	}
}
