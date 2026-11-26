// Package ui renders coop for people in a terminal: colours per side,
// icons per kind, cards, banners and spinners. Agents and pipes never see
// it; they get the plain text of package cli.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/xpresstoparadise/coop-cli/internal/core"
)

// Palette.
var (
	colA      = lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#58A6FF"}
	colB      = lipgloss.AdaptiveColor{Light: "#BC4C00", Dark: "#FFA657"}
	colHuman  = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"}
	colBad    = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F85149"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#D29922"}
	colDim    = lipgloss.AdaptiveColor{Light: "#6E7781", Dark: "#8B949E"}
	colAccent = lipgloss.AdaptiveColor{Light: "#8250DF", Dark: "#BC8CFF"}
	colText   = lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#E6EDF3"}
)

// UI renders for one output.
type UI struct {
	r *lipgloss.Renderer
}

// New returns a UI that renders for w.
//
// The background is not queried from the terminal: that query (OSC 11)
// reads the answer from stdin and can swallow what the person types, or
// hang where the terminal does not answer. Dark is assumed; set
// COOP_THEME=light for a light terminal.
func New(w io.Writer) *UI {
	r := lipgloss.NewRenderer(w)
	r.SetHasDarkBackground(os.Getenv("COOP_THEME") != "light")
	return &UI{r: r}
}

func (u *UI) s() lipgloss.Style { return u.r.NewStyle() }

// SideColor is the colour of a side.
func SideColor(side string) lipgloss.TerminalColor {
	if side == "B" {
		return colB
	}
	return colA
}

// ActorIcon is the icon of an actor.
func ActorIcon(a core.Actor) string {
	if a == core.Human {
		return "👤"
	}
	return "🤖"
}

var kindIcons = map[core.Kind]string{
	core.Propose: "💡", core.Ask: "❓", core.Answer: "💬", core.Counter: "🔁",
	core.Accept: "✅", core.Reject: "❌", core.Note: "📝", core.Hand: "✋",
	core.Resume: "⏩", core.Approve: "🤝", core.Close: "🔒",
}

var kindColors = map[core.Kind]lipgloss.TerminalColor{
	core.Propose: colA, core.Ask: colAccent, core.Answer: colText, core.Counter: colB,
	core.Accept: colOK, core.Reject: colBad, core.Note: colDim, core.Hand: colWarn,
	core.Resume: colOK, core.Approve: colOK, core.Close: colDim,
}

// KindIcon is the icon of a message kind.
func KindIcon(k core.Kind) string { return kindIcons[k] }

// ---- small pieces

func (u *UI) Dim(s string) string    { return u.s().Foreground(colDim).Render(s) }
func (u *UI) Bold(s string) string   { return u.s().Bold(true).Render(s) }
func (u *UI) Accent(s string) string { return u.s().Foreground(colAccent).Bold(true).Render(s) }

// Side renders "A" or "B" in its colour.
func (u *UI) Side(side string) string {
	return u.s().Foreground(SideColor(side)).Bold(true).Render(side)
}

// OK, Warn and Fail are status lines.
func (u *UI) OK(s string) string   { return u.s().Foreground(colOK).Render("✓ ") + s }
func (u *UI) Warn(s string) string { return u.s().Foreground(colWarn).Render("! ") + s }
func (u *UI) Fail(s string) string { return u.s().Foreground(colBad).Bold(true).Render("✗ ") + s }

// Hint renders a "next step" line.
func (u *UI) Hint(s string) string { return u.s().Foreground(colDim).Italic(true).Render("→ " + s) }

// Code renders a command the reader can type.
func (u *UI) Code(s string) string {
	return u.s().Foreground(colAccent).Render(s)
}

// ---- message card

// Message renders one message. me is the reader's side; fresh marks a
// message that has just arrived.
func (u *UI) Message(m core.Message, me string, fresh bool, width int) string {
	side := SideColor(m.Side)
	who := ActorIcon(m.Actor) + " " + u.s().Foreground(side).Bold(true).Render(m.Side) + " " + u.Dim(string(m.Actor))
	if m.Side == me {
		who += u.Dim(" (you)")
	}
	kind := KindIcon(m.Kind) + " " + u.s().Foreground(kindColors[m.Kind]).Bold(true).Render(string(m.Kind))
	parts := []string{who, kind}
	if m.Re != 0 {
		parts = append(parts, u.Dim(fmt.Sprintf("↩ #%d", m.Re)))
	}
	if m.Resolution {
		parts = append(parts, u.s().Foreground(colWarn).Bold(true).Render("★ RESOLUTION"))
	}
	if fresh {
		parts = append(parts, u.s().Foreground(colOK).Bold(true).Render("● new"))
	}
	meta := u.Dim(fmt.Sprintf("#%d", m.Seq))
	if !m.At.IsZero() {
		meta += u.Dim(" · " + m.At.Local().Format("15:04"))
	}
	title := strings.Join(parts, "  ")
	if width > 0 {
		gap := width - lipgloss.Width(title) - lipgloss.Width(meta) - 2
		if gap < 1 {
			gap = 1
		}
		title += strings.Repeat(" ", gap) + meta
	} else {
		title += "  " + meta
	}

	barColor := side
	if m.Actor == core.Human {
		barColor = colHuman
	}
	body := u.s().
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(barColor).
		MarginLeft(1).
		PaddingLeft(1).
		Foreground(colText)
	if width > 6 {
		body = body.Width(width - 3)
	}
	if m.Resolution {
		body = body.Bold(true)
	}
	if m.Kind == core.Note || m.Kind == core.Close || m.Kind == core.Resume {
		body = body.Foreground(colDim)
	}
	return " " + title + "\n" + body.Render(m.Body) + "\n"
}

// ---- room header

// Header describes the room for its card.
type Header struct {
	Topic, Slug, Backend string
	Side                 string
	Actor                core.Actor
	State                *core.State
}

func (u *UI) bar(used, budget int, side string) string {
	const n = 10
	if budget <= 0 {
		budget = 1
	}
	full := used * n / budget
	if used > 0 && full == 0 {
		full = 1
	}
	if full > n {
		full = n
	}
	c := SideColor(side)
	if used >= budget {
		c = colBad
	}
	return u.s().Foreground(c).Render(strings.Repeat("█", full)) +
		u.Dim(strings.Repeat("░", n-full)) + u.Dim(fmt.Sprintf(" %d/%d", used, budget))
}

// PhaseBadge renders the phase of a room.
func (u *UI) PhaseBadge(st *core.State) string {
	switch st.Phase() {
	case core.Closed:
		if st.CloseReason == "approved" {
			return u.s().Foreground(colOK).Bold(true).Render("✔ agreed & closed")
		}
		return u.s().Foreground(colDim).Bold(true).Render("■ closed")
	case core.Approval:
		return u.s().Foreground(colWarn).Bold(true).Render("◆ approval")
	}
	return u.s().Foreground(colA).Bold(true).Render("● negotiation")
}

// Header renders the room card.
func (u *UI) Header(h Header, width int) string {
	st := h.State
	r := st.Room
	title := "🏠 " + u.Bold(h.Topic) + u.Dim("  "+h.Slug+" · "+h.Backend)
	you := u.Dim("you ") + ActorIcon(h.Actor) + " " + u.Side(h.Side) + u.Dim(" "+string(h.Actor))
	floor := u.Dim("floor ") + "▶ " + u.Side(st.Floor)
	line2 := you + "   " + u.PhaseBadge(st)
	if !st.Closed && !st.Agreed {
		line2 += "   " + floor
	}
	line3 := u.Side(r.Sides[0]) + " " + u.bar(st.TurnsUsed[r.Sides[0]], r.TurnBudget, r.Sides[0]) +
		"    " + u.Side(r.Sides[1]) + " " + u.bar(st.TurnsUsed[r.Sides[1]], r.TurnBudget, r.Sides[1])
	lines := []string{title, line2, line3}
	if b := u.Banner(st); b != "" {
		lines = append(lines, b)
	}
	box := u.s().Border(lipgloss.RoundedBorder()).BorderForeground(SideColor(h.Side)).Padding(0, 1)
	if width > 4 {
		box = box.Width(width - 2)
	}
	return box.Render(strings.Join(lines, "\n"))
}

// Banner is the line about pauses, resolutions and approval, if any.
func (u *UI) Banner(st *core.State) string {
	switch {
	case st.Closed && st.CloseReason == "approved":
		return u.s().Foreground(colOK).Bold(true).Render(fmt.Sprintf("🎉 agreement reached: resolution #%d by %s", st.Resolution, st.ResolutionSide))
	case st.Closed:
		return u.s().Foreground(colDim).Render("🔒 " + strings.ReplaceAll(st.CloseReason, "_", " "))
	case st.Pause != nil && st.Pause.Kind == core.PauseBudget:
		return u.s().Foreground(colBad).Bold(true).Render("⏸  paused: side " + st.Pause.Side + " used its turn budget — a human resumes")
	case st.Pause != nil:
		return u.s().Foreground(colWarn).Bold(true).Render(fmt.Sprintf("✋ paused: hand raised by %s %s", st.Pause.Side, st.Pause.Actor))
	case st.Agreed:
		return u.s().Foreground(colWarn).Bold(true).Render(fmt.Sprintf("★ resolution #%d agreed — humans approve: %d of 2", st.Resolution, len(st.Approved)))
	case st.Resolution != 0:
		return u.s().Foreground(colWarn).Render(fmt.Sprintf("★ resolution #%d by %s on the table", st.Resolution, st.ResolutionSide))
	}
	return ""
}

// Resolution renders the resolution under approval in a frame.
func (u *UI) Resolution(st *core.State, width int) string {
	head := u.s().Foreground(colWarn).Bold(true).Render(fmt.Sprintf("★ Resolution #%d", st.Resolution)) +
		u.Dim("  by side ") + u.Side(st.ResolutionSide)
	box := u.s().Border(lipgloss.DoubleBorder()).BorderForeground(colWarn).Padding(1, 2)
	if width > 6 && width < 100 {
		box = box.Width(width - 2)
	} else if width >= 100 {
		box = box.Width(96)
	}
	return box.Render(head + "\n\n" + st.ResolutionBody())
}

// Invitation renders an invitation to pass on.
func (u *UI) Invitation(inv string) string {
	box := u.s().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1)
	return box.Render("📨 " + u.Bold("invitation") + u.Dim(" · single use · keep it secret") + "\n\n" +
		u.Code("coop join '"+inv+"'"))
}

// ---- spinner

// Frames of the spinner.
var Frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spin shows an animated spinner with text on w while fn runs.
func (u *UI) Spin(w io.Writer, text string, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	start := time.Now()
	for i := 0; ; i++ {
		select {
		case err := <-done:
			fmt.Fprint(w, "\r\x1b[2K")
			return err
		case <-tick.C:
			if time.Since(start) < 150*time.Millisecond {
				continue // do not flash for fast operations
			}
			frame := u.s().Foreground(colAccent).Render(Frames[i%len(Frames)])
			el := ""
			if s := time.Since(start); s > 3*time.Second {
				el = u.Dim(fmt.Sprintf("  %ds", int(s.Seconds())))
			}
			fmt.Fprint(w, "\r\x1b[2K"+frame+" "+text+el)
		}
	}
}
