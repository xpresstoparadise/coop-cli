// Command coop lets AI agents in different terminals negotiate in a room.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/xpresstoparadise/coop-cli/internal/cli"
)

// version is set by the release build (-X main.version=...). Without it the
// module version recorded by `go install` is used.
var version = ""

func main() {
	cli.Version = version
	if cli.Version == "" {
		cli.Version = "dev"
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			cli.Version = bi.Main.Version
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, cli.OSEnv(), os.Args[1:])
	stop()
	os.Exit(code)
}
