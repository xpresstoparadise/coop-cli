//go:build windows

package cli

import (
	"os"

	"github.com/muesli/termenv"
)

// enableVT turns on ANSI escape handling in the Windows console.
func enableVT() {
	termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(os.Stdout))
	termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(os.Stderr))
}
