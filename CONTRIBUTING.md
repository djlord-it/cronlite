# Contributing to CronLite

Thank you for your interest in contributing to CronLite! This guide covers the process for reporting issues, suggesting improvements, and submitting code.

## Code of Conduct

Be respectful, constructive, and inclusive. We want CronLite's community to be welcoming to everyone.

## Getting Started

1. Fork the repository
2. Clone your fork and set up the dev environment:

```bash
git clone https://github.com/<your-username>/cronlite.git
cd cronlite
./scripts/dev.sh   # PostgreSQL + migrations + API key + server + admin UI
```

3. Create a branch from `main`:

```bash
git checkout -b feat/your-feature
```

## Development

### Prerequisites

- Go 1.27+
- Docker & Docker Compose
- PostgreSQL (handled by Docker Compose for local dev)

### Build & Test

```bash
# Run all tests
go test ./...

# Run tests with race detector
go test -race ./...

# Build binary
go build -o cronlite ./cmd/cronlite

# Run quality checks (SOLID, coverage, security, race detection)
./scripts/quality.sh
```

### Reproducing CI locally

CI (`.github/workflows/ci.yml`) splits verification into parallel jobs; `CI OK` is the aggregate result. The commands below reproduce the jobs that do not require Docker:

```bash
# consistency
go mod tidy -diff && go mod verify
go generate ./internal/api/... && git diff --exit-code -- internal/api/server.gen.go
go build ./... && go vet ./...

# race shards + coverage (one local run covers every shard)
go test -race -count=1 -timeout=120s -covermode=atomic -coverprofile=coverage.out ./...

# tailscale variant
go build -tags tailscale ./... && go vet -tags tailscale ./...

# tools required by the static quality checks (same versions as CI)
go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
go install github.com/uudashr/gocognit/cmd/gocognit@v1.3.0
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

# admin gates, static quality checks, CI helper tests
./scripts/admin_ci_test.sh
./scripts/quality.sh --static
python3 -m unittest discover -s scripts -p ci_test.py
```

CI's race tests run in two deterministic package shards (`scripts/ci.py shard`). The `coverage` job merges them only after verifying every package ran exactly once, and enforces the 40% total and 80% admin coverage minimums. When the test suite grows, refresh the package timings in `scripts/ci_timings.json` from a successful run.

Pull requests that change only Markdown files (outside `go:embed` targets) skip the build, test, image, HA and scan jobs. The jobs that verify `README.md`, `OPERATORS.md` and `schema/README.md` still run, and `CI OK` accepts a skip only when the `changes` job classified the pull request as docs-only. Any other change, every push to `main`/`develop` and manual runs execute the full pipeline.

### Project Structure

```
cmd/cronlite/          — CLI entrypoint
internal/domain/       — Models + repository interfaces
internal/service/      — Business logic
internal/store/postgres/ — Repository implementations (raw SQL)
internal/api/          — REST handlers (OpenAPI codegen)
internal/scheduler/    — Job scheduling (leader-only)
internal/dispatcher/   — Webhook delivery with retries
internal/reconciler/   — Orphaned execution recovery
schema/                — SQL migration files
api/openapi.yaml       — OpenAPI 3.0 spec
```

### Key Conventions

- **No ORM** — all SQL queries are in `internal/store/postgres/queries.go`, parameterized with no string concatenation.
- **OpenAPI codegen** — types and handlers in `internal/api/server.gen.go` are auto-generated. Edit `api/openapi.yaml` and run `go generate ./internal/api/` instead.
- **Namespace scoping** — all data access is filtered by namespace. Keep this in mind when adding new endpoints or queries.
- **Schema migrations** — new migrations go in `schema/` with the next sequential number (currently `012_your_migration.sql`).

## Submitting Changes

### Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add job tagging support
fix: handle timezone edge case in scheduler
docs: update API reference
refactor: extract circuit breaker config
test: add dispatcher retry tests
chore: update dependencies
```

### Pull Requests

1. Keep PRs focused — one feature or fix per PR.
2. Ensure all tests pass (`go test ./...`) and the race detector is clean (`go test -race ./...`).
3. Update documentation if your change affects the API, configuration, or user-facing behavior.
4. Fill out the PR description with what changed and why.

### What We Look For in Review

- Tests covering new or changed behavior
- No security regressions (SSRF, injection, credential exposure)
- Consistent style with the existing codebase
- Clean separation between layers (domain, service, store, API)

### Dependency Maintenance

Dependabot checks weekly and groups minor and patch updates into one Go PR and
one GitHub Actions PR. Major upgrades remain individual PRs. Security updates
retain Dependabot's separate security update process.

Verified Dependabot PRs containing only patch updates are queued for squash
auto-merge into `main`. Groups containing a minor upgrade and all major upgrades
require manual merging. Auto-merge waits for the required build, quality, admin,
HA, and security checks and for the branch to be up to date with `main`.

The auto-merge workflow reads verified metadata from the base branch and never
checks out or executes PR code. It matches the PR head commit before enabling
auto-merge. Keep required status checks enabled in the `protect-main` ruleset;
if job names change, update those required check names too.

Keep CI tools on explicit versions and use release binaries where available.
Workflow contract tests enforce action identities, full SHA pins, and security
settings without duplicating the action SHAs in Go tests. Updating a pinned
action must not require editing a test constant.

## Reporting Issues

Open a [GitHub issue](https://github.com/djlord-it/cronlite/issues) with:

- Steps to reproduce
- Expected vs. actual behavior
- CronLite version and environment details

## License

By contributing, you agree that your contributions will be licensed under the [AGPL-3.0](LICENSE).
