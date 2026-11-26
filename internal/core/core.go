// Package core holds the room model shared by every backend: messages, the
// state derived from a log of messages, and the rules that decide whether a
// message may be posted. It does no I/O.
package core

import (
	"fmt"
	"time"
)

// MaxBody is the largest message body, in bytes.
const MaxBody = 8192

// DefaultTurnBudget is the number of agent turns per side before the room
// pauses for the humans.
const DefaultTurnBudget = 20

// Actor is who sent a message within a side.
type Actor string

const (
	Agent Actor = "agent"
	Human Actor = "human"
)

// Kind is what a message does.
type Kind string

const (
	Propose Kind = "propose"
	Ask     Kind = "ask"
	Answer  Kind = "answer"
	Counter Kind = "counter"
	Accept  Kind = "accept"
	Reject  Kind = "reject"
	Note    Kind = "note"
	Hand    Kind = "hand"
	Resume  Kind = "resume"
	Approve Kind = "approve"
	Close   Kind = "close"
)

// Kinds lists every kind, in documentation order.
var Kinds = []Kind{Propose, Ask, Answer, Counter, Accept, Reject, Note, Hand, Resume, Approve, Close}

// Message is one entry of a room log. Messages are never edited.
type Message struct {
	Seq        int       `json:"seq"`
	Side       string    `json:"side"`
	Actor      Actor     `json:"actor"`
	Kind       Kind      `json:"kind"`
	Re         int       `json:"re,omitempty"`
	Resolution bool      `json:"resolution,omitempty"`
	Body       string    `json:"body"`
	At         time.Time `json:"at"`
}

// Room is the fixed description of a room, set when it is opened.
type Room struct {
	Topic      string    `json:"topic"`
	Sides      [2]string `json:"sides"`
	TurnBudget int       `json:"turn_budget"`
}

// NewRoom returns a two-sided room A/B. A budget of 0 means the default.
func NewRoom(topic string, budget int) Room {
	if budget <= 0 {
		budget = DefaultTurnBudget
	}
	return Room{Topic: topic, Sides: [2]string{"A", "B"}, TurnBudget: budget}
}

// Other returns the side opposite to side.
func (r Room) Other(side string) string {
	if side == r.Sides[0] {
		return r.Sides[1]
	}
	return r.Sides[0]
}

func (r Room) hasSide(side string) bool { return side == r.Sides[0] || side == r.Sides[1] }

// PauseKind says why the agents are paused.
type PauseKind string

const (
	PauseHand   PauseKind = "hand"
	PauseBudget PauseKind = "budget"
)

// Pause describes a pause of the agents.
type Pause struct {
	Kind  PauseKind `json:"kind"`
	Side  string    `json:"side"`
	Actor Actor     `json:"actor"`
	Seq   int       `json:"seq"`
}

// Phase is the coarse stage of a room.
type Phase string

const (
	Negotiation Phase = "negotiation"
	Approval    Phase = "approval"
	Closed      Phase = "closed"
)

// State is everything derived from a room's log.
type State struct {
	Room      Room           `json:"room"`
	LastSeq   int            `json:"last_seq"`
	Floor     string         `json:"floor"`
	TurnsUsed map[string]int `json:"turns_used"`
	Pause     *Pause         `json:"pause,omitempty"`

	// Resolution is the seq of the resolution under discussion, 0 if none.
	Resolution     int             `json:"resolution,omitempty"`
	ResolutionSide string          `json:"resolution_side,omitempty"`
	Agreed         bool            `json:"agreed,omitempty"`
	Approved       map[string]bool `json:"approved,omitempty"`

	Closed      bool   `json:"closed,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`

	// LastHumanSeq is the seq of the latest human message, 0 if none.
	LastHumanSeq int `json:"last_human_seq,omitempty"`

	resolutionBody string
}

// NewState returns the state of an empty room. Side A holds the floor.
func NewState(r Room) *State {
	return &State{Room: r, Floor: r.Sides[0], TurnsUsed: map[string]int{}, Approved: map[string]bool{}}
}

// Replay builds the state from a log, checking every message.
func Replay(r Room, log []Message) (*State, error) {
	s := NewState(r)
	for _, m := range log {
		if m.Seq != s.LastSeq+1 {
			return nil, fmt.Errorf("message %d: expected seq %d", m.Seq, s.LastSeq+1)
		}
		if _, err := s.Apply(m); err != nil {
			return nil, fmt.Errorf("message %d: %w", m.Seq, err)
		}
	}
	return s, nil
}

// Phase reports the coarse stage of the room.
func (s *State) Phase() Phase {
	switch {
	case s.Closed:
		return Closed
	case s.Agreed:
		return Approval
	default:
		return Negotiation
	}
}

// ResolutionBody is the text of the resolution under discussion.
func (s *State) ResolutionBody() string { return s.resolutionBody }

// Post checks m as a new message from a participant who has read the log up
// to seen, and applies it. Agents must have read every human message.
func (s *State) Post(m Message, seen int) (Message, error) {
	if m.Actor == Agent && s.LastHumanSeq > seen {
		return Message{}, ErrUnreadHuman
	}
	return s.Apply(m)
}

// Apply checks m against the rules and, if allowed, assigns its seq and
// updates the state. It does not check unread human messages; see Post.
func (s *State) Apply(m Message) (Message, error) {
	if err := s.check(m); err != nil {
		return Message{}, err
	}
	m.Seq = s.LastSeq + 1
	if m.Kind == Approve && m.Re == 0 {
		m.Re = s.Resolution
	}
	s.effect(m)
	s.LastSeq = m.Seq
	if m.Actor == Human {
		s.LastHumanSeq = m.Seq
	}
	return m, nil
}

func usesFloor(m Message) bool {
	if m.Actor != Agent {
		return false
	}
	switch m.Kind {
	case Propose, Ask, Answer, Counter, Accept, Reject:
		return true
	}
	return false
}

func (s *State) check(m Message) error {
	if !s.Room.hasSide(m.Side) {
		return badf("unknown side %q", m.Side)
	}
	if m.Actor != Agent && m.Actor != Human {
		return badf("unknown actor %q", m.Actor)
	}
	if len(m.Body) > MaxBody {
		return ErrTooLarge
	}
	if m.Re < 0 || m.Re > s.LastSeq {
		return badf("re %d does not point to an earlier message", m.Re)
	}
	if m.Resolution && !(m.Actor == Agent && (m.Kind == Propose || m.Kind == Counter)) {
		return badf("only an agent's propose or counter can be a resolution")
	}
	if s.Closed {
		return ErrClosed
	}

	switch m.Kind {
	case Propose, Ask, Answer, Counter, Accept:
		if m.Actor != Agent {
			return badf("%s is for agents; a human uses note", m.Kind)
		}
	case Reject, Note, Hand:
	case Resume, Approve, Close:
		if m.Actor != Human {
			return ErrHumansOnly
		}
	default:
		return badf("unknown kind %q", m.Kind)
	}

	if usesFloor(m) {
		switch {
		case s.Agreed:
			return ErrWrongPhase
		case s.Pause != nil:
			return ErrPaused
		case s.Floor != m.Side:
			return ErrNotYourTurn
		case s.TurnsUsed[m.Side] >= s.Room.TurnBudget:
			return ErrBudgetExhausted
		}
		if m.Kind == Accept && m.Re == s.Resolution && s.Resolution != 0 && s.ResolutionSide == m.Side {
			return badf("a side cannot accept its own resolution")
		}
	}

	switch m.Kind {
	case Hand:
		if s.Pause != nil {
			return ErrAlreadyPaused
		}
	case Resume:
		if s.Pause == nil {
			return ErrNotPaused
		}
	case Approve:
		if !s.Agreed {
			return ErrNoResolution
		}
		if m.Re != 0 && m.Re != s.Resolution {
			return ErrStaleResolution
		}
		if s.Approved[m.Side] {
			return ErrAlreadyApproved
		}
	case Reject:
		if m.Actor == Human {
			if s.Resolution == 0 {
				return ErrNoResolution
			}
			if m.Re != s.Resolution {
				return ErrStaleResolution
			}
		}
	}
	return nil
}

func (s *State) effect(m Message) {
	switch m.Kind {
	case Hand:
		s.Pause = &Pause{Kind: PauseHand, Side: m.Side, Actor: m.Actor, Seq: m.Seq}
	case Resume:
		if s.Pause.Kind == PauseBudget {
			s.TurnsUsed = map[string]int{}
		}
		s.Pause = nil
	case Close:
		s.Closed = true
		s.CloseReason = "closed_by_" + m.Side
	case Approve:
		s.Approved[m.Side] = true
		if s.Approved[s.Room.Sides[0]] && s.Approved[s.Room.Sides[1]] {
			s.Closed = true
			s.CloseReason = "approved"
		}
	case Reject:
		if s.Resolution != 0 && m.Re == s.Resolution {
			s.clearResolution()
		}
	case Accept:
		if s.Resolution != 0 && m.Re == s.Resolution {
			s.Agreed = true
		}
	}
	if m.Resolution {
		s.Resolution = m.Seq
		s.ResolutionSide = m.Side
		s.resolutionBody = m.Body
		s.Agreed = false
		s.Approved = map[string]bool{}
	}
	if usesFloor(m) {
		s.TurnsUsed[m.Side]++
		s.Floor = s.Room.Other(m.Side)
		if s.TurnsUsed[m.Side] >= s.Room.TurnBudget && s.Pause == nil && !s.Agreed {
			s.Pause = &Pause{Kind: PauseBudget, Side: m.Side, Actor: Agent, Seq: m.Seq}
		}
	}
}

func (s *State) clearResolution() {
	s.Resolution = 0
	s.ResolutionSide = ""
	s.resolutionBody = ""
	s.Agreed = false
	s.Approved = map[string]bool{}
}

// Error is a rule violation. Code is stable and machine-readable.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Is matches errors by code, so errors.Is works across copies.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

func badf(format string, a ...any) error {
	return &Error{Code: "bad_request", Msg: fmt.Sprintf(format, a...)}
}

// Rule violations.
var (
	ErrBadRequest      = &Error{"bad_request", "invalid message"}
	ErrTooLarge        = &Error{"too_large", fmt.Sprintf("body over %d bytes", MaxBody)}
	ErrClosed          = &Error{"room_closed", "the room is closed"}
	ErrHumansOnly      = &Error{"humans_only", "only a human may do this"}
	ErrWrongPhase      = &Error{"wrong_phase", "a resolution is agreed; waiting for the humans to approve"}
	ErrPaused          = &Error{"paused", "agents are paused; wait for a human to resume"}
	ErrNotYourTurn     = &Error{"not_your_turn", "the other side holds the floor"}
	ErrBudgetExhausted = &Error{"budget_exhausted", "this side has used its turn budget"}
	ErrAlreadyPaused   = &Error{"already_paused", "agents are already paused"}
	ErrNotPaused       = &Error{"not_paused", "agents are not paused"}
	ErrNoResolution    = &Error{"no_resolution", "there is no agreed resolution"}
	ErrStaleResolution = &Error{"stale_resolution", "that is not the resolution under discussion"}
	ErrAlreadyApproved = &Error{"already_approved", "this side has already approved"}
	ErrUnreadHuman     = &Error{"unread_human_message", "read the human messages first"}
)
