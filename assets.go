// Package coopcli holds files shipped inside the coop binary.
package coopcli

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed skills/coop/SKILL.md
var skill string

// Skill is the main agent skill, with LF line endings.
var Skill = strings.ReplaceAll(skill, "\r\n", "\n")

//go:embed skills
var skills embed.FS

// Skills returns every skill as name → SKILL.md: the main `coop` skill and
// the slash commands (coop-open, coop-join, ...). `coop setup` writes them
// all, so each command shows up in the agent's / menu.
func Skills() map[string]string {
	out := map[string]string{}
	entries, _ := fs.ReadDir(skills, "skills")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := skills.ReadFile(path.Join("skills", e.Name(), "SKILL.md"))
		if err == nil {
			// A Windows checkout may turn the files into CRLF.
			out[e.Name()] = strings.ReplaceAll(string(data), "\r\n", "\n")
		}
	}
	return out
}

// SkillNames lists the skills in a stable order.
func SkillNames() []string {
	var names []string
	for n := range Skills() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
