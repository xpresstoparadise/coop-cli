// Package local keeps a side's identity for a project in .coop/<side>.json.
package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is the folder, relative to the project root, that holds identities.
const Dir = ".coop"

// Identity is what one side knows about the room it is in.
type Identity struct {
	Backend string `json:"backend"` // "git" or "server"
	Slug    string `json:"slug"`
	Side    string `json:"side"`
	Remote  bool   `json:"remote,omitempty"` // git: sync through the remote

	// Server backend.
	Server     string `json:"server,omitempty"`
	RoomID     string `json:"room_id,omitempty"`
	AgentToken string `json:"agent_token,omitempty"`
	HumanToken string `json:"human_token,omitempty"`

	// SeenAgent and SeenHuman are the last seq each actor has read.
	SeenAgent int `json:"seen_agent"`
	SeenHuman int `json:"seen_human"`
}

func path(root, side string) string { return filepath.Join(root, Dir, side+".json") }

// Save writes the identity with owner-only permissions.
func (id *Identity) Save(root string) error {
	if err := os.MkdirAll(filepath.Join(root, Dir), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(id, "", "  ")
	tmp := path(root, id.Side) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path(root, id.Side))
}

// Sides lists the sides that have an identity in this project.
func Sides(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, Dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sides []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".json") && !e.IsDir() {
			sides = append(sides, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(sides)
	return sides, nil
}

// Load returns the identity for side. An empty side means $COOP_SIDE, or
// the only identity in the project.
func Load(root, side string) (*Identity, error) {
	if side == "" {
		side = os.Getenv("COOP_SIDE")
	}
	if side == "" {
		sides, err := Sides(root)
		if err != nil {
			return nil, err
		}
		switch len(sides) {
		case 0:
			return nil, errors.New("not in a room here: run `coop open` or `coop join` first")
		case 1:
			side = sides[0]
		default:
			return nil, fmt.Errorf("this folder has sides %s: choose one with --side or COOP_SIDE", strings.Join(sides, ", "))
		}
	}
	data, err := os.ReadFile(path(root, side))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no identity for side %s here: run `coop join` first", side)
	}
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("%s: %w", path(root, side), err)
	}
	return &id, nil
}

// EnsureIgnored adds .coop/ to the project's .gitignore if it is missing.
func EnsureIgnored(root string) error {
	p := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case ".coop", ".coop/", "/.coop", "/.coop/":
			return nil
		}
	}
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(prefix + ".coop/\n")
	return err
}
