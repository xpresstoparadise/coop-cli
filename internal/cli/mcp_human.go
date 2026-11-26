package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xpresstoparadise/coop-cli/internal/core"
)

// Human actions from the chat. The agent calls the tool; the coop server
// then asks the human directly through the client's own dialog (MCP
// elicitation) and acts only on their "yes". The model cannot answer that
// dialog, so an agent still cannot approve, resume, close or speak as its
// human on its own.

const humanNote = "Your human is asked to confirm in a dialog of the client; you cannot confirm it yourself. " +
	"Use it only when your human asks for it (for example /coop approve)."

func humanTools() []tool {
	return []tool{
		{
			Name:        "coop_approve",
			Description: "Ask your human to approve the agreed resolution. " + humanNote,
			InputSchema: obj(map[string]any{}),
			human:       humanApprove,
		},
		{
			Name:        "coop_resume",
			Description: "Ask your human to resume the paused agents. " + humanNote,
			InputSchema: obj(map[string]any{}),
			human:       humanResume,
		},
		{
			Name:        "coop_close",
			Description: "Ask your human to close the room. " + humanNote,
			InputSchema: obj(map[string]any{
				"reason": map[string]any{"type": "string"},
			}),
			human: humanClose,
		},
		{
			Name: "coop_say_human",
			Description: "Ask your human to send a message to the room in their own name (the other side sees it as the human, not as you). " +
				humanNote + " Pass the exact text your human wants to send.",
			InputSchema: obj(map[string]any{
				"text": map[string]any{"type": "string"},
			}, "text"),
			human: humanSay,
		},
	}
}

// ask shows message to the human with a single yes/no field and reports
// whether they said yes.
func (s *mcpServer) ask(ctx context.Context, message, field, title string) (bool, error) {
	s.mu.Lock()
	s.nextID++
	id := json.RawMessage(fmt.Sprintf(`"coop-ask-%d"`, s.nextID))
	ch := make(chan rpcMsg, 1)
	s.pending[string(id)] = ch
	s.mu.Unlock()
	params, _ := json.Marshal(map[string]any{
		"message": message,
		"requestedSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				field: map[string]any{"type": "boolean", "title": title, "default": false},
			},
			"required": []string{field},
		},
	})
	s.send(rpcMsg{ID: id, Method: "elicitation/create", Params: params})
	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, string(id))
		s.mu.Unlock()
		return false, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return false, errors.New(resp.Error.Message)
		}
		data, _ := json.Marshal(resp.Result)
		var r struct {
			Action  string         `json:"action"`
			Content map[string]any `json:"content"`
		}
		json.Unmarshal(data, &r)
		yes, _ := r.Content[field].(bool)
		return r.Action == "accept" && yes, nil
	}
}

// humanSession opens the room as this side's human, with captured output.
func (s *mcpServer) humanSession(ctx context.Context) (*session, *bytes.Buffer, error) {
	var out bytes.Buffer
	env := &Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Dir: s.env.Dir, IsTTY: func() bool { return false }}
	side, agent := s.side, false
	sess, err := openSession(ctx, env, sessionFlags{side: &side, agent: &agent})
	return sess, &out, err
}

func refusal(err error) (string, bool) {
	var ce *core.Error
	if errors.As(err, &ce) {
		return fmt.Sprintf("refused (%s): %s", ce.Code, ce.Msg), true
	}
	return err.Error(), true
}

// confirmAndPost asks the human and, on yes, posts m as the human.
func (s *mcpServer) confirmAndPost(ctx context.Context, sess *session, out *bytes.Buffer, question, title string, m core.Message) (string, bool) {
	yes, err := s.ask(ctx, question, "confirm", title)
	if err != nil {
		return "could not ask your human: " + err.Error(), true
	}
	if !yes {
		return "your human declined; nothing was sent", false
	}
	if err := sess.post(ctx, m); err != nil {
		return refusal(err)
	}
	return "your human confirmed. " + strings.TrimSpace(out.String()), false
}

func humanApprove(ctx context.Context, s *mcpServer, _ map[string]any) (string, bool) {
	sess, out, err := s.humanSession(ctx)
	if err != nil {
		return refusal(err)
	}
	_, _, st, err := sess.load(ctx)
	if err != nil {
		return refusal(err)
	}
	if !st.Agreed {
		return refusal(core.ErrNoResolution)
	}
	if st.Approved[sess.id.Side] {
		return refusal(core.ErrAlreadyApproved)
	}
	q := fmt.Sprintf("coop · %s\n\nApprove resolution #%d (by side %s) for your side %s?\n\n%s\n\nOnly you can answer this; your agent cannot.",
		st.Room.Topic, st.Resolution, st.ResolutionSide, sess.id.Side, st.ResolutionBody())
	return s.confirmAndPost(ctx, sess, out, q, "Approve this resolution",
		core.Message{Kind: core.Approve, Re: st.Resolution, Body: "approved"})
}

func humanResume(ctx context.Context, s *mcpServer, _ map[string]any) (string, bool) {
	sess, out, err := s.humanSession(ctx)
	if err != nil {
		return refusal(err)
	}
	_, _, st, err := sess.load(ctx)
	if err != nil {
		return refusal(err)
	}
	if st.Pause == nil {
		return refusal(core.ErrNotPaused)
	}
	why := "hand raised by side " + st.Pause.Side + " " + string(st.Pause.Actor)
	if st.Pause.Kind == core.PauseBudget {
		why = "side " + st.Pause.Side + " used its turn budget; resuming gives both sides a fresh budget"
	}
	q := fmt.Sprintf("coop · %s\n\nResume the agents? They are paused: %s.", st.Room.Topic, why)
	return s.confirmAndPost(ctx, sess, out, q, "Resume the agents",
		core.Message{Kind: core.Resume, Body: "resumed"})
}

func humanClose(ctx context.Context, s *mcpServer, a map[string]any) (string, bool) {
	sess, out, err := s.humanSession(ctx)
	if err != nil {
		return refusal(err)
	}
	reason := strings.TrimSpace(str(a["reason"]))
	if reason == "" {
		reason = "closed"
	}
	q := fmt.Sprintf("coop · %s\n\nClose the room for both sides? This cannot be undone.\n\nReason: %s",
		sess.id.Slug, reason)
	return s.confirmAndPost(ctx, sess, out, q, "Close the room",
		core.Message{Kind: core.Close, Body: reason})
}

func humanSay(ctx context.Context, s *mcpServer, a map[string]any) (string, bool) {
	text := strings.TrimSpace(str(a["text"]))
	if text == "" {
		return "no text to send", true
	}
	sess, out, err := s.humanSession(ctx)
	if err != nil {
		return refusal(err)
	}
	q := fmt.Sprintf("coop · %s\n\nSend this to the room in YOUR name (side %s, human)?\n\n%s",
		sess.id.Slug, sess.id.Side, text)
	return s.confirmAndPost(ctx, sess, out, q, "Send as me",
		core.Message{Kind: core.Note, Body: text})
}
