// Package api holds the HTTP API types shared by the coop server and its
// client. All endpoints are under /v1; bodies are JSON.
//
//	POST /v1/rooms                 host key   → CreateResponse
//	POST /v1/rooms/{id}/join       invitation → JoinResponse
//	GET  /v1/rooms/{id}            token      → RoomResponse
//	POST /v1/rooms/{id}/messages   token      → PostResponse
//	GET  /v1/rooms/{id}/wait       token      → WaitResponse  (?after=N&timeout=50s)
//
// Tokens are sent as "Authorization: Bearer ...". The side and actor of a
// message come from the token, never from the request body.
package api

import "github.com/xpresstoparadise/coop-cli/internal/core"

// Token prefixes.
const (
	HostKeyPrefix    = "coopk_"
	AgentTokenPrefix = "coopa_"
	HumanTokenPrefix = "cooph_"
)

// MaxWait is the longest a single wait request is held by the server; the
// client repeats it. It stays under common proxy read timeouts (60 s).
const MaxWait = 50

type CreateRequest struct {
	Topic      string `json:"topic"`
	TurnBudget int    `json:"turn_budget,omitempty"`
}

// Credentials are one side's tokens.
type Credentials struct {
	Side       string `json:"side"`
	AgentToken string `json:"agent_token"`
	HumanToken string `json:"human_token"`
}

type CreateResponse struct {
	RoomID      string      `json:"room_id"`
	Room        core.Room   `json:"room"`
	Credentials Credentials `json:"credentials"`
	Invitation  string      `json:"invitation"` // <base>/r/<id>#<code>
}

type JoinRequest struct {
	Code string `json:"code"`
}

type JoinResponse struct {
	RoomID      string      `json:"room_id"`
	Room        core.Room   `json:"room"`
	Credentials Credentials `json:"credentials"`
}

type You struct {
	Side  string     `json:"side"`
	Actor core.Actor `json:"actor"`
}

type RoomResponse struct {
	RoomID string         `json:"room_id"`
	Room   core.Room      `json:"room"`
	Log    []core.Message `json:"log"`
	You    You            `json:"you"`
}

type PostRequest struct {
	Kind       core.Kind `json:"kind"`
	Re         int       `json:"re,omitempty"`
	Resolution bool      `json:"resolution,omitempty"`
	Body       string    `json:"body"`
	Seen       int       `json:"seen"` // last seq the sender has read
}

type PostResponse struct {
	Message core.Message `json:"message"`
}

type WaitResponse struct {
	Fresh []core.Message `json:"fresh"` // messages after `after` not sent by the caller
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
