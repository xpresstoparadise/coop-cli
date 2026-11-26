package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests must not see the developer's own ~/.config/coop or environment.
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "coop-test-conf")
	os.Setenv("COOP_CONFIG", filepath.Join(dir, "config.json"))
	for _, v := range []string{"COOP_SERVER", "COOP_HOST_KEY", "COOP_SIDE", "COOP_DIR", "CLAUDE_PROJECT_DIR"} {
		os.Unsetenv(v)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
