// Package gitroom keeps a room as files in coop/<slug>/ of a project folder,
// optionally synchronised through git.
//
// Layout:
//
//	coop/<slug>/room.json
//	coop/<slug>/thread/NNNN-<side>-<actor>-<kind>.md
//	coop/<slug>/contract/agreement.md
package gitroom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xpresstoparadise/coop-cli/internal/core"
)

// Options say where a room lives and how it is synchronised.
type Options struct {
	Root   string // project folder
	Slug   string // room folder name under coop/
	Remote bool   // commit, push and pull through git

	// Poll is how often Wait looks at the folder; Sync how often it pulls
	// when Remote is set. Zero means 500ms and 15s.
	Poll, Sync time.Duration
}

// Room is a room stored in a folder.
type Room struct {
	opt Options
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Slugify turns a topic into a folder name.
func Slugify(topic string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(topic) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	if s == "" {
		s = "room"
	}
	return s
}

func newRoom(opt Options) (*Room, error) {
	if !slugRe.MatchString(opt.Slug) {
		return nil, fmt.Errorf("invalid room name %q: use lowercase letters, digits and dashes", opt.Slug)
	}
	if opt.Poll == 0 {
		opt.Poll = 500 * time.Millisecond
	}
	if opt.Sync == 0 {
		opt.Sync = 15 * time.Second
	}
	return &Room{opt: opt}, nil
}

// Dir is the room folder.
func (r *Room) Dir() string       { return filepath.Join(r.opt.Root, "coop", r.opt.Slug) }
func (r *Room) threadDir() string { return filepath.Join(r.Dir(), "thread") }
func (r *Room) roomFile() string  { return filepath.Join(r.Dir(), "room.json") }
func (r *Room) contractFile() string {
	return filepath.Join(r.Dir(), "contract", "agreement.md")
}

// Create makes a new room folder. With Remote it commits and pushes it.
func Create(ctx context.Context, opt Options, room core.Room) (*Room, error) {
	r, err := newRoom(opt)
	if err != nil {
		return nil, err
	}
	if opt.Remote {
		if err := r.pull(ctx); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(r.Dir()); err == nil {
		return nil, fmt.Errorf("room %q already exists in %s", opt.Slug, r.Dir())
	}
	if err := os.MkdirAll(r.threadDir(), 0o755); err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(room, "", "  ")
	files := map[string][]byte{
		r.roomFile():                         append(data, '\n'),
		filepath.Join(r.Dir(), ".gitignore"): []byte(".lock\n"),
		// Keep LF everywhere, whatever core.autocrlf says on Windows.
		filepath.Join(r.Dir(), ".gitattributes"): []byte("* text eol=lf\n"),
		filepath.Join(r.threadDir(), ".keep"):    nil,
	}
	var paths []string
	for p, b := range files {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	if opt.Remote {
		if err := r.commitPush(ctx, paths, "coop("+opt.Slug+"): open room"); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Open returns an existing room. With Remote it pulls first.
func Open(ctx context.Context, opt Options) (*Room, error) {
	r, err := newRoom(opt)
	if err != nil {
		return nil, err
	}
	if opt.Remote {
		if err := r.pull(ctx); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(r.roomFile()); err != nil {
		return nil, fmt.Errorf("no room %q in %s", opt.Slug, filepath.Join(opt.Root, "coop"))
	}
	return r, nil
}

// Load reads the room and replays its log.
func (r *Room) Load() (core.Room, []core.Message, *core.State, error) {
	var room core.Room
	data, err := os.ReadFile(r.roomFile())
	if err != nil {
		return room, nil, nil, err
	}
	if err := json.Unmarshal(data, &room); err != nil {
		return room, nil, nil, fmt.Errorf("%s: %w", r.roomFile(), err)
	}
	log, err := r.readThread()
	if err != nil {
		return room, nil, nil, err
	}
	st, err := core.Replay(room, log)
	if err != nil {
		return room, nil, nil, fmt.Errorf("room log is inconsistent: %w", err)
	}
	return room, log, st, nil
}

// Post checks m against the room's rules and writes it. seen is the last
// seq the sender has read. With Remote the message is committed and pushed;
// if the push races with the other side, the message is renumbered and
// checked again.
func (r *Room) Post(ctx context.Context, m core.Message, seen int) (core.Message, *core.State, error) {
	unlock, err := r.lock()
	if err != nil {
		return core.Message{}, nil, err
	}
	defer unlock()

	if r.opt.Remote {
		if err := r.pull(ctx); err != nil {
			return core.Message{}, nil, err
		}
	}
	m.At = time.Now().UTC().Truncate(time.Second)
	for attempt := 0; ; attempt++ {
		_, _, st, err := r.Load()
		if err != nil {
			return core.Message{}, nil, err
		}
		out, err := st.Post(m, seen)
		if err != nil {
			return core.Message{}, nil, err
		}
		paths, err := r.write(out, st)
		if err != nil {
			return core.Message{}, nil, err
		}
		if !r.opt.Remote {
			return out, st, nil
		}
		msg := fmt.Sprintf("coop(%s): %04d %s %s %s", r.opt.Slug, out.Seq, out.Side, out.Actor, out.Kind)
		err = r.commitPush(ctx, paths, msg)
		if err == nil {
			return out, st, nil
		}
		if !errors.Is(err, errPushRejected) || attempt >= 4 {
			return core.Message{}, nil, err
		}
		// Someone pushed first: drop our commit, take theirs, try again.
		if err := r.undoLastCommit(ctx, paths); err != nil {
			return core.Message{}, nil, err
		}
		if err := r.pull(ctx); err != nil {
			return core.Message{}, nil, err
		}
	}
}

// Wait returns the messages after seq after that mine does not match, as
// soon as there is at least one, or none when ctx ends.
func (r *Room) Wait(ctx context.Context, after int, mine func(core.Message) bool) ([]core.Message, *core.State, error) {
	var lastSync time.Time
	for {
		if r.opt.Remote && time.Since(lastSync) >= r.opt.Sync {
			if err := r.pullLocked(ctx); err != nil && ctx.Err() == nil {
				return nil, nil, err
			}
			lastSync = time.Now()
		}
		_, log, st, err := r.Load()
		if err != nil {
			return nil, nil, err
		}
		var fresh []core.Message
		for _, m := range log {
			if m.Seq > after && !mine(m) {
				fresh = append(fresh, m)
			}
		}
		if len(fresh) > 0 || st.Closed {
			return fresh, st, nil
		}
		select {
		case <-ctx.Done():
			return nil, st, nil
		case <-time.After(r.opt.Poll):
		}
	}
}

// ---- files

var nameRe = regexp.MustCompile(`^(\d{4})-([A-Za-z0-9]+)-(agent|human)-([a-z]+)\.md$`)

func fileName(m core.Message) string {
	return fmt.Sprintf("%04d-%s-%s-%s.md", m.Seq, m.Side, m.Actor, m.Kind)
}

func encode(m core.Message) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	if m.Re != 0 {
		fmt.Fprintf(&b, "re: %d\n", m.Re)
	}
	if m.Resolution {
		b.WriteString("resolution: true\n")
	}
	fmt.Fprintf(&b, "at: %s\n", m.At.Format(time.RFC3339))
	b.WriteString("---\n")
	b.WriteString(m.Body)
	if !strings.HasSuffix(m.Body, "\n") {
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func decode(name string, data []byte) (core.Message, error) {
	g := nameRe.FindStringSubmatch(name)
	if g == nil {
		return core.Message{}, fmt.Errorf("%s: not a message file name", name)
	}
	seq, _ := strconv.Atoi(g[1])
	m := core.Message{Seq: seq, Side: g[2], Actor: core.Actor(g[3]), Kind: core.Kind(g[4])}
	// git on Windows may check files out with CRLF line endings.
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return m, fmt.Errorf("%s: missing front matter", name)
	}
	head, body, ok := strings.Cut(text[4:], "\n---\n")
	if !ok {
		return m, fmt.Errorf("%s: unterminated front matter", name)
	}
	sc := bufio.NewScanner(strings.NewReader(head))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "re":
			n, err := strconv.Atoi(v)
			if err != nil {
				return m, fmt.Errorf("%s: bad re %q", name, v)
			}
			m.Re = n
		case "resolution":
			m.Resolution = v == "true"
		case "at":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return m, fmt.Errorf("%s: bad at %q", name, v)
			}
			m.At = t
		}
	}
	m.Body = strings.TrimSuffix(body, "\n")
	return m, nil
}

func (r *Room) readThread() ([]core.Message, error) {
	entries, err := os.ReadDir(r.threadDir())
	if err != nil {
		return nil, err
	}
	var log []core.Message
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.threadDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		m, err := decode(e.Name(), data)
		if err != nil {
			return nil, err
		}
		log = append(log, m)
	}
	sort.SliceStable(log, func(i, j int) bool { return log[i].Seq < log[j].Seq })
	return log, nil
}

// write stores m and, when it closed the room with an agreement, the
// agreement. It returns the paths written.
func (r *Room) write(m core.Message, st *core.State) ([]string, error) {
	p := filepath.Join(r.threadDir(), fileName(m))
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(encode(m)); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	paths := []string{p}
	if st.Closed && st.CloseReason == "approved" {
		if err := os.MkdirAll(filepath.Dir(r.contractFile()), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(r.contractFile(), Agreement(st), 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, r.contractFile())
	}
	return paths, nil
}

// Agreement renders the approved resolution of a closed room.
func Agreement(st *core.State) []byte {
	return []byte(fmt.Sprintf("# Agreement: %s\n\nResolution #%d by side %s, approved by both sides.\n\n%s\n",
		st.Room.Topic, st.Resolution, st.ResolutionSide, st.ResolutionBody()))
}

// ---- locking

func (r *Room) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(r.Dir(), ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}

// ---- git

var errPushRejected = errors.New("push rejected")

func (r *Room) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.opt.Root
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

func (r *Room) hasUpstream(ctx context.Context) bool {
	_, err := r.git(ctx, "rev-parse", "--abbrev-ref", "@{u}")
	return err == nil
}

func (r *Room) pull(ctx context.Context) error {
	if !r.hasUpstream(ctx) {
		return errors.New("remote mode needs a current branch with an upstream (git push -u origin <branch>)")
	}
	_, err := r.git(ctx, "pull", "--rebase", "--autostash", "--quiet")
	return err
}

func (r *Room) pullLocked(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return r.pull(ctx)
}

func (r *Room) commitPush(ctx context.Context, paths []string, msg string) error {
	args := append([]string{"add", "--"}, paths...)
	if _, err := r.git(ctx, args...); err != nil {
		return err
	}
	args = append([]string{"commit", "--quiet", "-m", msg, "--"}, paths...)
	if _, err := r.git(ctx, args...); err != nil {
		return err
	}
	if _, err := r.git(ctx, "push", "--quiet"); err != nil {
		if strings.Contains(err.Error(), "rejected") || strings.Contains(err.Error(), "fetch first") {
			return fmt.Errorf("%w: %v", errPushRejected, err)
		}
		return err
	}
	return nil
}

func (r *Room) undoLastCommit(ctx context.Context, paths []string) error {
	if _, err := r.git(ctx, "reset", "--soft", "HEAD~1"); err != nil {
		return err
	}
	args := append([]string{"rm", "--cached", "--quiet", "-f", "--"}, paths...)
	if _, err := r.git(ctx, args...); err != nil {
		return err
	}
	for _, p := range paths {
		os.Remove(p)
	}
	return nil
}
