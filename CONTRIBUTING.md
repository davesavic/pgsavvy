# Contributing to pgsavvy

Thanks for your interest in improving pgsavvy — a vim-style TUI PostgreSQL
client for the terminal. This guide covers how to get a development build
running, the test and lint workflow, and how to propose changes.

## Prerequisites

- [Go 1.26](https://go.dev/dl/) or newer
- [go-task](https://taskfile.dev) v3 — `go install github.com/go-task/task/v3/cmd/task@latest`
- [golangci-lint](https://golangci-lint.run) v2 — `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.0`
- [Docker Compose](https://docs.docker.com/compose/) — only for the integration-test fixtures

## Getting the source

```sh
git clone https://github.com/davesavic/pgsavvy.git
cd pgsavvy
task build       # compiles to bin/pgsavvy
```

Run `task --list` to see every available task.

## Build, test, lint

```sh
task build       # compile to bin/pgsavvy
task test        # unit tests across all packages (task test -- -run TestX to filter)
task lint        # golangci-lint v2
task fmt         # format Go files (gofumpt + goimports via golangci-lint)
task vulncheck   # govulncheck across the module
```

Please run `task fmt`, `task lint`, and `task test` before opening a pull
request — CI runs the same checks.

## Integration tests

Integration tests run against a live PostgreSQL fixture and are gated by the
`PGSAVVY_TEST_PG` environment variable, so they never silently skip.

```sh
task pg:up               # bring up the docker/postgres fixture (waits until healthy)
export PGSAVVY_TEST_PG=postgres://pgsavvy:pgsavvy@localhost:5432/pgsavvy_test
task test:integration    # integration tests against the fixture
task test:all            # unit + integration in one run
task pg:down             # tear down the fixture (removes container + volume)
```

The official `postgres` image only honors the fixture credentials on an empty
data directory. If you change the schema or credentials, recreate the volume
with `task pg:down && task pg:up` before re-running the suite.

SSH-tunnel tests use a separate fixture:

```sh
task sshtunnel:up        # SSH bastion + private Postgres (ephemeral keypair)
task sshtunnel:down
```

## Proposing changes

1. Fork the repository and create a topic branch for your change.
2. Make your change, keeping commits focused and the tree green (`task test`,
   `task lint`).
3. Open a pull request against `master`. Describe the change, the motivation,
   and how you verified it; fill in the pull-request checklist.
4. For larger or behavior-changing work, open an issue first so the approach
   can be discussed before you invest in the implementation.

## Reporting issues

File bugs and feature requests through
[GitHub Issues](https://github.com/davesavic/pgsavvy/issues) using the
provided templates. For security-sensitive reports, follow
[SECURITY.md](SECURITY.md) instead of opening a public issue.
