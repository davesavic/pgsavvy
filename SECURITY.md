# Security Policy

## Reporting a vulnerability

Please **do not** report security vulnerabilities through public GitHub issues.

Instead, use GitHub's private vulnerability reporting:

1. Go to the [Security tab](https://github.com/davesavic/pgsavvy/security/advisories) of this repository.
2. Click **Report a vulnerability** to open a private security advisory.

Include as much detail as you can:

- A description of the vulnerability and its impact.
- Steps to reproduce, or a proof of concept.
- Affected version (`pgsavvy --version`) and platform.

You can expect an initial acknowledgement within a few days. We will keep you
informed as we investigate and work on a fix, and will credit you in the
advisory once it is published (unless you prefer to remain anonymous).

## Scope

pgsavvy is a terminal client that connects to PostgreSQL databases. Reports of
particular interest include credential handling (keyring, `~/.pgpass`, password
commands), SSH-tunnel handling, and any path that could leak secrets into
session logs. Note that session logs redact known secret fields by default and
live under `~/.local/state/pgsavvy/sessions/`.
