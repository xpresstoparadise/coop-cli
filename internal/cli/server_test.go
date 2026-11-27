package cli

import (
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xpresstoparadise/coop-cli/internal/server"
)

func startServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv, err := server.Open(filepath.Join(t.TempDir(), "coop.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); srv.Close() })
	_, key, err := srv.AddKey("test")
	if err != nil {
		t.Fatal(err)
	}
	return hs, key
}

var inviteRe = regexp.MustCompile(`coop join '([^']+)'`)

func TestFullRoomOnServer(t *testing.T) {
	hs, key := startServer(t)
	dir := t.TempDir()
	tm := &term{t: t, dir: dir}

	tm.run(1, "open", "--topic", "Orders export", "--server", hs.URL)                 // no key
	tm.run(1, "open", "--topic", "Orders export", "--server", hs.URL, "--key", "bad") // bad key
	out := tm.run(0, "open", "--topic", "Orders export", "--turns", "5", "--server", hs.URL, "--key", key)
	m := inviteRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no invitation in:\n%s", out)
	}
	// The server was started without a public URL; point the link at it.
	inv := hs.URL + m[1][strings.Index(m[1], "/r/"):]
	mustContain(t, tm.run(0, "join", inv), "joined room \"Orders export\"")
	mustContain(t, tm.run(1, "join", inv), "already in room") // checked before the invitation is spent

	other := &term{t: t, dir: t.TempDir()}
	mustContain(t, other.run(1, "join", inv), "invalid_invitation")

	negotiate(t, tm, filepath.Join(dir, ".coop", "agreement-orders-export.md"))
}

// A wait that runs out of time must report "nothing new", not an error,
// even when the deadline falls between the long-poll and the reload.
func TestServerWaitTimeoutIsNotAnError(t *testing.T) {
	hs, key := startServer(t)
	tm := &term{t: t, dir: t.TempDir()}
	tm.run(0, "open", "--topic", "t", "--server", hs.URL, "--key", key)
	for _, d := range []string{"1ms", "50ms", "300ms", "1s"} {
		mustContain(t, tm.run(0, "wait", "--agent", "--timeout", d), "no new messages")
	}
}

func TestLoginAndJoinByCode(t *testing.T) {
	t.Setenv("COOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	hs, key := startServer(t)
	a := &term{t: t, dir: t.TempDir()}
	a.run(1, "login", "--server", "http://127.0.0.1:1") // unreachable
	a.run(0, "login", "--server", hs.URL, "--key", key) // saved for this test process
	out := a.run(0, "open", "--topic", "Коды")          // server from login
	m := regexp.MustCompile(`invite code \(single use, a secret\): (\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no code in:\n%s", out)
	}
	code := m[1]
	if !regexp.MustCompile(`^[0-9A-Z]{5}-[0-9A-Z]{5}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	b := &term{t: t, dir: t.TempDir()}
	// Typed carelessly: lower case, no dash. Still works, once.
	mustContain(t, b.run(0, "join", strings.ToLower(strings.ReplaceAll(code, "-", ""))), "joined room \"Коды\"")
	c := &term{t: t, dir: t.TempDir()}
	mustContain(t, c.run(1, "join", code), "invalid_invitation")
	// --git still opens a git room although a server is configured.
	mustContain(t, (&term{t: t, dir: t.TempDir()}).run(0, "open", "--git", "--topic", "local"), "coop/local")
}

func TestMCPOpenAndJoin(t *testing.T) {
	t.Setenv("COOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	hs, key := startServer(t)
	(&term{t: t, dir: t.TempDir()}).run(0, "login", "--server", hs.URL, "--key", key)

	a := startMCP(t, t.TempDir(), "")
	a.send("initialize", map[string]any{"protocolVersion": "2025-11-25"})
	a.recv()
	r := a.call("coop_open", map[string]any{"topic": "Из чата", "turns": 5})
	m := regexp.MustCompile(`invite code \(single use, a secret\): (\S+)`).FindStringSubmatch(r.Content[0].Text)
	if r.IsError || m == nil {
		t.Fatalf("coop_open: %+v", r)
	}
	b := startMCP(t, t.TempDir(), "")
	b.send("initialize", map[string]any{"protocolVersion": "2025-11-25"})
	b.recv()
	r = b.call("coop_join", map[string]any{"invite": m[1]})
	if r.IsError || !strings.Contains(r.Content[0].Text, `joined room "Из чата"`) || !strings.Contains(r.Content[0].Text, "ask them what they need") {
		t.Fatalf("coop_join: %+v", r)
	}
	r = b.call("coop_status", nil)
	if !strings.Contains(r.Content[0].Text, "you: side B, agent") {
		t.Fatalf("status after join: %+v", r)
	}
}
