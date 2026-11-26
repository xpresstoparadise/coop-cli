package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	tm := &term{t: t, dir: dir}
	tm.run(0, "open", "--topic", "Orders")
	tm.run(0, "join")
	tm.run(0, "say", "--agent", "--side", "A", "--kind", "propose", "first")

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan int)
	go func() {
		env := Env{Stdin: inR, Stdout: out, Stderr: out, Dir: dir, IsTTY: func() bool { return true }}
		done <- Run(context.Background(), env, []string{"watch", "--side", "B"})
	}()
	eventually(t, "history", func() bool { return strings.Contains(out.String(), "── #1 · A agent · propose") })

	tm.run(0, "say", "--agent", "--side", "B", "--kind", "ask", "second?")
	eventually(t, "live message", func() bool { return strings.Contains(out.String(), "second?") })

	fmt.Fprintln(inW, "h need to think")
	eventually(t, "hand from keys", func() bool { return strings.Contains(out.String(), "paused (hand by B human)") })
	fmt.Fprintln(inW, "x")
	eventually(t, "unknown key", func() bool { return strings.Contains(out.String(), "unknown key") })
	fmt.Fprintln(inW, "q")
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("watch exit %d:\n%s", code, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("q did not stop watch")
	}
}

func TestExport(t *testing.T) {
	dir := t.TempDir()
	tm := &term{t: t, dir: dir}
	tm.run(0, "open", "--topic", "Orders export")
	tm.run(0, "join")
	mustContain(t, tm.run(0, "export", "--side", "A"), "Outcome: in negotiation")
	negotiate(t, tm, filepath.Join(dir, "coop", "orders-export", "contract", "agreement.md"))

	md := tm.run(0, "export", "--side", "B")
	mustContain(t, md, "# Minutes: Orders export")
	mustContain(t, md, "## Agreement")
	mustContain(t, md, "approved by both sides")
	mustContain(t, md, "### #3 · B agent · counter re #1 · resolution")

	var got struct {
		State     struct{ Outcome string }
		Agreement string
		Log       []json.RawMessage
	}
	if err := json.Unmarshal([]byte(tm.run(0, "export", "--side", "A", "--format", "json")), &got); err != nil {
		t.Fatal(err)
	}
	if got.State.Outcome != "agreement" || !strings.Contains(got.Agreement, "100 per page") || len(got.Log) != 6 {
		t.Fatalf("json export: %+v", got)
	}
	mustContain(t, tm.run(0, "export", "--side", "A", "-o", filepath.Join(dir, "m.md")), "exported 6 messages")
	tm.run(1, "export", "--side", "A", "--format", "pdf")
}

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("hi\x7f\r\x1b[A\x1b[6~ж\x1b[1;5C\x03\x1b"))
	var names []string
	for _, k := range got {
		if k.r != 0 {
			names = append(names, string(k.r))
		} else {
			names = append(names, k.name)
		}
	}
	want := "h i backspace enter up pgdown ж quit quit"
	if strings.Join(names, " ") != want {
		t.Fatalf("keys: %q, want %q", strings.Join(names, " "), want)
	}
}
