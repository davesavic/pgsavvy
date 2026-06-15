// Command pgprobe is a fail-loud reachability check used by `task test:integration`.
// It connects to the DSN given as the first argument (or PGSAVVY_TEST_PG) and
// pings it; a non-zero exit means the integration suite must NOT run silently.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	dsn := ""
	if len(os.Args) > 1 {
		dsn = os.Args[1]
	}
	if dsn == "" {
		dsn = os.Getenv("PGSAVVY_TEST_PG")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "pgprobe: no DSN (set PGSAVVY_TEST_PG or pass it as the first argument)")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgprobe: connect failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close(ctx) }()

	if err := conn.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "pgprobe: ping failed: %v\n", err)
		os.Exit(1)
	}
}
