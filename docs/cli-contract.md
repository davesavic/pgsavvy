# Machine CLI contract

The machine CLI is Linux-only. This protocol package is a library milestone:
production commands are registered only after their complete implementations.
It never initializes TUI configuration, history, persistent logs or themes.

Commands return one version-1 JSON object followed by a newline. The envelope
has `schema_version`, `ok`, `command`, optional `connection`, `data` or `error`,
`warnings` (always an array), `result_complete`, `truncated`, and an optional
`truncation_reason` (`row_limit` or `page_limit`). A connection is a selected
profile name, never an endpoint. Backend work and cleanup finish before encoding.
Error messages and remediation are allowlisted; raw backend errors are private.
Diagnostic stderr is best-effort and follows the stdout publication attempt;
its safe outcome code reflects that attempt. A failed diagnostic sink does not
suppress a healthy stdout document or change its exit status.

| Exit | Meaning |
| --- | --- |
| 0 | Success, including an explicitly capped preview |
| 2 | Invalid input or unsupported SQL |
| 3 | Unavailable connection, credentials or SSH trust |
| 4 | Profile policy denied |
| 5 | Database failure or missing object |
| 6 | Timeout or cancellation |
| 7 | I/O, encoding or result-resource failure |

Output including the envelope and newline is at most 1 MiB. Encoding overflow
publishes a safe error instead of partial data. A failing/broken output writer
returns exit 7 without appending another document. An open unread pipe is also
bounded by the invocation deadline, not only a pipe whose reader has closed.

Limits: SQL and parameter input each 1 MiB; 256 parameters; profile name 256
bytes; rows default 100, maximum 1000; PostgreSQL-text cell 64 KiB; invocation
30 seconds; connection 10 seconds; helper 5 seconds and 32 KiB stdout;
statement 10 seconds; lock 1 second; fresh cleanup at most 2 seconds.
`--timeout` may lower the invocation budget; `--format json` is the only format.
Duplicate/conflicting/unknown flags are errors. Command-specific help is JSON.

The invocation reserves up to four seconds, or 40% of a shorter budget, inside
its deadline: half for cleanup and half for publication. Backend work receives
the earlier deadline, calculated from the effective deadline including its parent.
Work cancellation interrupts owned input without prematurely closing output.
A parent cancellation can prevent error delivery; no
publication continues past the outer invocation deadline. Cancellable handles
must interrupt and join blocked I/O when closed. Arbitrarily stalled remote
filesystems and non-cooperating injected streams are excluded.

Bare startup, version, log-directory selection and `update` remain legacy routes.
Unknown command words never fall back to the TUI. Commands and dependencies are
injected into `Dispatch`, without package-init registration or GUI construction.
