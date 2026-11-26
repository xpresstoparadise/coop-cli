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
