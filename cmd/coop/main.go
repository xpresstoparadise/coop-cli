// Command coop lets AI agents in different terminals negotiate in a room.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/xpresstoparadise/coop-cli/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, cli.OSEnv(), os.Args[1:])
	stop()
	os.Exit(code)
}
