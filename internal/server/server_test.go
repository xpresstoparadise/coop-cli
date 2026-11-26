package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xpresstoparadise/coop-cli/internal/api"
)

type fixture struct {
	t   *testing.T
	srv *Server
	hs  *httptest.Server
	key string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	srv, err := Open(filepath.Join(t.TempDir(), "coop.db"), "https://coop.example.org")
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); srv.Close() })
	_, key, err := srv.AddKey("t")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, srv: srv, hs: hs, key: key}
}

func (f *fixture) req(method, path, token string, body any, out any) int {
	f.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	r, _ := http.NewRequest(method, f.hs.URL+path, rd)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (f *fixture) room() (api.CreateResponse, api.JoinResponse) {
	f.t.Helper()
	var c api.CreateResponse
	if code := f.req("POST", "/v1/rooms", f.key, api.CreateRequest{Topic: "t"}, &c); code != 201 {
		f.t.Fatalf("create: %d", code)
	}
	if !strings.HasPrefix(c.Invitation, "https://coop.example.org/r/"+c.RoomID+"#") {
		f.t.Fatalf("invitation %q", c.Invitation)
	}
	code := c.Invitation[strings.Index(c.Invitation, "#")+1:]
	var j api.JoinResponse
	if st := f.req("POST", "/v1/rooms/"+c.RoomID+"/join", "", api.JoinRequest{Code: code}, &j); st != 200 {
		f.t.Fatalf("join: %d", st)
	}
	if st := f.req("POST", "/v1/rooms/"+c.RoomID+"/join", "", api.JoinRequest{Code: code}, nil); st != 404 {
		f.t.Fatalf("second join: %d, want 404", st)
	}
	return c, j
}

func TestAuthAndRoles(t *testing.T) {
	f := setup(t)
	if st := f.req("POST", "/v1/rooms", "", api.CreateRequest{Topic: "t"}, nil); st != 401 {
		t.Fatalf("create without key: %d", st)
	}
	c, j := f.room()
	c2, _ := f.room()
	id := c.RoomID

	// A token works only for its own room.
	if st := f.req("GET", "/v1/rooms/"+c2.RoomID, c.Credentials.AgentToken, nil, nil); st != 401 {
		t.Fatalf("token of another room: %d", st)
	}
	var rr api.RoomResponse
	f.req("GET", "/v1/rooms/"+id, j.Credentials.AgentToken, nil, &rr)
	if rr.You.Side != "B" || rr.You.Actor != "agent" {
		t.Fatalf("you = %+v", rr.You)
	}

	// Side and actor come from the token: an agent cannot close.
	var e api.ErrorResponse
	if st := f.req("POST", "/v1/rooms/"+id+"/messages", c.Credentials.AgentToken, api.PostRequest{Kind: "close", Body: "x"}, &e); st != 403 || e.Error != "humans_only" {
		t.Fatalf("agent close: %d %+v", st, e)
	}
	if st := f.req("POST", "/v1/rooms/"+id+"/messages", j.Credentials.AgentToken, api.PostRequest{Kind: "propose", Body: "x"}, &e); st != 409 || e.Error != "not_your_turn" {
		t.Fatalf("B out of turn: %d %+v", st, e)
	}
	var p api.PostResponse
	if st := f.req("POST", "/v1/rooms/"+id+"/messages", c.Credentials.AgentToken, api.PostRequest{Kind: "propose", Body: "x"}, &p); st != 201 || p.Message.Side != "A" || p.Message.Seq != 1 {
		t.Fatalf("A propose: %d %+v", st, p)
	}
	big := api.PostRequest{Kind: "note", Body: strings.Repeat("x", 70<<10)}
	if st := f.req("POST", "/v1/rooms/"+id+"/messages", c.Credentials.HumanToken, big, &e); st != 413 {
		t.Fatalf("big body: %d", st)
	}
}

func TestWaitWakes(t *testing.T) {
	f := setup(t)
	c, j := f.room()
	id := c.RoomID
	done := make(chan api.WaitResponse)
	go func() {
		var w api.WaitResponse
		f.req("GET", "/v1/rooms/"+id+"/wait?after=0&timeout=5s", j.Credentials.AgentToken, nil, &w)
		done <- w
	}()
	time.Sleep(100 * time.Millisecond)
	// B's own human message does wake B's agent; A's agent post too.
	f.req("POST", "/v1/rooms/"+id+"/messages", c.Credentials.AgentToken, api.PostRequest{Kind: "propose", Body: "hi"}, nil)
	select {
	case w := <-done:
		if len(w.Fresh) != 1 || w.Fresh[0].Body != "hi" {
			t.Fatalf("wait got %+v", w)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not wake")
	}
	// Own messages do not wake the sender.
	start := time.Now()
	var w api.WaitResponse
	f.req("GET", "/v1/rooms/"+id+"/wait?after=0&timeout=300ms", c.Credentials.AgentToken, nil, &w)
	if len(w.Fresh) != 0 || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("A woke on its own message: %+v", w)
	}
}

func TestKeysAndPurge(t *testing.T) {
	f := setup(t)
	keys, _ := f.srv.Keys()
	if err := f.srv.RevokeKey(keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if st := f.req("POST", "/v1/rooms", f.key, api.CreateRequest{Topic: "t"}, nil); st != 401 {
		t.Fatalf("revoked key: %d", st)
	}
	_, key, _ := f.srv.AddKey("t2")
	f.key = key
	c, _ := f.room()

	if n, _ := f.srv.Purge(); n != 0 {
		t.Fatalf("fresh room purged")
	}
	f.srv.now = func() time.Time { return time.Now().UTC().Add(OpenRetention + time.Hour) }
	if n, _ := f.srv.Purge(); n != 1 {
		t.Fatalf("stale room not purged: %d", n)
	}
	if st := f.req("GET", "/v1/rooms/"+c.RoomID, c.Credentials.HumanToken, nil, nil); st != 401 {
		t.Fatalf("tokens of a purged room must stop working: %d", st)
	}
}
