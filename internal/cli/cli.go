// Package cli implements the coop command.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/xpresstoparadise/coop-cli/internal/core"
	"github.com/xpresstoparadise/coop-cli/internal/gitroom"
	"github.com/xpresstoparadise/coop-cli/internal/local"
	"github.com/xpresstoparadise/coop-cli/internal/serverroom"
	"github.com/xpresstoparadise/coop-cli/internal/userconf"
)

const usage = `coop — rooms for AI agents working in different terminals

Usage:
  coop open --topic TEXT [--slug NAME] [--turns N] [--remote]
                              open a room in coop/<slug>/ as side A
  coop login --server URL [--key KEY]
                              remember your coop server (and host key)
  coop open --topic TEXT [--server URL] [--key KEY] [--git]
                              open a room on your server (or, with --git or
                              no server configured, in coop/ of this repo);
                              prints an invite code
  coop join CODE              join a server room by its invite code
  coop join [SLUG] [--side B] [--remote]
                              join a room of this repository
  coop join INVITATION        join a server room with the invitation link
  coop status                 phase, floor, turns, resolution
  coop read [--all]           print new messages
  coop wait [--timeout 10m]   block until the other side posts
  coop say [--kind K] [--re N] [--resolution] TEXT|-
                              post a message (TEXT, or - for stdin)
  coop hand [REASON]          pause the agents
  coop resume                 lift the pause
  coop approve                approve the agreed resolution (asks on a TTY)
  coop close [REASON]         close the room
  coop watch [--last 20]      follow the room live; type h/r/s/q to act
  coop export [--format md|json] [-o FILE]
                              minutes of the room
  coop setup AGENT...|all [--command PATH]
                              connect an agent to coop in this project:
                              claude, cursor, codex, gemini, opencode,
                              kimi (Kimi Code), zcode (Z.ai)
  coop mcp [--side A|B] [--dir PATH] [--max-wait 5m]
                              serve the agent's tools over MCP (stdio)

Server (run by the admin):
  coop serve [--db coop.db] [--addr 127.0.0.1:8470] [--base-url URL]
  coop key add --name NAME    issue a host key (printed once)
  coop key list | coop key revoke ID
  coop purge                  delete rooms past retention (serve does it hourly)

  coop version

Every room command takes --side A|B (default: $COOP_SIDE or the only side
in .coop/). The command acts as the human; read, wait, say and hand take
--agent to act as the agent.

Kinds: propose ask answer counter accept reject note hand resume approve close
`

// Version is set at build time.
var Version = "dev"

// Env is what a command may touch. Tests replace it.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Dir            string      // working folder
	IsTTY          func() bool // stdin is an interactive terminal
	StdoutTTY      func() bool // stdout is a terminal (styled output allowed)
	Width          func() int  // terminal width, 0 if unknown
}

// OSEnv is the environment of the running process.
func OSEnv() Env {
	wd, _ := os.Getwd()
	return Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Dir: wd,
		IsTTY: func() bool {
			fi, err := os.Stdin.Stat()
			return err == nil && fi.Mode()&os.ModeCharDevice != 0
		},
		StdoutTTY: func() bool { return xterm.IsTerminal(os.Stdout.Fd()) },
		Width: func() int {
			w, _, err := xterm.GetSize(os.Stdout.Fd())
			if err != nil {
				return 0
			}
			return w
		},
	}
}

// Run runs the command line args (without the program name) and returns an
// exit code.
func Run(ctx context.Context, env Env, args []string) int {
	enableVT()
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}
	name := args[0]
	switch name {
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(env.Stdout, Version)
		return 0
	}
	if commands[name] == nil {
		fmt.Fprintf(env.Stderr, "coop: unknown command %q\n\n%s", name, usage)
		return 2
	}
	if err := runCmd(ctx, &env, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		var ce *core.Error
		if errors.As(err, &ce) {
			// Keep text that callers wrapped around the rule, such as hints.
			extra := strings.TrimPrefix(err.Error(), ce.Error())
			fmt.Fprintln(env.Stderr, failText(&env, ce.Code, ce.Msg, extra))
		} else {
			fmt.Fprintln(env.Stderr, failText(&env, "", err.Error(), ""))
		}
		return 1
	}
	return 0
}

var commands map[string]func(context.Context, *Env, []string) error

func init() {
	commands = map[string]func(context.Context, *Env, []string) error{
		"open": cmdOpen, "join": cmdJoin, "status": cmdStatus, "read": cmdRead,
		"wait": cmdWait, "say": cmdSay, "hand": cmdHand, "resume": cmdResume,
		"approve": cmdApprove, "close": cmdClose, "mcp": cmdMCP, "setup": cmdSetup, "login": cmdLogin,
		"watch": cmdWatch, "export": cmdExport,
		"serve": cmdServe, "key": cmdKey, "purge": cmdPurge,
	}
}

// runCmd runs one command; args[0] is its name.
func runCmd(ctx context.Context, env *Env, args []string) error {
	cmd := commands[args[0]]
	if cmd == nil {
		return fmt.Errorf("unknown command %q", args[0])
	}
	return cmd(ctx, env, args[1:])
}

// ---- shared plumbing

// projectRoot is the git top level of dir, or dir itself.
func projectRoot(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return dir
}

type session struct {
	env   *Env
	root  string
	id    *local.Identity
	room  backend
	actor core.Actor
	out   *out
}

func (s *session) seen() int {
	if s.actor == core.Agent {
		return s.id.SeenAgent
	}
	return s.id.SeenHuman
}

func (s *session) markSeen(seq int) error {
	if seq <= s.seen() {
		return nil
	}
	if s.actor == core.Agent {
		s.id.SeenAgent = seq
	} else {
		s.id.SeenHuman = seq
	}
	return s.id.Save(s.root)
}

func (s *session) mine(m core.Message) bool { return m.Side == s.id.Side && m.Actor == s.actor }

type sessionFlags struct {
	side  *string
	agent *bool
}

func newFlags(name string, env *Env, withAgent bool) (*flag.FlagSet, sessionFlags) {
	fs := flag.NewFlagSet("coop "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	sf := sessionFlags{side: fs.String("side", "", "side to act as (A or B)")}
	if withAgent {
		sf.agent = fs.Bool("agent", false, "act as the agent, not the human")
	} else {
		f := false
		sf.agent = &f
	}
	return fs, sf
}

func openSession(ctx context.Context, env *Env, sf sessionFlags) (*session, error) {
	root := projectRoot(ctx, env.Dir)
	id, err := local.Load(root, *sf.side)
	if err != nil {
		return nil, err
	}
	actor := core.Human
	if *sf.agent {
		actor = core.Agent
	}
	room, err := openBackend(ctx, root, id, actor)
	if err != nil {
		return nil, err
	}
	return &session{env: env, root: root, id: id, room: room, actor: actor, out: newOut(env, actor)}, nil
}

func (s *session) post(ctx context.Context, m core.Message) error {
	m.Side, m.Actor = s.id.Side, s.actor
	var out core.Message
	var st *core.State
	err := s.out.spin("sending "+string(m.Kind)+"…", func() error {
		var err error
		out, st, err = s.room.Post(ctx, m, s.seen())
		return err
	})
	if err != nil {
		if errors.Is(err, core.ErrUnreadHuman) {
			return fmt.Errorf("%w — run `coop read --agent` first", err)
		}
		return err
	}
	// Our own message is read by definition; earlier ones were checked.
	if err := s.markSeen(out.Seq); err != nil {
		return err
	}
	s.out.posted(out)
	if err := s.saveAgreement(st); err != nil {
		return err
	}
	if st.Closed && st.CloseReason == "approved" {
		p := agreementPath(s.root, s.id)
		s.out.say("both sides approved; agreement saved to "+p,
			s.out.u.Banner(st)+"\n"+s.out.u.OK("agreement saved to "+s.out.u.Code(p)))
	}
	s.out.hint(st, s.id.Side, s.actor)
	return nil
}

func readBody(env *Env, args []string) (string, error) {
	if len(args) == 1 && args[0] == "-" || len(args) == 0 && !env.IsTTY() {
		b, err := io.ReadAll(io.LimitReader(env.Stdin, core.MaxBody+1))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\n"), nil
	}
	return strings.Join(args, " "), nil
}

// ---- commands

func cmdOpen(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("coop open", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	topic := fs.String("topic", "", "what the room is about (required)")
	slug := fs.String("slug", "", "folder name under coop/ (default: from the topic)")
	turns := fs.Int("turns", core.DefaultTurnBudget, "agent turns per side before the room pauses")
	remote := fs.Bool("remote", false, "commit, push and pull every message through git")
	server := fs.String("server", "", "open the room on this coop server (default: from `coop login` or $COOP_SERVER)")
	hostKey := fs.String("key", "", "host key for the server (default: from `coop login` or $COOP_HOST_KEY)")
	gitMode := fs.Bool("git", false, "open the room in this repository's coop/ folder even if a server is configured")
	if err := parse(fs, args); err != nil {
		return err
	}
	conf, err := userconf.Load()
	if err != nil {
		return err
	}
	if *server == "" && !*gitMode && !*remote && *slug == "" {
		*server = conf.Server
	}
	if *hostKey == "" {
		*hostKey = conf.HostKey
	}
	if *topic == "" {
		*topic = strings.Join(fs.Args(), " ")
	}
	if *topic == "" {
		return errors.New("open needs --topic")
	}
	if *slug == "" {
		*slug = gitroom.Slugify(*topic)
	}
	root := projectRoot(ctx, env.Dir)
	room := core.NewRoom(*topic, *turns)
	side := room.Sides[0]
	if id, err := local.Load(root, side); err == nil {
		return fmt.Errorf("side %s here is already in room %q (remove .coop/%s.json to leave it)", side, id.Slug, side)
	}
	if *server != "" {
		return openOnServer(ctx, env, root, *server, *hostKey, *slug, room)
	}
	if _, err := gitroom.Create(ctx, gitroom.Options{Root: root, Slug: *slug, Remote: *remote}, room); err != nil {
		return err
	}
	if err := local.EnsureIgnored(root); err != nil {
		return err
	}
	id := &local.Identity{Backend: "git", Slug: *slug, Side: side, Remote: *remote}
	if err := id.Save(root); err != nil {
		return err
	}
	join := "coop join " + *slug
	if *remote {
		join += " --remote"
	}
	o := newOut(env, core.Human)
	o.say(fmt.Sprintf("opened room %q in coop/%s as side %s", *topic, *slug, side),
		o.u.OK(fmt.Sprintf("opened %s in %s as side %s", o.u.Bold(*topic), o.u.Code("coop/"+*slug), o.u.Side(side))))
	o.say("the other side runs: "+join, "  👉 the other side runs: "+o.u.Code(join))
	o.say("(in this same folder: COOP_SIDE=B "+join+")", o.u.Dim("     in this same folder: COOP_SIDE=B "+join))
	return nil
}

func cmdJoin(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("coop join", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	side := fs.String("side", "", "side to join as (default: $COOP_SIDE or B)")
	remote := fs.Bool("remote", false, "commit, push and pull every message through git")
	serverFlag := fs.String("server", "", "server for an invite code (default: from `coop login` or $COOP_SERVER)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *side == "" {
		*side = os.Getenv("COOP_SIDE")
	}
	if *side == "" {
		*side = "B"
	}
	root := projectRoot(ctx, env.Dir)
	slug := fs.Arg(0)
	if serverroom.IsInvitation(slug) {
		return joinOnServer(ctx, env, root, slug)
	}
	if serverroom.LooksLikeCode(slug) {
		if _, err := os.Stat(filepath.Join(root, "coop", slug)); err != nil { // not a git room of that name
			return joinByCode(ctx, env, root, *serverFlag, slug)
		}
	}
	if slug == "" {
		entries, _ := os.ReadDir(filepath.Join(root, "coop"))
		var rooms []string
		for _, e := range entries {
			if e.IsDir() {
				rooms = append(rooms, e.Name())
			}
		}
		if len(rooms) != 1 {
			return fmt.Errorf("say which room to join; rooms here: %s", strings.Join(rooms, ", "))
		}
		slug = rooms[0]
	}
	if id, err := local.Load(root, *side); err == nil {
		return fmt.Errorf("side %s here is already in room %q (remove .coop/%s.json to leave it)", *side, id.Slug, *side)
	}
	room, err := gitroom.Open(ctx, gitroom.Options{Root: root, Slug: slug, Remote: *remote})
	if err != nil {
		return err
	}
	r, _, st, err := room.Load()
	if err != nil {
		return err
	}
	if *side != r.Sides[0] && *side != r.Sides[1] {
		return fmt.Errorf("room %q has sides %s and %s", slug, r.Sides[0], r.Sides[1])
	}
	if err := local.EnsureIgnored(root); err != nil {
		return err
	}
	id := &local.Identity{Backend: "git", Slug: slug, Side: *side, Remote: *remote}
	if err := id.Save(root); err != nil {
		return err
	}
	o := newOut(env, core.Human)
	o.say(fmt.Sprintf("joined room %q as side %s", r.Topic, *side), o.u.OK("joined as side "+o.u.Side(*side)))
	o.header(st, id, core.Human)
	return nil
}

func cmdStatus(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("status", env, true)
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
	unread := 0
	for _, m := range log {
		if m.Seq > s.seen() && !s.mine(m) {
			unread++
		}
	}
	s.out.header(st, s.id, s.actor)
	s.out.say(fmt.Sprintf("unread: %d", unread), s.out.u.Dim(fmt.Sprintf("📬 unread: %d", unread)))
	s.out.hint(st, s.id.Side, s.actor)
	return nil
}

func cmdRead(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("read", env, true)
	all := fs.Bool("all", false, "print the whole log, not just new messages")
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
	s.out.header(st, s.id, s.actor)
	n := 0
	for _, m := range log {
		if *all || m.Seq > s.seen() {
			s.out.message(m, s.id.Side, !*all && !s.mine(m))
			n++
		}
	}
	if n == 0 {
		s.out.say("no new messages", s.out.u.Dim("📭 no new messages"))
	}
	s.out.hint(st, s.id.Side, s.actor)
	return s.markSeen(st.LastSeq)
}

func cmdWait(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("wait", env, true)
	timeout := fs.Duration("timeout", 10*time.Minute, "give up after this long")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var fresh []core.Message
	var st *core.State
	err = s.out.spin("waiting for the other side…", func() error {
		var err error
		fresh, st, err = s.room.Wait(wctx, s.seen(), s.mine)
		return err
	})
	if err != nil {
		return err
	}
	if err := s.saveAgreement(st); err != nil {
		return err
	}
	if st == nil {
		return ctx.Err()
	}
	for _, m := range fresh {
		s.out.message(m, s.id.Side, true)
	}
	if len(fresh) == 0 && !st.Closed {
		s.out.say(fmt.Sprintf("no new messages after %s; call wait again", *timeout),
			s.out.u.Dim(fmt.Sprintf("⌛ nothing new after %s", *timeout)))
	}
	s.out.header(st, s.id, s.actor)
	s.out.hint(st, s.id.Side, s.actor)
	if len(fresh) > 0 {
		return s.markSeen(fresh[len(fresh)-1].Seq)
	}
	return nil
}

func cmdSay(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("say", env, true)
	kind := fs.String("kind", "", "message kind (default for a human: note; required for an agent)")
	reSeq := fs.Int("re", 0, "seq of the message this one answers")
	resolution := fs.Bool("resolution", false, "mark a propose or counter as the proposed final agreement")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *kind == "" {
		if *sf.agent {
			return errors.New("an agent must give --kind (propose, ask, answer, counter, accept, reject, note)")
		}
		*kind = string(core.Note)
	}
	switch core.Kind(*kind) {
	case core.Hand, core.Resume, core.Approve, core.Close:
		return fmt.Errorf("use `coop %s` for %s", *kind, *kind)
	}
	body, err := readBody(env, fs.Args())
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("no message text: put it at the end of the command, in quotes, or use - to read it from stdin")
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	return s.post(ctx, core.Message{Kind: core.Kind(*kind), Re: *reSeq, Resolution: *resolution, Body: body})
}

func cmdHand(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("hand", env, true)
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	body := strings.Join(fs.Args(), " ")
	if body == "" {
		body = "hand raised"
	}
	return s.post(ctx, core.Message{Kind: core.Hand, Body: body})
}

func cmdResume(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("resume", env, false)
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	body := strings.Join(fs.Args(), " ")
	if body == "" {
		body = "resumed"
	}
	return s.post(ctx, core.Message{Kind: core.Resume, Body: body})
}

func cmdClose(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("close", env, false)
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	body := strings.Join(fs.Args(), " ")
	if body == "" {
		body = "closed"
	}
	return s.post(ctx, core.Message{Kind: core.Close, Body: body})
}

func cmdApprove(ctx context.Context, env *Env, args []string) error {
	fs, sf := newFlags("approve", env, false)
	if err := parse(fs, args); err != nil {
		return err
	}
	if !env.IsTTY() {
		return errors.New("approve must be run by a person in an interactive terminal")
	}
	s, err := openSession(ctx, env, sf)
	if err != nil {
		return err
	}
	_, _, st, err := s.load(ctx)
	if err != nil {
		return err
	}
	if !st.Agreed {
		return core.ErrNoResolution
	}
	if s.out.on {
		fmt.Fprintln(env.Stdout, s.out.u.Resolution(st, env.width()))
		fmt.Fprintf(env.Stdout, "\n🤝 Approve it for side %s? %s ", s.out.u.Side(s.id.Side), s.out.u.Accent("type yes ›"))
	} else {
		fmt.Fprintf(env.Stdout, "Resolution #%d by side %s:\n\n%s\n\nApprove it for side %s? Type yes: ",
			st.Resolution, st.ResolutionSide, st.ResolutionBody(), s.id.Side)
	}
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return errors.New("not approved")
	}
	return s.post(ctx, core.Message{Kind: core.Approve, Re: st.Resolution, Body: "approved"})
}

// ---- output

// format renders a message for agents and humans alike. The body is
// wrapped so that an agent can tell which side wrote it; a closing tag
// inside the body is escaped so it cannot end the wrapper early.
func format(m core.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<from side="%s" actor="%s" seq="%d" kind="%s"`, m.Side, m.Actor, m.Seq, m.Kind)
	if m.Re != 0 {
		fmt.Fprintf(&b, ` re="%d"`, m.Re)
	}
	if m.Resolution {
		b.WriteString(` resolution="true"`)
	}
	b.WriteString(">\n")
	b.WriteString(strings.ReplaceAll(m.Body, "</from", `<\/from`))
	b.WriteString("\n</from>\n")
	return b.String()
}

func header(st *core.State, id *local.Identity, actor core.Actor) string {
	r := st.Room
	s := fmt.Sprintf("room %s (%q) · you: side %s, %s · phase %s · floor %s · turns %s %d/%d, %s %d/%d",
		id.Slug, r.Topic, id.Side, actor, st.Phase(), st.Floor,
		r.Sides[0], st.TurnsUsed[r.Sides[0]], r.TurnBudget, r.Sides[1], st.TurnsUsed[r.Sides[1]], r.TurnBudget)
	if st.Pause != nil {
		s += fmt.Sprintf(" · paused (%s by %s %s)", st.Pause.Kind, st.Pause.Side, st.Pause.Actor)
	}
	if st.Resolution != 0 {
		s += fmt.Sprintf(" · resolution #%d by %s", st.Resolution, st.ResolutionSide)
		if st.Agreed {
			s += fmt.Sprintf(", agreed, approved by %d of 2", len(st.Approved))
		}
	}
	if st.Closed {
		s += " · closed: " + st.CloseReason
	}
	return s
}

// hint tells the reader what it can do next.
func hint(st *core.State, side string, actor core.Actor) string {
	switch {
	case st.Closed:
		return "next: the room is closed"
	case actor == core.Human && st.Agreed && !st.Approved[side]:
		return "next: review the resolution and run `coop approve`, or reject it with `coop say --kind reject --re N`"
	case actor == core.Human && st.Pause != nil:
		return "next: the agents are paused; `coop say` to instruct, `coop resume` to continue"
	case actor == core.Human:
		return "next: `coop wait` or `coop read` to follow, `coop hand` to pause the agents"
	case st.Agreed:
		return "next: a resolution is agreed; keep calling `coop wait --agent` until the room closes and tell your human about every new message (you may only post notes now)"
	case st.Pause != nil:
		return "next: paused; wait for a human (`coop wait --agent`)"
	case st.Floor == side:
		return "next: your turn (`coop say --agent --kind ...`)"
	default:
		return "next: the other side's turn (`coop wait --agent`)"
	}
}

// parse parses flags wherever they appear among the arguments, so that
// `coop say "text" --agent` works. Arguments after "--" are positional.
func parse(fs *flag.FlagSet, args []string) error {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if len(rest) < len(args) && args[len(args)-len(rest)-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
	return fs.Parse(append([]string{"--"}, pos...))
}
