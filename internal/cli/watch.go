package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xpresstoparadise/coop-cli/internal/core"
)

// pretty renders a message for a person reading the room.
func pretty(m core.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "── #%d · %s %s · %s", m.Seq, m.Side, m.Actor, m.Kind)
	if m.Re != 0 {
		fmt.Fprintf(&b, " re #%d", m.Re)
	}
	if m.Resolution {
		b.WriteString(" · RESOLUTION")
	}
	if !m.At.IsZero() {
		b.WriteString(" · " + m.At.Local().Format("15:04"))
	}
	b.WriteByte('\n')
	for _, line := range strings.Split(m.Body, "\n") {
		b.WriteString("   " + line + "\n")
	}
	return b.String()
}

func cmdWatch(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("watch", env, false)
	last := fs.Int("last", 20, "show this many earlier messages first (0: all)")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	ctx, quit := context.WithCancel(ctx)
	defer quit()

	var mu sync.Mutex // serialises output and the current wait
	var kick context.CancelFunc
	say := func(text string) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprint(env.Stdout, text)
	}

	_, log, st, err := s.load(ctx)
	if err != nil {
		return err
	}
	head := header(st, s.id, core.Human)
	start := 0
	if *last > 0 && len(log) > *last {
		start = len(log) - *last
		say(fmt.Sprintf("(%d earlier messages; coop read --all for the whole log)\n", start))
	}
	for _, m := range log[start:] {
		say(pretty(m))
	}
	say(head + "\n")
	seq := st.LastSeq

	if env.IsTTY() {
		say("keys: h [reason] = raise hand · r = resume · s TEXT = say · q = quit\n")
		go watchKeys(ctx, env, s, sf, say, quit, func() {
			mu.Lock()
			if kick != nil {
				kick()
			}
			mu.Unlock()
		})
	}

	for !st.Closed {
		wctx, cancel := context.WithTimeout(ctx, 50*time.Second)
		mu.Lock()
		kick = cancel
		mu.Unlock()
		_, _, werr := s.room.Wait(wctx, seq, func(core.Message) bool { return false })
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if werr != nil {
			say("(connection problem: " + werr.Error() + "; retrying)\n")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		// Reload: the server's wait skips our own human messages.
		_, log, st, err = s.load(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			say("(" + err.Error() + ")\n")
			continue
		}
		for _, m := range log {
			if m.Seq > seq {
				say(pretty(m))
			}
		}
		seq = st.LastSeq
		if h := header(st, s.id, core.Human); h != head {
			head = h
			say(h + "\n")
		}
	}
	say("the room is closed\n")
	return nil
}

// watchKeys reads commands typed into watch and runs them as the human.
func watchKeys(ctx context.Context, env *Env, s *session, sf sessionFlags, say func(string), quit, refresh func()) {
	sc := bufio.NewScanner(env.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		cmd, rest, _ := strings.Cut(line, " ")
		var args []string
		switch cmd {
		case "":
			continue
		case "q", "quit":
			quit()
			return
		case "h", "hand":
			args = []string{"hand", "--", rest}
			if rest == "" {
				args = args[:1]
			}
		case "r", "resume":
			args = []string{"resume"}
		case "s", "say":
			if rest == "" {
				say("(s needs text)\n")
				continue
			}
			args = []string{"say", "--", rest}
		default:
			say("(unknown key: use h, r, s TEXT or q)\n")
			continue
		}
		if *sf.side != "" {
			args = append([]string{args[0], "--side", *sf.side}, args[1:]...)
		}
		var out strings.Builder
		sub := Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Dir: env.Dir, IsTTY: func() bool { return false }}
		if err := runCmd(ctx, &sub, args); err != nil {
			var ce *core.Error
			if errors.As(err, &ce) {
				say(fmt.Sprintf("(refused: %s)\n", ce.Msg))
			} else {
				say("(" + err.Error() + ")\n")
			}
		}
		refresh()
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		say("(" + err.Error() + ")\n")
	}
}

func cmdExport(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("export", env, false)
	format := fs.String("format", "md", "md or json")
	out := fs.String("o", "", "write to this file instead of stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	_, log, st, err := s.load(ctx)
	if err != nil {
		return err
	}
	var data []byte
	switch *format {
	case "md", "markdown":
		data = []byte(minutes(st, s.id.Slug, s.id.Backend, log))
	case "json":
		data, err = json.MarshalIndent(exportJSON(st, s.id.Slug, s.id.Backend, log), "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
	default:
		return fmt.Errorf("unknown format %q (md or json)", *format)
	}
	if *out == "" {
		_, err = env.Stdout.Write(data)
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "exported %d messages to %s\n", len(log), *out)
	return nil
}

func outcome(st *core.State) string {
	switch {
	case st.Closed && st.CloseReason == "approved":
		return "agreement"
	case st.Closed:
		return "closed without agreement (" + st.CloseReason + ")"
	case st.Agreed:
		return "resolution agreed, waiting for approval"
	case st.Resolution != 0:
		return "resolution under discussion"
	}
	return "in negotiation"
}

func minutes(st *core.State, slug, backend string, log []core.Message) string {
	var b strings.Builder
	r := st.Room
	fmt.Fprintf(&b, "# Minutes: %s\n\n", r.Topic)
	fmt.Fprintf(&b, "- Room: `%s` (%s backend)\n", slug, backend)
	fmt.Fprintf(&b, "- Sides: %s, %s\n", r.Sides[0], r.Sides[1])
	fmt.Fprintf(&b, "- Outcome: %s\n", outcome(st))
	fmt.Fprintf(&b, "- Messages: %d; turns used: %s %d, %s %d of %d\n",
		len(log), r.Sides[0], st.TurnsUsed[r.Sides[0]], r.Sides[1], st.TurnsUsed[r.Sides[1]], r.TurnBudget)
	if len(log) > 0 {
		fmt.Fprintf(&b, "- From %s to %s\n", log[0].At.UTC().Format(time.RFC3339), log[len(log)-1].At.UTC().Format(time.RFC3339))
	}
	if st.Resolution != 0 {
		title := "Resolution"
		if st.Closed && st.CloseReason == "approved" {
			title = "Agreement"
		}
		fmt.Fprintf(&b, "\n## %s\n\nResolution #%d by side %s", title, st.Resolution, st.ResolutionSide)
		switch {
		case st.Closed && st.CloseReason == "approved":
			b.WriteString(", approved by both sides.\n\n")
		case st.Agreed:
			fmt.Fprintf(&b, ", agreed by both agents, approved by %d of 2 humans.\n\n", len(st.Approved))
		default:
			b.WriteString(", not yet accepted.\n\n")
		}
		b.WriteString(st.ResolutionBody() + "\n")
	}
	b.WriteString("\n## Thread\n")
	for _, m := range log {
		fmt.Fprintf(&b, "\n### #%d · %s %s · %s", m.Seq, m.Side, m.Actor, m.Kind)
		if m.Re != 0 {
			fmt.Fprintf(&b, " re #%d", m.Re)
		}
		if m.Resolution {
			b.WriteString(" · resolution")
		}
		fmt.Fprintf(&b, "\n\n%s\n", m.Body)
	}
	return b.String()
}

func exportJSON(st *core.State, slug, backend string, log []core.Message) any {
	type summary struct {
		Phase          core.Phase      `json:"phase"`
		Outcome        string          `json:"outcome"`
		CloseReason    string          `json:"close_reason,omitempty"`
		Resolution     int             `json:"resolution,omitempty"`
		ResolutionSide string          `json:"resolution_side,omitempty"`
		Agreed         bool            `json:"agreed"`
		Approved       map[string]bool `json:"approved,omitempty"`
		TurnsUsed      map[string]int  `json:"turns_used"`
	}
	out := struct {
		Room      core.Room      `json:"room"`
		Slug      string         `json:"slug"`
		Backend   string         `json:"backend"`
		State     summary        `json:"state"`
		Agreement string         `json:"agreement,omitempty"`
		Log       []core.Message `json:"log"`
	}{
		Room: st.Room, Slug: slug, Backend: backend, Log: log,
		State: summary{Phase: st.Phase(), Outcome: outcome(st), CloseReason: st.CloseReason, Resolution: st.Resolution,
			ResolutionSide: st.ResolutionSide, Agreed: st.Agreed, Approved: st.Approved, TurnsUsed: st.TurnsUsed},
	}
	if st.Closed && st.CloseReason == "approved" {
		out.Agreement = st.ResolutionBody()
	}
	if out.Log == nil {
		out.Log = []core.Message{}
	}
	return out
}
