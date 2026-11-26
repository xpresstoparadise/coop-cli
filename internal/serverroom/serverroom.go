// Package serverroom is the client side of the server backend.
package serverroom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xpresstoparadise/coop-cli/internal/api"
	"github.com/xpresstoparadise/coop-cli/internal/core"
)

var httpClient = &http.Client{Timeout: (api.MaxWait + 20) * time.Second}

func do(ctx context.Context, method, u, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e api.ErrorResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return &core.Error{Code: e.Error, Msg: e.Message}
		}
		return fmt.Errorf("%s %s: %s", method, u, resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: bad response: %w", method, u, err)
		}
	}
	return nil
}

// Create opens a room on server with a host key.
func Create(ctx context.Context, server, hostKey, topic string, budget int) (*api.CreateResponse, error) {
	var out api.CreateResponse
	err := do(ctx, "POST", strings.TrimRight(server, "/")+"/v1/rooms", hostKey, api.CreateRequest{Topic: topic, TurnBudget: budget}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ParseInvitation splits <base>/r/<room>#<code>.
func ParseInvitation(inv string) (server, room, code string, err error) {
	u, err := url.Parse(strings.TrimSpace(inv))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Fragment == "" {
		return "", "", "", errors.New("an invitation looks like https://host/r/<room>#<code> (copy the whole link)")
	}
	base, room, ok := strings.Cut(u.Path, "/r/")
	if !ok || room == "" || strings.Contains(room, "/") {
		return "", "", "", errors.New("an invitation looks like https://host/r/<room>#<code>")
	}
	return u.Scheme + "://" + u.Host + base, room, u.Fragment, nil
}

// IsInvitation reports whether s looks like an invitation link.
func IsInvitation(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// Join claims an invitation.
func Join(ctx context.Context, invitation string) (server string, resp *api.JoinResponse, err error) {
	server, room, code, err := ParseInvitation(invitation)
	if err != nil {
		return "", nil, err
	}
	var out api.JoinResponse
	if err := do(ctx, "POST", server+"/v1/rooms/"+url.PathEscape(room)+"/join", "", api.JoinRequest{Code: code}, &out); err != nil {
		return "", nil, err
	}
	return server, &out, nil
}

// Room is a room on a server, seen through one token.
type Room struct {
	Server, ID, Token string
}

func (r *Room) url(path string) string {
	return strings.TrimRight(r.Server, "/") + "/v1/rooms/" + url.PathEscape(r.ID) + path
}

// Load fetches the log and replays it with the same rules as the server.
func (r *Room) Load(ctx context.Context) (core.Room, []core.Message, *core.State, error) {
	var out api.RoomResponse
	if err := do(ctx, "GET", r.url(""), r.Token, nil, &out); err != nil {
		return core.Room{}, nil, nil, err
	}
	st, err := core.Replay(out.Room, out.Log)
	if err != nil {
		return out.Room, nil, nil, fmt.Errorf("room log is inconsistent: %w", err)
	}
	return out.Room, out.Log, st, nil
}

// Post sends m; the server takes side and actor from the token.
func (r *Room) Post(ctx context.Context, m core.Message, seen int) (core.Message, *core.State, error) {
	var out api.PostResponse
	req := api.PostRequest{Kind: m.Kind, Re: m.Re, Resolution: m.Resolution, Body: m.Body, Seen: seen}
	if err := do(ctx, "POST", r.url("/messages"), r.Token, req, &out); err != nil {
		return core.Message{}, nil, err
	}
	_, _, st, err := r.Load(ctx)
	if err != nil {
		return core.Message{}, nil, err
	}
	return out.Message, st, nil
}

// Wait long-polls until messages after seq after arrive from someone other
// than the caller, the room closes, or ctx ends. mine is applied again on
// the client so the result matches the git backend.
func (r *Room) Wait(ctx context.Context, after int, mine func(core.Message) bool) ([]core.Message, *core.State, error) {
	for {
		chunk := api.MaxWait * time.Second
		if dl, ok := ctx.Deadline(); ok {
			if left := time.Until(dl); left < chunk {
				chunk = left
			}
		}
		if chunk <= 0 {
			break
		}
		var out api.WaitResponse
		q := "/wait?after=" + strconv.Itoa(after) + "&timeout=" + chunk.Round(time.Millisecond).String()
		err := do(ctx, "GET", r.url(q), r.Token, nil, &out)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		var fresh []core.Message
		for _, m := range out.Fresh {
			if !mine(m) {
				fresh = append(fresh, m)
			}
		}
		_, _, st, err := r.Load(ctx)
		if err != nil {
			return nil, nil, err
		}
		if len(fresh) > 0 || st.Closed {
			return fresh, st, nil
		}
	}
	// ctx ended: report the current state with a fresh context.
	lctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, st, err := r.Load(lctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, st, nil
}
