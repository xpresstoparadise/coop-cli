package gitroom

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xpresstoparadise/coop-cli/internal/core"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// clones returns two clones of one bare repository, with an initial commit
// pushed and upstream set.
func clones(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "origin.git")
	git(t, base, "init", "--quiet", "--bare", "-b", "main", bare)
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	git(t, base, "clone", "--quiet", bare, a)
	for _, d := range []string{a} {
		git(t, d, "config", "user.name", "t")
		git(t, d, "config", "user.email", "t@example.invalid")
		git(t, d, "checkout", "--quiet", "-b", "main")
		git(t, d, "commit", "--quiet", "--allow-empty", "-m", "init")
		git(t, d, "push", "--quiet", "-u", "origin", "main")
	}
	git(t, base, "clone", "--quiet", bare, b)
	git(t, b, "config", "user.name", "t")
	git(t, b, "config", "user.email", "t@example.invalid")
	return a, b
}

func msg(side string, k core.Kind, body string) core.Message {
	return core.Message{Side: side, Actor: core.Agent, Kind: k, Body: body}
}

func TestEncodeDecode(t *testing.T) {
	m := core.Message{Seq: 12, Side: "B", Actor: core.Human, Kind: core.Note, Re: 3,
		Body: "line one\n---\nline three", At: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	got, err := decode(fileName(m), encode(m))
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, m)
	}
}

func TestDecodeCRLF(t *testing.T) {
	m := core.Message{Seq: 1, Side: "A", Actor: core.Agent, Kind: core.Propose, Body: "a\nb",
		At: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	crlf := strings.ReplaceAll(string(encode(m)), "\n", "\r\n")
	got, err := decode(fileName(m), []byte(crlf))
	if err != nil || got != m {
		t.Fatalf("CRLF decode: %+v, %v", got, err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"Orders export": "orders-export", "  API v2 / auth!! ": "api-v2-auth", "Привет": "room"} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoteRace(t *testing.T) {
	ctx := context.Background()
	dirA, dirB := clones(t)
	optA := Options{Root: dirA, Slug: "orders", Remote: true, Poll: 20 * time.Millisecond, Sync: 20 * time.Millisecond}
	optB := optA
	optB.Root = dirB

	ra, err := Create(ctx, optA, core.NewRoom("Orders", 0))
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Open(ctx, optB)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := ra.Post(ctx, msg("A", core.Propose, "p1"), 0); err != nil {
		t.Fatal(err)
	}

	// B has not pulled: its post must still land as #2 after A's #1.
	out, _, err := rb.Post(ctx, msg("B", core.Answer, "a2"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Seq != 2 {
		t.Fatalf("B's message got seq %d, want 2", out.Seq)
	}

	// A and B both post notes without pulling; one is renumbered.
	if _, _, err := ra.Post(ctx, msg("A", core.Note, "nA"), 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rb.Post(ctx, msg("B", core.Note, "nB"), 2); err != nil {
		t.Fatal(err)
	}

	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	fresh, st, err := ra.Wait(wctx, 3, func(m core.Message) bool { return m.Side == "A" })
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].Body != "nB" || st.LastSeq != 4 {
		t.Fatalf("A sees %+v, last %d", fresh, st.LastSeq)
	}

	// Out of turn is refused even when the turn changed remotely.
	if _, _, err := rb.Post(ctx, msg("B", core.Ask, "again"), 4); !errors.Is(err, core.ErrNotYourTurn) {
		t.Fatalf("got %v, want not_your_turn", err)
	}
}
