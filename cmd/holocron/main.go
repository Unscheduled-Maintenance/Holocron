// Command holocron is a personal work archive: fast capture, full-text
// search and deterministic reports over a local SQLite journal.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/Unscheduled-Maintenance/Holocron/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], cli.SystemIO())
	stop()
	os.Exit(code)
}
