package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	xterm "github.com/charmbracelet/x/term"

	"github.com/xpresstoparadise/coop-cli/internal/core"
	"github.com/xpresstoparadise/coop-cli/internal/ui"
)

// The live view of `coop watch` for a person at a terminal: a small
// full-screen loop (raw mode, alternate screen) drawn with package ui.
// It deliberately avoids TUI frameworks that query the terminal on start:
// such a query reads the answer from stdin and hangs, or eats input, where
// the terminal does not answer.

const freshFor = 4 * time.Second

type key struct {
	r    rune   // printable rune, or 0
	name string // "enter", "backspace", "up", "down", "pgup", "pgdown", "quit"
}

var escKeys = []struct{ seq, name string }{
	{"\x1b[A", "up"}, {"\x1b[B", "down"}, {"\x1bOA", "up"}, {"\x1bOB", "down"},
	{"\x1b[5~", "pgup"}, {"\x1b[6~", "pgdown"},
}

// parseKeys turns raw terminal input into keys.
func parseKeys(b []byte) []key {
	var keys []key
	for len(b) > 0 {
		switch {
		case b[0] == 3 || b[0] == 4: // Ctrl-C, Ctrl-D
			keys = append(keys, key{name: "quit"})
			b = b[1:]
		case b[0] == '\r' || b[0] == '\n':
			keys = append(keys, key{name: "enter"})
			if len(b) > 1 && b[0] == '\r' && b[1] == '\n' {
				b = b[1:]
			}
			b = b[1:]
		case b[0] == 0x7f || b[0] == 0x08:
			keys = append(keys, key{name: "backspace"})
			b = b[1:]
		case b[0] == 0x1b:
			matched := false
			for _, e := range escKeys {
				if strings.HasPrefix(string(b), e.seq) {
					keys = append(keys, key{name: e.name})
					b = b[len(e.seq):]
					matched = true
					break
				}
			}
			if matched {
				continue
			}
			if len(b) == 1 {
				keys = append(keys, key{name: "quit"}) // a lone Esc
				b = b[1:]
				continue
			}
			// Skip an unknown escape sequence: ESC [ params final-byte.
			i := 1
			if b[i] == '[' || b[i] == 'O' {
				i++
				for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
					i++
				}
			}
			i++
			if i > len(b) {
				i = len(b)
			}
			b = b[i:]
		case b[0] < 0x20:
			b = b[1:]
		default:
			r, n := utf8.DecodeRune(b)
			if r != utf8.RuneError {
				keys = append(keys, key{r: r})
			}
			b = b[n:]
		}
	}
	return keys
}

type roomEvent struct {
	log []core.Message
	st  *core.State
	err error
}

type actionEvent struct {
	text string
	err  error
}

type watchModel struct {
	ctx context.Context
	s   *session
	u   *ui.UI

	log     []core.Message
	st      *core.State
	lastSeq int
	freshAt map[int]time.Time

	input   []rune
	status  string
	confirm bool
	scroll  int // lines up from the bottom
	frame   int
	w, h    int
	quit    bool
}

func runWatchTUI(ctx context.Context, env *Env, s *session) error {
	in, ok := env.Stdin.(*os.File)
	if !ok {
		return errors.New("watch: stdin is not a terminal")
	}
	old, err := xterm.MakeRaw(in.Fd())
	if err != nil {
		return err
	}
	defer xterm.Restore(in.Fd(), old)
	w := env.Stdout
	fmt.Fprint(w, "\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	defer fmt.Fprint(w, "\x1b[?25h\x1b[?1049l")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := &watchModel{ctx: ctx, s: s, u: ui.New(w), freshAt: map[int]time.Time{}, w: 80, h: 24}
	m.resize()

	keys := make(chan []key, 16)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				keys <- parseKeys(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
	rooms := make(chan roomEvent, 4)
	go m.follow(rooms)
	actions := make(chan actionEvent, 4)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	m.draw(w)
	for !m.quit {
		select {
		case <-ctx.Done():
			return nil
		case ks, ok := <-keys:
			if !ok {
				return nil
			}
			for _, k := range ks {
				m.key(k, actions)
			}
		case ev := <-rooms:
			m.room(ev)
		case ev := <-actions:
			m.status = m.result(ev)
			go func() {
				_, log, st, err := m.s.load(ctx)
				rooms <- roomEvent{log: log, st: st, err: err}
			}()
		case <-tick.C:
			m.frame++
			m.resize()
		}
		m.draw(w)
	}
	return nil
}

func (m *watchModel) resize() {
	if fw, fh, err := xterm.GetSize(os.Stdout.Fd()); err == nil && fw > 0 && fh > 0 {
		m.w, m.h = fw, fh
	}
}

func (m *watchModel) result(ev actionEvent) string {
	if ev.err == nil {
		return m.u.OK(ev.text)
	}
	var ce *core.Error
	if errors.As(ev.err, &ce) {
		return m.u.Fail(m.u.Bold("refused") + m.u.Dim(" · "+ce.Code) + "  " + ce.Msg)
	}
	return m.u.Fail(ev.err.Error())
}

// follow loads the room and then long-polls it, sending every change.
func (m *watchModel) follow(out chan<- roomEvent) {
	after := -1
	for m.ctx.Err() == nil {
		if after >= 0 {
			wctx, cancel := context.WithTimeout(m.ctx, 50*time.Second)
			_, _, err := m.s.room.Wait(wctx, after, func(core.Message) bool { return false })
			cancel()
			if m.ctx.Err() != nil {
				return
			}
			if err != nil {
				out <- roomEvent{err: err}
				time.Sleep(3 * time.Second)
				continue
			}
		}
		_, log, st, err := m.s.load(m.ctx)
		if m.ctx.Err() != nil {
			return
		}
		out <- roomEvent{log: log, st: st, err: err}
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		after = st.LastSeq
		if st.Closed {
			return
		}
	}
}

func (m *watchModel) room(ev roomEvent) {
	if ev.err != nil {
		m.status = m.u.Fail(ev.err.Error())
		return
	}
	if m.st != nil && ev.st.LastSeq < m.st.LastSeq {
		return // an older answer than what is shown
	}
	now := time.Now()
	for _, x := range ev.log {
		if m.lastSeq > 0 && x.Seq > m.lastSeq {
			m.freshAt[x.Seq] = now
		}
	}
	m.log, m.st, m.lastSeq = ev.log, ev.st, ev.st.LastSeq
}

// act posts a message as the human, without printing.
func (m *watchModel) act(msg core.Message, out chan<- actionEvent) {
	go func() {
		msg.Side, msg.Actor = m.s.id.Side, core.Human
		res, st, err := m.s.room.Post(m.ctx, msg, m.s.seen())
		if err != nil {
			out <- actionEvent{err: err}
			return
		}
		m.s.markSeen(res.Seq)
		m.s.saveAgreement(st)
		out <- actionEvent{text: fmt.Sprintf("posted #%d %s %s", res.Seq, ui.KindIcon(res.Kind), res.Kind)}
	}()
}

func (m *watchModel) key(k key, actions chan<- actionEvent) {
	switch k.name {
	case "quit":
		m.quit = true
	case "enter":
		line := strings.TrimSpace(string(m.input))
		m.input = m.input[:0]
		m.command(line, actions)
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case "up":
		m.scroll++
	case "down":
		if m.scroll > 0 {
			m.scroll--
		}
	case "pgup":
		m.scroll += m.h / 2
	case "pgdown":
		m.scroll -= m.h / 2
		if m.scroll < 0 {
			m.scroll = 0
		}
	default:
		if k.r != 0 && len(m.input) < 2000 {
			m.input = append(m.input, k.r)
		}
	}
}

func (m *watchModel) command(line string, actions chan<- actionEvent) {
	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	if m.confirm {
		m.confirm = false
		if strings.EqualFold(line, "yes") {
			m.act(core.Message{Kind: core.Approve, Re: m.st.Resolution, Body: "approved"}, actions)
			return
		}
		m.status = m.u.Warn("not approved")
		return
	}
	switch strings.ToLower(cmd) {
	case "":
	case "q", "quit", "exit":
		m.quit = true
	case "h", "hand":
		if rest == "" {
			rest = "hand raised"
		}
		m.act(core.Message{Kind: core.Hand, Body: rest}, actions)
	case "r", "resume":
		m.act(core.Message{Kind: core.Resume, Body: "resumed"}, actions)
	case "s", "say":
		if rest == "" {
			m.status = m.u.Warn("type the text after s, e.g. " + m.u.Code("s keep it simple"))
			return
		}
		m.act(core.Message{Kind: core.Note, Body: rest}, actions)
	case "a", "approve":
		if m.st == nil || !m.st.Agreed {
			m.status = m.u.Warn("there is no agreed resolution to approve yet")
			return
		}
		m.confirm = true
		m.status = m.u.Accent(fmt.Sprintf("🤝 approve resolution #%d for side %s? type yes", m.st.Resolution, m.s.id.Side))
	case "x", "close":
		if rest == "" {
			rest = "closed"
		}
		m.act(core.Message{Kind: core.Close, Body: rest}, actions)
	default:
		m.status = m.u.Warn("unknown command " + m.u.Code(cmd) + m.u.Dim(" — h reason · r · s text · a · x · q"))
	}
}

func (m *watchModel) spinner() string { return m.u.Accent(ui.Frames[m.frame%len(ui.Frames)]) }

func (m *watchModel) activity(width int) string {
	st := m.st
	if st == nil {
		return m.spinner() + " " + m.u.Dim("loading the room…")
	}
	me := m.s.id.Side
	switch {
	case st.Closed:
		return m.u.Banner(st) + m.u.Dim("   (q to quit)")
	case m.confirm:
		return m.u.Resolution(st, width)
	case st.Pause != nil:
		return m.u.Banner(st) + m.u.Dim("   r = resume · s TEXT = instruct the agents")
	case st.Agreed && !st.Approved[me]:
		return m.spinner() + " " + m.u.Accent(fmt.Sprintf("★ resolution #%d agreed — type a to approve", st.Resolution))
	case st.Agreed:
		return m.spinner() + " " + m.u.Dim("waiting for the other side's human to approve…")
	case st.Floor == me:
		return m.spinner() + " 🤖 " + m.u.Dim("your agent (") + m.u.Side(me) + m.u.Dim(") is up — waiting for it to post…")
	default:
		return m.spinner() + " 🤖 " + m.u.Dim("side ") + m.u.Side(st.Floor) + m.u.Dim("'s agent is thinking…")
	}
}

func (m *watchModel) draw(w io.Writer) {
	width := m.w
	if width > 110 {
		width = 110
	}
	header := m.u.Dim(" connecting…")
	if m.st != nil {
		header = m.u.Header(ui.Header{Topic: m.st.Room.Topic, Slug: m.s.id.Slug, Backend: m.s.id.Backend,
			Side: m.s.id.Side, Actor: core.Human, State: m.st}, width)
	}
	cursor := m.u.Accent("▏")
	input := m.u.Accent("› ") + string(m.input) + cursor
	if len(m.input) == 0 {
		input = m.u.Accent("› ") + cursor + m.u.Dim("h reason · r · s text · a · q")
	}
	help := m.u.Dim(" h reason ✋  r ⏩  s text 📝  a approve 🤝  x close 🔒  q quit   ↑↓ PgUp PgDn scroll")
	bottom := strings.Join([]string{m.activity(width), input, m.status, help}, "\n")

	var body []string
	if len(m.log) == 0 {
		body = append(body, m.u.Dim(" no messages yet — the agents start when side A's agent posts"))
	}
	now := time.Now()
	for _, x := range m.log {
		fresh := now.Sub(m.freshAt[x.Seq]) < freshFor
		body = append(body, strings.Split(strings.TrimRight(m.u.Message(x, m.s.id.Side, fresh, width-1), "\n"), "\n")...)
		body = append(body, "")
	}
	avail := m.h - lipgloss.Height(header) - lipgloss.Height(bottom)
	if avail < 1 {
		avail = 1
	}
	if max := len(body) - avail; m.scroll > max {
		m.scroll = max
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	end := len(body) - m.scroll
	start := end - avail
	if start < 0 {
		start = 0
	}
	view := append([]string(nil), body[start:end]...)
	for len(view) < avail {
		view = append(view, "")
	}
	if m.scroll > 0 {
		view[len(view)-1] = m.u.Dim(fmt.Sprintf(" ↓ %d more lines below", m.scroll))
	}

	lines := strings.Split(header, "\n")
	lines = append(lines, view...)
	lines = append(lines, strings.Split(bottom, "\n")...)
	if len(lines) > m.h {
		lines = lines[len(lines)-m.h:]
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		b.WriteString(l)
		b.WriteString("\x1b[K")
		if i < len(lines)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J")
	io.WriteString(w, b.String())
}
