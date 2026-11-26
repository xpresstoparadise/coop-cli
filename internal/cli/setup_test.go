package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupAll(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// Existing configs of the user must survive.
	write(".mcp.json", `{"mcpServers":{"other":{"command":"x"}}}`)
	write(".codex/config.toml", "model = \"o\"\n\n[mcp_servers.coop]\ncommand = \"old\"\n\n[mcp_servers.other]\ncommand = \"y\"\n")

	tm := &term{t: t, dir: dir}
	tm.run(1, "setup")
	tm.run(1, "setup", "vim")
	out := tm.run(0, "setup", "all")
	mustContain(t, out, "created  .agents/skills/coop/SKILL.md")
	mustContain(t, out, "updated  .mcp.json")
	mustContain(t, out, "trusted")

	var mcp struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(".mcp.json")), &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp.MCPServers["other"] == nil || mcp.MCPServers["coop"]["command"] != "coop" {
		t.Fatalf(".mcp.json: %+v", mcp)
	}

	toml := read(".codex/config.toml")
	mustContain(t, toml, `model = "o"`)
	mustContain(t, toml, "[mcp_servers.other]")
	mustContain(t, toml, "tool_timeout_sec = 660")
	if strings.Contains(toml, `"old"`) || strings.Count(toml, "[mcp_servers.coop]") != 1 {
		t.Fatalf("codex section not replaced:\n%s", toml)
	}

	mustContain(t, read(".cursor/mcp.json"), "${workspaceFolder}")
	mustContain(t, read(".cursor/rules/coop.mdc"), "alwaysApply: false")
	mustContain(t, read(".gemini/settings.json"), `"timeout": 660000`)
	mustContain(t, read("opencode.json"), `"$schema"`)
	mustContain(t, read(".claude/skills/coop/SKILL.md"), "name: coop")
	for _, cmd := range []string{"coop-open", "coop-join", "coop-status", "coop-approve", "coop-resume", "coop-say", "coop-close", "coop-stop"} {
		mustContain(t, read(".agents/skills/"+cmd+"/SKILL.md"), "name: "+cmd)
		mustContain(t, read(".claude/skills/"+cmd+"/SKILL.md"), "disable-model-invocation: true")
	}

	mustContain(t, read(".kimi-code/mcp.json"), `"toolTimeoutMs": 330000`)
	mustContain(t, read(".agents/mcp.json"), `"55s"`)
	join := read(".opencode/commands/coop-join.md")
	mustContain(t, join, "description: Join a coop room")
	mustContain(t, join, "$ARGUMENTS")
	mustContain(t, read(".agents/commands/coop-join.md"), "argument-hint:")
	if strings.Contains(join, "disable-model-invocation") {
		t.Fatal("command files must not carry skill-only fields")
	}

	// Running again changes nothing.
	again := tm.run(0, "setup", "all")
	if strings.Contains(again, "created") || strings.Contains(again, "updated") {
		t.Fatalf("setup is not idempotent:\n%s", again)
	}

	// A file we cannot parse is left alone, with a snippet to add by hand.
	write(".gemini/settings.json", "{ // comment\n}")
	mustContain(t, tm.run(1, "setup", "gemini"), `"mcpServers"`)
}

func TestSplitFrontMatterCRLF(t *testing.T) {
	fm, body := splitFrontMatter("---\r\nname: coop-join\r\ndescription: Join.\r\ndisable-model-invocation: true\r\n---\r\n\r\nBody $ARGUMENTS\r\n")
	if fm["description"] != "Join." || strings.Contains(body, "disable-model-invocation") || !strings.Contains(body, "Body $ARGUMENTS") {
		t.Fatalf("fm=%v body=%q", fm, body)
	}
}
