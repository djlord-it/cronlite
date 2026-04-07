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
docker compose up -d
```

3. Create a branch from `main`:

```bash
git checkout -b feat/your-feature
```

## Development

### Prerequisites

- Go 1.25+
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
- **OpenAPI codegen** — types in `internal/api/types.gen.go` are auto-generated. Edit `api/openapi.yaml` and run `go generate ./internal/api/` instead.
- **Namespace scoping** — all data access is filtered by namespace. Keep this in mind when adding new endpoints or queries.
- **Schema migrations** — new migrations go in `schema/` with the next sequential number (e.g., `007_your_migration.sql`).

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

## Reporting Issues

Open a [GitHub issue](https://github.com/djlord-it/cronlite/issues) with:

- Steps to reproduce
- Expected vs. actual behavior
- CronLite version and environment details

## License

By contributing, you agree that your contributions will be licensed under the [AGPL-3.0](LICENSE).
