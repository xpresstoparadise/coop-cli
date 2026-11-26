package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xpresstoparadise/coop-cli/internal/api"
	"github.com/xpresstoparadise/coop-cli/internal/core"
	"github.com/xpresstoparadise/coop-cli/internal/gitroom"
	"github.com/xpresstoparadise/coop-cli/internal/local"
	"github.com/xpresstoparadise/coop-cli/internal/serverroom"
)

// backend is where a room lives. Both backends apply the core rules: the
// git backend in this process, the server backend on the server.
type backend interface {
	Load(ctx context.Context) (core.Room, []core.Message, *core.State, error)
	Post(ctx context.Context, m core.Message, seen int) (core.Message, *core.State, error)
	Wait(ctx context.Context, after int, mine func(core.Message) bool) ([]core.Message, *core.State, error)
}

type gitBackend struct{ *gitroom.Room }

func (g gitBackend) Load(context.Context) (core.Room, []core.Message, *core.State, error) {
	return g.Room.Load()
}

func openBackend(ctx context.Context, root string, id *local.Identity, actor core.Actor) (backend, error) {
	switch id.Backend {
	case "git":
		r, err := gitroom.Open(ctx, gitroom.Options{Root: root, Slug: id.Slug, Remote: id.Remote})
		if err != nil {
			return nil, err
		}
		return gitBackend{r}, nil
	case "server":
		tok := id.HumanToken
		if actor == core.Agent {
			tok = id.AgentToken
		}
		return &serverroom.Room{Server: id.Server, ID: id.RoomID, Token: tok}, nil
	}
	return nil, fmt.Errorf("unknown backend %q in .coop/%s.json", id.Backend, id.Side)
}

// agreementPath is where the agreement of a closed room is kept.
func agreementPath(root string, id *local.Identity) string {
	if id.Backend == "git" {
		return filepath.Join(root, "coop", id.Slug, "contract", "agreement.md")
	}
	return filepath.Join(root, local.Dir, "agreement-"+id.Slug+".md")
}

// saveAgreement writes the agreement once the room has closed with one.
// The git backend writes it into the repository itself.
func (s *session) saveAgreement(st *core.State) error {
	if st == nil || !st.Closed || st.CloseReason != "approved" {
		return nil
	}
	p := agreementPath(s.root, s.id)
	if _, err := os.Stat(p); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(p, gitroom.Agreement(st), 0o600)
}

func (s *session) load(ctx context.Context) (core.Room, []core.Message, *core.State, error) {
	r, log, st, err := s.room.Load(ctx)
	if err == nil {
		err = s.saveAgreement(st)
	}
	return r, log, st, err
}

func openOnServer(ctx context.Context, env *Env, root, server, key, slug string, room core.Room) error {
	if key == "" {
		key = os.Getenv("COOP_HOST_KEY")
	}
	if key == "" {
		return errors.New("opening a room on a server needs a host key: --key or $COOP_HOST_KEY (the server admin issues one with `coop key add`)")
	}
	if id, err := local.Load(root, room.Sides[0]); err == nil {
		return fmt.Errorf("side %s here is already in room %q (remove .coop/%s.json to leave it)", id.Side, id.Slug, id.Side)
	}
	o := newOut(env, core.Human)
	var resp *api.CreateResponse
	err := o.spin("opening the room on "+server+"…", func() error {
		var err error
		resp, err = serverroom.Create(ctx, server, key, room.Topic, room.TurnBudget)
		return err
	})
	if err != nil {
		return err
	}
	if err := local.EnsureIgnored(root); err != nil {
		return err
	}
	id := &local.Identity{Backend: "server", Slug: slug, Side: resp.Credentials.Side, Server: server, RoomID: resp.RoomID,
		AgentToken: resp.Credentials.AgentToken, HumanToken: resp.Credentials.HumanToken}
	if err := id.Save(root); err != nil {
		return err
	}
	if o.on {
		o.println(o.u.OK(fmt.Sprintf("opened %s on %s as side %s", o.u.Bold(room.Topic), o.u.Dim(server), o.u.Side(id.Side))))
		o.println(o.u.Invitation(resp.Invitation))
		o.println(o.u.Hint("send it to the other side; then `coop watch` to follow the room"))
		return nil
	}
	fmt.Fprintf(env.Stdout, "opened room %q on %s as side %s\n", room.Topic, server, id.Side)
	fmt.Fprintf(env.Stdout, "send this single-use invitation to the other side (it is a secret):\n\n  %s\n\n", resp.Invitation)
	fmt.Fprintf(env.Stdout, "they run: coop join '%s'\n", resp.Invitation)
	return nil
}

func joinOnServer(ctx context.Context, env *Env, root, invitation string) error {
	// The server always gives the invited side B. Check before the
	// single-use invitation is spent.
	if id, err := local.Load(root, "B"); err == nil {
		return fmt.Errorf("side B here is already in room %q (remove .coop/B.json to leave it)", id.Slug)
	}
	o := newOut(env, core.Human)
	var server string
	var resp *api.JoinResponse
	err := o.spin("joining…", func() error {
		var err error
		server, resp, err = serverroom.Join(ctx, invitation)
		return err
	})
	if err != nil {
		return err
	}
	side := resp.Credentials.Side
	if err := local.EnsureIgnored(root); err != nil {
		return err
	}
	id := &local.Identity{Backend: "server", Slug: gitroom.Slugify(resp.Room.Topic), Side: side, Server: server, RoomID: resp.RoomID,
		AgentToken: resp.Credentials.AgentToken, HumanToken: resp.Credentials.HumanToken}
	if err := id.Save(root); err != nil {
		return err
	}
	_, _, st, err := (&serverroom.Room{Server: server, ID: resp.RoomID, Token: id.HumanToken}).Load(ctx)
	if err != nil {
		return err
	}
	o.say(fmt.Sprintf("joined room %q on %s as side %s", resp.Room.Topic, server, side),
		o.u.OK("joined as side "+o.u.Side(side)+o.u.Dim(" on "+server)))
	o.header(st, id, core.Human)
	if o.on {
		o.println(o.u.Hint("connect your agent here with `coop setup <agent>`, follow with `coop watch`"))
	}
	return nil
}
