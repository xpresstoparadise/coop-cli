package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xpresstoparadise/coop-cli/internal/core"
	"github.com/xpresstoparadise/coop-cli/internal/ui"
)

// The live view of `coop watch` for a person at a terminal.

const freshFor = 4 * time.Second

type roomMsg struct {
	log      []core.Message
	st       *core.State
	err      error
	fromWait bool
}

type actionMsg struct {
	text string
	err  error
}

type secondMsg time.Time

type watchModel struct {
	ctx context.Context
	s   *session
	u   *ui.UI

	log     []core.Message
	st      *core.State
	lastSeq int
	freshAt map[int]time.Time
	waiting bool

	vp    viewport.Model
	sp    spinner.Model
	in    textinput.Model
	w, h  int
	ready bool

	status   string
	confirm  bool // the next "yes" approves
	quitting bool
}

func runWatchTUI(ctx context.Context, env *Env, s *session) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := textinput.New()
	in.Placeholder = "h reason · r · s text · a · q"
	in.Prompt = "› "
	in.CharLimit = core.MaxBody
	in.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	m := &watchModel{ctx: ctx, s: s, u: ui.New(env.Stdout), freshAt: map[int]time.Time{}, in: in, sp: sp}
	m.sp.Style = lipgloss.NewStyle().Foreground(ui.SideColor(s.id.Side))
	lipgloss.SetHasDarkBackground(os.Getenv("COOP_THEME") != "light") // no OSC 11 query
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithMouseCellMotion(),
		tea.WithInput(env.Stdin), tea.WithOutput(env.Stdout))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m *watchModel) Init() tea.Cmd {
	return tea.Batch(m.load(), m.sp.Tick, textinput.Blink, everySecond())
}

func everySecond() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return secondMsg(t) })
}

func (m *watchModel) load() tea.Cmd {
	return func() tea.Msg {
		_, log, st, err := m.s.load(m.ctx)
		return roomMsg{log: log, st: st, err: err}
	}
}

func (m *watchModel) wait(after int) tea.Cmd {
	return func() tea.Msg {
		wctx, cancel := context.WithTimeout(m.ctx, 50*time.Second)
		_, _, err := m.s.room.Wait(wctx, after, func(core.Message) bool { return false })
		cancel()
		if m.ctx.Err() != nil {
			return nil
		}
		if err != nil {
			time.Sleep(3 * time.Second)
			return roomMsg{err: err, fromWait: true}
		}
		_, log, st, err := m.s.load(m.ctx)
		return roomMsg{log: log, st: st, err: err, fromWait: true}
	}
}

// act posts a message as the human, without printing anything.
func (m *watchModel) act(msg core.Message) tea.Cmd {
	return func() tea.Msg {
		msg.Side, msg.Actor = m.s.id.Side, core.Human
		out, st, err := m.s.room.Post(m.ctx, msg, m.s.seen())
		if err != nil {
			return actionMsg{err: err}
		}
		m.s.markSeen(out.Seq)
		m.s.saveAgreement(st)
		return actionMsg{text: fmt.Sprintf("posted #%d %s %s", out.Seq, ui.KindIcon(out.Kind), out.Kind)}
	}
}

func (m *watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.in.Width = m.w - 4
		m.layout()
	case roomMsg:
		if msg.fromWait {
			m.waiting = false
		}
		if msg.err != nil {
			m.status = m.u.Fail(msg.err.Error())
		} else {
			now := time.Now()
			for _, x := range msg.log {
				if m.lastSeq > 0 && x.Seq > m.lastSeq {
					m.freshAt[x.Seq] = now
				}
			}
			m.log, m.st, m.lastSeq = msg.log, msg.st, msg.st.LastSeq
			m.layout() // the header and banners change height
			m.render(true)
		}
		if !m.waiting && (m.st == nil || !m.st.Closed) {
			m.waiting = true
			cmds = append(cmds, m.wait(m.lastSeq))
		}
	case actionMsg:
		if msg.err != nil {
			var ce *core.Error
			if errors.As(msg.err, &ce) {
				m.status = m.u.Fail(m.u.Bold("refused") + m.u.Dim(" · "+ce.Code) + "  " + ce.Msg)
			} else {
				m.status = m.u.Fail(msg.err.Error())
			}
		} else {
			m.status = m.u.OK(msg.text)
		}
		cmds = append(cmds, m.load())
	case secondMsg:
		m.layout()
		cmds = append(cmds, everySecond())
	case spinner.TickMsg:
		var c tea.Cmd
		m.sp, c = m.sp.Update(msg)
		cmds = append(cmds, c)
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEnter:
			line := strings.TrimSpace(m.in.Value())
			m.in.Reset()
			if c := m.command(line); c != nil {
				cmds = append(cmds, c)
			}
			m.layout()
			if m.quitting {
				return m, tea.Quit
			}
			return m, tea.Batch(cmds...)
		case tea.KeyPgUp, tea.KeyPgDown, tea.KeyUp, tea.KeyDown:
			var c tea.Cmd
			m.vp, c = m.vp.Update(msg)
			return m, c
		}
	case tea.MouseMsg:
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		return m, c
	}
	var c tea.Cmd
	m.in, c = m.in.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

func (m *watchModel) command(line string) tea.Cmd {
	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	if m.confirm {
		m.confirm = false
		if strings.EqualFold(line, "yes") {
			return m.act(core.Message{Kind: core.Approve, Re: m.st.Resolution, Body: "approved"})
		}
		m.status = m.u.Warn("not approved")
		return nil
	}
	switch strings.ToLower(cmd) {
	case "":
		return nil
	case "q", "quit", "exit":
		m.quitting = true
		return nil
	case "h", "hand":
		if rest == "" {
			rest = "hand raised"
		}
		return m.act(core.Message{Kind: core.Hand, Body: rest})
	case "r", "resume":
		return m.act(core.Message{Kind: core.Resume, Body: "resumed"})
	case "s", "say":
		if rest == "" {
			m.status = m.u.Warn("type the text after s, e.g. " + m.u.Code("s keep it simple"))
			return nil
		}
		return m.act(core.Message{Kind: core.Note, Body: rest})
	case "a", "approve":
		if m.st == nil || !m.st.Agreed {
			m.status = m.u.Warn("there is no agreed resolution to approve yet")
			return nil
		}
		m.confirm = true
		m.status = m.u.Accent(fmt.Sprintf("🤝 approve resolution #%d for side %s? type yes", m.st.Resolution, m.s.id.Side))
		return nil
	case "x", "close":
		if rest == "" {
			rest = "closed"
		}
		return m.act(core.Message{Kind: core.Close, Body: rest})
	}
	m.status = m.u.Warn("unknown command " + m.u.Code(cmd) + m.u.Dim(" — h reason · r · s text · a · x · q"))
	return nil
}

func (m *watchModel) headerView() string {
	if m.st == nil {
		return m.u.Dim(" connecting…")
	}
	return m.u.Header(ui.Header{Topic: m.st.Room.Topic, Slug: m.s.id.Slug, Backend: m.s.id.Backend,
		Side: m.s.id.Side, Actor: core.Human, State: m.st}, m.w)
}

func (m *watchModel) activity() string {
	st := m.st
	if st == nil {
		return m.sp.View() + " " + m.u.Dim("loading the room…")
	}
	me := m.s.id.Side
	switch {
	case st.Closed:
		return m.u.Banner(st) + m.u.Dim("   (q to quit)")
	case m.confirm:
		return m.u.Resolution(st, m.w)
	case st.Pause != nil:
		return m.u.Banner(st) + m.u.Dim("   r = resume · s TEXT = instruct the agents")
	case st.Agreed && !st.Approved[me]:
		return m.sp.View() + " " + m.u.Accent(fmt.Sprintf("★ resolution #%d agreed — type a to approve", st.Resolution))
	case st.Agreed:
		return m.sp.View() + " " + m.u.Dim("waiting for the other side's human to approve…")
	case st.Floor == me:
		return m.sp.View() + " 🤖 " + m.u.Dim("your agent (") + m.u.Side(me) + m.u.Dim(") is up — waiting for it to post…")
	default:
		return m.sp.View() + " 🤖 " + m.u.Dim("side ") + m.u.Side(st.Floor) + m.u.Dim("'s agent is thinking…")
	}
}

func (m *watchModel) layout() {
	if m.w == 0 {
		return // no window size yet
	}
	// input, status and help take one line each
	used := lipgloss.Height(m.headerView()) + lipgloss.Height(m.activity()) + 3
	h := m.h - used
	if h < 3 {
		h = 3
	}
	if !m.ready {
		m.vp = viewport.New(m.w, h)
		m.ready = true
	} else {
		m.vp.Width, m.vp.Height = m.w, h
	}
	m.render(false)
}

// render rebuilds the message list; bottom scrolls to the end.
func (m *watchModel) render(bottom bool) {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	var b strings.Builder
	if len(m.log) == 0 {
		b.WriteString(m.u.Dim(" no messages yet — the agents start when side A's agent posts\n"))
	}
	now := time.Now()
	for _, x := range m.log {
		fresh := now.Sub(m.freshAt[x.Seq]) < freshFor
		b.WriteString(m.u.Message(x, m.s.id.Side, fresh, m.w-1))
		b.WriteByte('\n')
	}
	m.vp.SetContent(b.String())
	if bottom || atBottom {
		m.vp.GotoBottom()
	}
}

func (m *watchModel) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return m.sp.View() + " loading…"
	}
	help := m.u.Dim(" h reason ✋  r ⏩  s text 📝  a approve 🤝  x close 🔒  q quit   ↑↓ scroll")
	status := m.status
	if status == "" {
		status = " "
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		m.vp.View(),
		m.activity(),
		m.in.View(),
		status,
		help,
	)
}
