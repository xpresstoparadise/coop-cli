// Package server is the coop room server: rooms, credentials and message
// logs in SQLite, the core rules enforced on every post, long-poll waits.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xpresstoparadise/coop-cli/internal/api"
	"github.com/xpresstoparadise/coop-cli/internal/core"
)

//go:embed schema.sql
var schema string

// Retention.
const (
	OpenRetention   = 7 * 24 * time.Hour  // open room without activity
	ClosedRetention = 30 * 24 * time.Hour // closed room
)

// Server serves the API. Create it with Open.
type Server struct {
	db      *sql.DB
	baseURL string
	now     func() time.Time

	mu   sync.Mutex
	hubs map[string]*hub
}

// hub serialises posts to one room and wakes its waiters.
type hub struct {
	post   sync.Mutex
	mu     sync.Mutex
	notify chan struct{} // closed on every new message
}

// Open opens (and creates) the database. baseURL is the public address
// used in invitations, for example https://coop.example.org.
func Open(path, baseURL string) (*Server, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Server{db: db, baseURL: strings.TrimRight(baseURL, "/"), now: func() time.Time { return time.Now().UTC() }, hubs: map[string]*hub{}}, nil
}

// Close closes the database.
func (s *Server) Close() error { return s.db.Close() }

func (s *Server) hub(room string) *hub {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hubs[room]
	if h == nil {
		h = &hub{notify: make(chan struct{})}
		s.hubs[room] = h
	}
	return h
}

func (h *hub) signal() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notify
}

func (h *hub) wake() {
	h.mu.Lock()
	close(h.notify)
	h.notify = make(chan struct{})
	h.mu.Unlock()
}

// ---- secrets

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func secret(prefix string) string { return prefix + base64.RawURLEncoding.EncodeToString(random(24)) }

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func roomID() string {
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random(10)))
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// ---- host keys

// HostKey is a credential that may create rooms.
type HostKey struct {
	ID        int64
	Name      string
	CreatedAt string
	RevokedAt string
}

// AddKey issues a host key and returns it; only its hash is stored.
func (s *Server) AddKey(name string) (int64, string, error) {
	key := secret(api.HostKeyPrefix)
	res, err := s.db.Exec(`INSERT INTO host_keys(name, hash, created_at) VALUES(?,?,?)`, name, hash(key), ts(s.now()))
	if err != nil {
		return 0, "", err
	}
	id, _ := res.LastInsertId()
	return id, key, nil
}

// Keys lists host keys.
func (s *Server) Keys() ([]HostKey, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at, COALESCE(revoked_at,'') FROM host_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []HostKey
	for rows.Next() {
		var k HostKey
		if err := rows.Scan(&k.ID, &k.Name, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// RevokeKey revokes a host key; rooms it created stay.
func (s *Server) RevokeKey(id int64) error {
	res, err := s.db.Exec(`UPDATE host_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, ts(s.now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no active host key %d", id)
	}
	return nil
}

// ---- retention

// Purge deletes rooms past their retention and returns how many.
func (s *Server) Purge() (int64, error) {
	now := s.now()
	res, err := s.db.Exec(`DELETE FROM rooms WHERE (closed_at IS NOT NULL AND closed_at < ?) OR (closed_at IS NULL AND active_at < ?)`,
		ts(now.Add(-ClosedRetention)), ts(now.Add(-OpenRetention)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunPurge purges every interval until ctx ends.
func (s *Server) RunPurge(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.Purge(); err != nil {
			log.Printf("purge: %v", err)
		} else if n > 0 {
			log.Printf("purge: deleted %d rooms", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- rooms

func (s *Server) loadRoom(ctx context.Context, id string) (core.Room, []core.Message, error) {
	var r core.Room
	err := s.db.QueryRowContext(ctx, `SELECT topic, turn_budget FROM rooms WHERE id=?`, id).Scan(&r.Topic, &r.TurnBudget)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil, errNotFound
	}
	if err != nil {
		return r, nil, err
	}
	r.Sides = [2]string{"A", "B"}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, side, actor, kind, re, resolution, body, at FROM messages WHERE room_id=? ORDER BY seq`, id)
	if err != nil {
		return r, nil, err
	}
	defer rows.Close()
	var msgs []core.Message
	for rows.Next() {
		var m core.Message
		var at string
		if err := rows.Scan(&m.Seq, &m.Side, &m.Actor, &m.Kind, &m.Re, &m.Resolution, &m.Body, &at); err != nil {
			return r, nil, err
		}
		m.At, _ = time.Parse(time.RFC3339Nano, at)
		msgs = append(msgs, m)
	}
	return r, msgs, rows.Err()
}

func (s *Server) insertCredentials(tx *sql.Tx, room, side string) (api.Credentials, error) {
	c := api.Credentials{Side: side, AgentToken: secret(api.AgentTokenPrefix), HumanToken: secret(api.HumanTokenPrefix)}
	for tok, actor := range map[string]core.Actor{c.AgentToken: core.Agent, c.HumanToken: core.Human} {
		if _, err := tx.Exec(`INSERT INTO tokens(hash, room_id, side, actor) VALUES(?,?,?,?)`, hash(tok), room, side, actor); err != nil {
			return c, err
		}
	}
	return c, nil
}

// ---- HTTP

var (
	errNotFound     = &core.Error{Code: "not_found", Msg: "no such room"}
	errUnauthorized = &core.Error{Code: "unauthorized", Msg: "missing or invalid token"}
	errInvitation   = &core.Error{Code: "invalid_invitation", Msg: "invitation unknown or already used"}
)

func status(code string) int {
	switch code {
	case "bad_request":
		return http.StatusBadRequest
	case "unauthorized":
		return http.StatusUnauthorized
	case "humans_only":
		return http.StatusForbidden
	case "not_found", "invalid_invitation":
		return http.StatusNotFound
	case "too_large":
		return http.StatusRequestEntityTooLarge
	case "paused":
		return http.StatusLocked
	}
	return http.StatusConflict
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	var ce *core.Error
	if errors.As(err, &ce) {
		writeJSON(w, status(ce.Code), api.ErrorResponse{Error: ce.Code, Message: ce.Msg})
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return // the client went away; nobody reads the answer
	}
	log.Printf("internal error: %v", err)
	writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "internal", Message: "internal error"})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return core.ErrTooLarge
		}
		return &core.Error{Code: "bad_request", Msg: "invalid JSON: " + err.Error()}
	}
	return nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// auth resolves the bearer token for room id.
func (s *Server) auth(r *http.Request, id string) (api.You, error) {
	tok := bearer(r)
	if tok == "" {
		return api.You{}, errUnauthorized
	}
	var you api.You
	var room string
	err := s.db.QueryRowContext(r.Context(), `SELECT room_id, side, actor FROM tokens WHERE hash=?`, hash(tok)).Scan(&room, &you.Side, &you.Actor)
	if errors.Is(err, sql.ErrNoRows) || err == nil && room != id {
		return api.You{}, errUnauthorized
	}
	return you, err
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /r/{id}", s.invitationPage)
	mux.HandleFunc("POST /v1/rooms", s.create)
	mux.HandleFunc("POST /v1/rooms/{id}/join", s.join)
	mux.HandleFunc("GET /v1/rooms/{id}", s.get)
	mux.HandleFunc("POST /v1/rooms/{id}/messages", s.post)
	mux.HandleFunc("GET /v1/rooms/{id}/wait", s.wait)
	return mux
}

func (s *Server) invitationPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "This is an invitation to a coop room.\nCopy the whole link (including the part after #) and run:\n\n  coop join '<link>'\n")
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	key := bearer(r)
	var keyID int64
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM host_keys WHERE hash=? AND revoked_at IS NULL`, hash(key)).Scan(&keyID)
	if key == "" || errors.Is(err, sql.ErrNoRows) {
		fail(w, &core.Error{Code: "unauthorized", Msg: "creating rooms needs a valid host key"})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	var req api.CreateRequest
	if err := decode(w, r, &req); err != nil {
		fail(w, err)
		return
	}
	req.Topic = strings.TrimSpace(req.Topic)
	if req.Topic == "" || len(req.Topic) > 200 {
		fail(w, &core.Error{Code: "bad_request", Msg: "topic must be 1-200 bytes"})
		return
	}
	if req.TurnBudget < 0 || req.TurnBudget > 1000 {
		fail(w, &core.Error{Code: "bad_request", Msg: "turn_budget must be 0-1000"})
		return
	}
	room := core.NewRoom(req.Topic, req.TurnBudget)
	id, code := roomID(), base64.RawURLEncoding.EncodeToString(random(16))
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback()
	now := ts(s.now())
	if _, err := tx.Exec(`INSERT INTO rooms(id, topic, turn_budget, host_key, invite_hash, created_at, active_at) VALUES(?,?,?,?,?,?,?)`,
		id, room.Topic, room.TurnBudget, keyID, hash(code), now, now); err != nil {
		fail(w, err)
		return
	}
	creds, err := s.insertCredentials(tx, id, room.Sides[0])
	if err != nil {
		fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, api.CreateResponse{
		RoomID: id, Room: room, Credentials: creds,
		Invitation: s.baseURL + "/r/" + id + "#" + code,
	})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req api.JoinRequest
	if err := decode(w, r, &req); err != nil {
		fail(w, err)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE rooms SET invite_hash=NULL, active_at=? WHERE id=? AND invite_hash=?`, ts(s.now()), id, hash(req.Code))
	if err != nil {
		fail(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 || req.Code == "" {
		fail(w, errInvitation)
		return
	}
	creds, err := s.insertCredentials(tx, id, "B")
	if err != nil {
		fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, err)
		return
	}
	room, _, err := s.loadRoom(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.JoinResponse{RoomID: id, Room: room, Credentials: creds})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	you, err := s.auth(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	room, msgs, err := s.loadRoom(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if msgs == nil {
		msgs = []core.Message{}
	}
	writeJSON(w, http.StatusOK, api.RoomResponse{RoomID: id, Room: room, Log: msgs, You: you})
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	you, err := s.auth(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	var req api.PostRequest
	if err := decode(w, r, &req); err != nil {
		fail(w, err)
		return
	}
	h := s.hub(id)
	h.post.Lock()
	defer h.post.Unlock()

	room, msgs, err := s.loadRoom(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	st, err := core.Replay(room, msgs)
	if err != nil {
		fail(w, err)
		return
	}
	m := core.Message{Side: you.Side, Actor: you.Actor, Kind: req.Kind, Re: req.Re, Resolution: req.Resolution, Body: req.Body, At: s.now().Truncate(time.Second)}
	out, err := st.Post(m, req.Seen)
	if err != nil {
		fail(w, err)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO messages(room_id, seq, side, actor, kind, re, resolution, body, at) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, out.Seq, out.Side, out.Actor, out.Kind, out.Re, out.Resolution, out.Body, ts(out.At)); err != nil {
		fail(w, err)
		return
	}
	now := ts(s.now())
	if st.Closed {
		_, err = tx.Exec(`UPDATE rooms SET active_at=?, closed_at=? WHERE id=?`, now, now, id)
	} else {
		_, err = tx.Exec(`UPDATE rooms SET active_at=? WHERE id=?`, now, id)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, err)
		return
	}
	h.wake()
	writeJSON(w, http.StatusCreated, api.PostResponse{Message: out})
}

func (s *Server) wait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	you, err := s.auth(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	timeout := api.MaxWait * time.Second
	if d, err := time.ParseDuration(r.URL.Query().Get("timeout")); err == nil && d > 0 && d < timeout {
		timeout = d
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	h := s.hub(id)
	for {
		signal := h.signal() // before reading, so no message is missed
		room, msgs, err := s.loadRoom(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			fail(w, err)
			return
		}
		fresh := []core.Message{}
		for _, m := range msgs {
			if m.Seq > after && !(m.Side == you.Side && m.Actor == you.Actor) {
				fresh = append(fresh, m)
			}
		}
		closed := false
		if st, err := core.Replay(room, msgs); err == nil {
			closed = st.Closed
		}
		if len(fresh) > 0 || closed {
			writeJSON(w, http.StatusOK, api.WaitResponse{Fresh: fresh})
			return
		}
		select {
		case <-signal:
			continue
		case <-ctx.Done():
		}
		break
	}
	writeJSON(w, http.StatusOK, api.WaitResponse{Fresh: []core.Message{}})
}
