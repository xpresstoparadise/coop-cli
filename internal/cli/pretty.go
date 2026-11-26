package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/xpresstoparadise/coop-cli/internal/core"
	"github.com/xpresstoparadise/coop-cli/internal/local"
	"github.com/xpresstoparadise/coop-cli/internal/ui"
)

// pretty reports whether output may be styled: stdout is a terminal and
// the reader has not asked for plain text.
func (e *Env) pretty() bool {
	if e.StdoutTTY == nil || !e.StdoutTTY() {
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || os.Getenv("COOP_PLAIN") != "" {
		return false
	}
	return true
}

func (e *Env) width() int {
	if e.Width == nil {
		return 0
	}
	w := e.Width()
	if w > 110 {
		w = 110
	}
	return w
}

// out prints either the styled view (a person at a terminal) or the plain
// text agents and scripts rely on. Agents always get plain text.
type out struct {
	env *Env
	u   *ui.UI
	on  bool
}

func newOut(env *Env, actor core.Actor) *out {
	return &out{env: env, u: ui.New(env.Stdout), on: actor == core.Human && env.pretty()}
}

func (o *out) println(s string) { fmt.Fprintln(o.env.Stdout, s) }

// say prints plain or styled text.
func (o *out) say(plain, styled string) {
	if o.on {
		o.println(styled)
	} else {
		o.println(plain)
	}
}

func (o *out) header(st *core.State, id *local.Identity, actor core.Actor) {
	if !o.on {
		o.println(header(st, id, actor))
		return
	}
	o.println(o.u.Header(ui.Header{Topic: st.Room.Topic, Slug: id.Slug, Backend: id.Backend, Side: id.Side, Actor: actor, State: st}, o.env.width()))
}

func (o *out) message(m core.Message, me string, fresh bool) {
	if !o.on {
		fmt.Fprint(o.env.Stdout, format(m))
		return
	}
	fmt.Fprint(o.env.Stdout, o.u.Message(m, me, fresh, o.env.width()))
}

func (o *out) hint(st *core.State, side string, actor core.Actor) {
	h := hint(st, side, actor)
	if !o.on {
		o.println(h)
		return
	}
	o.println(o.u.Hint(strings.TrimPrefix(h, "next: ")))
}

func (o *out) posted(m core.Message) {
	o.say(fmt.Sprintf("posted #%d %s", m.Seq, m.Kind),
		o.u.OK(fmt.Sprintf("posted %s %s %s", o.u.Dim(fmt.Sprintf("#%d", m.Seq)), ui.KindIcon(m.Kind), o.u.Bold(string(m.Kind)))))
}

// spin runs fn, with a spinner when styled.
func (o *out) spin(text string, fn func() error) error {
	if !o.on {
		return fn()
	}
	return o.u.Spin(o.env.Stderr, text, fn)
}

// fail prints an error for Run.
func failText(env *Env, code, msg, extra string) string {
	if !env.pretty() {
		s := "coop: "
		if code != "" {
			s += "refused (" + code + "): "
		}
		return s + msg + extra
	}
	u := ui.New(env.Stderr)
	s := msg
	if code != "" {
		s = u.Bold("refused") + u.Dim(" · "+code) + "  " + msg
	}
	if extra = strings.TrimSpace(strings.TrimPrefix(extra, " — ")); extra != "" {
		s += "\n  " + u.Hint(extra)
	}
	return u.Fail(s)
}
