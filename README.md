# dbsavvy

A vim-style TUI database client built like [lazygit](https://github.com/jesseduffield/lazygit) — fast keyboard navigation, modal panes, and a focused workflow for browsing and querying relational databases from the terminal.

See [DESIGN.md](DESIGN.md) for the full specification.

## Status

Bootstrap. The repo is a runnable skeleton; no database features have landed yet. Track progress through the `dbsavvy-*` epics in the beads issue tracker.

## Quick Start

```sh
task build       # produces bin/dbsavvy with -ldflags-injected version metadata
task test ./...  # runs the test suite (empty-pass at bootstrap)
task lint        # golangci-lint v2 — 5 linters + 2 formatters
task fmt         # golangci-lint run --fix (formatter pass)
bin/dbsavvy      # prints "dbsavvy dev (task)" — stub entry point
```

## Requirements

- [Go 1.25](https://go.dev/dl/)
- [go-task](https://taskfile.dev) v3 — `go install github.com/go-task/task/v3/cmd/task@latest`
- [golangci-lint](https://golangci-lint.run) v2 — `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.0`
- [Docker Compose](https://docs.docker.com/compose/) — optional, only needed to run the Postgres integration fixture

### Optional dev tooling

- `gofumpt` — IDE save-on-format hook. Not required for `task fmt` or CI; golangci-lint v2 bundles `gofumpt` and `goimports` as built-in formatters.

## Integration test fixture (gotcha)

Once `docker/postgres/docker-compose.yml` lands (`dbsavvy-2zl.4`), bringing the Postgres fixture up against a pre-existing `pgdata` volume will skip the env-driven init step — the official `postgres` image only honors `POSTGRES_USER` / `POSTGRES_DB` on an empty data directory. Before running integration tests against a fresh schema, tear the stack down with the volume:

```sh
docker compose -f docker/postgres/docker-compose.yml down -v
docker compose -f docker/postgres/docker-compose.yml up -d
```

## License

[MIT](LICENSE) — Copyright (c) 2026 Dave Savic.
