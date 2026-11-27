package core

import (
	"errors"
	"testing"
)

func agent(side string, k Kind) Message { return Message{Side: side, Actor: Agent, Kind: k, Body: "x"} }
func human(side string, k Kind) Message { return Message{Side: side, Actor: Human, Kind: k, Body: "x"} }

func re(m Message, n int) Message { m.Re = n; return m }
func res(m Message) Message       { m.Resolution = true; return m }

// post applies m as a participant who has read everything so far.
func post(t *testing.T, s *State, m Message) Message {
	t.Helper()
	out, err := s.Post(m, s.LastSeq)
	if err != nil {
		t.Fatalf("post %s/%s/%s: %v", m.Side, m.Actor, m.Kind, err)
	}
	return out
}

func refuse(t *testing.T, s *State, m Message, seen int, want error) {
	t.Helper()
	before := *s
	_, err := s.Post(m, seen)
	if !errors.Is(err, want) {
		t.Fatalf("post %s/%s/%s: got %v, want %v", m.Side, m.Actor, m.Kind, err, want)
	}
	if s.LastSeq != before.LastSeq || s.Floor != before.Floor {
		t.Fatalf("refused message changed the state")
	}
}

func TestHappyPath(t *testing.T) {
	s := NewState(NewRoom("orders", 0))
	post(t, s, agent("A", Propose))
	post(t, s, agent("B", Ask))
	post(t, s, agent("A", Answer))
	r := post(t, s, res(agent("B", Counter)))
	if s.Resolution != r.Seq || s.Phase() != Negotiation {
		t.Fatalf("resolution not recorded: %+v", s)
	}
	post(t, s, re(agent("A", Accept), r.Seq))
	if s.Phase() != Approval {
		t.Fatalf("phase %s, want approval", s.Phase())
	}
	refuse(t, s, agent("B", Answer), s.LastSeq, ErrWrongPhase)
	post(t, s, agent("B", Note))
	post(t, s, human("A", Approve))
	refuse(t, s, human("A", Approve), s.LastSeq, ErrAlreadyApproved)
	post(t, s, human("B", Approve))
	if s.Phase() != Closed || s.CloseReason != "approved" {
		t.Fatalf("not closed by approval: %+v", s)
	}
	refuse(t, s, human("A", Note), s.LastSeq, ErrClosed)
}

func TestTurns(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	refuse(t, s, agent("B", Propose), 0, ErrNotYourTurn)
	post(t, s, agent("A", Propose))
	refuse(t, s, agent("A", Ask), s.LastSeq, ErrNotYourTurn)
	post(t, s, agent("A", Note)) // notes need no floor
	if s.Floor != "B" {
		t.Fatalf("floor %s, want B", s.Floor)
	}
	post(t, s, human("A", Note)) // humans need no floor
	post(t, s, agent("B", Answer))
}

func TestUnreadHuman(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	post(t, s, agent("A", Propose))
	h := post(t, s, human("A", Note))
	refuse(t, s, agent("B", Answer), h.Seq-1, ErrUnreadHuman)
	if _, err := s.Post(agent("B", Answer), h.Seq); err != nil {
		t.Fatal(err)
	}
}

func TestHandAndResume(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	post(t, s, agent("A", Propose))
	post(t, s, agent("B", Hand))
	refuse(t, s, agent("B", Answer), s.LastSeq, ErrPaused)
	refuse(t, s, agent("A", Hand), s.LastSeq, ErrAlreadyPaused)
	refuse(t, s, agent("A", Note), s.LastSeq, ErrPaused) // agents keep quiet in a pause
	post(t, s, human("A", Note))
	refuse(t, s, agent("B", Resume), s.LastSeq, ErrHumansOnly)
	post(t, s, human("B", Resume))
	refuse(t, s, human("B", Resume), s.LastSeq, ErrNotPaused)
	post(t, s, agent("B", Answer))
}

func TestBudget(t *testing.T) {
	s := NewState(NewRoom("t", 2))
	post(t, s, agent("A", Propose))
	post(t, s, agent("B", Ask))
	post(t, s, agent("A", Answer)) // A has used 2 of 2
	if s.Pause == nil || s.Pause.Kind != PauseBudget {
		t.Fatalf("want budget pause, got %+v", s.Pause)
	}
	refuse(t, s, agent("B", Ask), s.LastSeq, ErrPaused)
	post(t, s, human("A", Resume))
	if s.TurnsUsed["A"] != 0 {
		t.Fatalf("resume must refill budgets")
	}
	post(t, s, agent("B", Ask))
}

func TestResolutionRules(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	post(t, s, res(agent("A", Propose)))
	post(t, s, agent("B", Accept)) // accept without re is just a message
	if s.Agreed {
		t.Fatal("accept without re must not agree the resolution")
	}
}

func TestRejectAndReplace(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	r1 := post(t, s, res(agent("A", Propose)))
	post(t, s, re(agent("B", Reject), r1.Seq))
	if s.Resolution != 0 {
		t.Fatalf("reject must clear the resolution")
	}
	r2 := post(t, s, res(agent("A", Propose)))
	r3 := post(t, s, res(agent("B", Counter))) // replaces r2
	if s.Resolution != r3.Seq || s.ResolutionSide != "B" {
		t.Fatalf("counter must replace the resolution")
	}
	post(t, s, re(agent("A", Accept), r2.Seq)) // stale: does not agree
	if s.Agreed {
		t.Fatal("accepting a replaced resolution must not agree")
	}
}

func TestAcceptOwnResolution(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	r := post(t, s, res(agent("A", Propose)))
	post(t, s, agent("B", Ask))
	refuse(t, s, re(agent("A", Accept), r.Seq), s.LastSeq, ErrBadRequest)
}

func TestHumanRejectDuringApproval(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	r := post(t, s, res(agent("A", Propose)))
	post(t, s, re(agent("B", Accept), r.Seq))
	post(t, s, human("A", Approve))
	post(t, s, re(human("B", Reject), r.Seq))
	if s.Phase() != Negotiation || len(s.Approved) != 0 {
		t.Fatalf("human reject must reopen negotiation: %+v", s)
	}
	post(t, s, agent("A", Propose))
}

func TestPermissions(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	refuse(t, s, agent("A", Close), 0, ErrHumansOnly)
	refuse(t, s, agent("A", Approve), 0, ErrHumansOnly)
	refuse(t, s, human("A", Propose), 0, ErrBadRequest)
	refuse(t, s, human("A", Approve), 0, ErrNoResolution)
	refuse(t, s, Message{Side: "C", Actor: Agent, Kind: Propose}, 0, ErrBadRequest)
	refuse(t, s, Message{Side: "A", Actor: Agent, Kind: "shout"}, 0, ErrBadRequest)
	refuse(t, s, re(agent("A", Propose), 5), 0, ErrBadRequest)
	big := agent("A", Propose)
	big.Body = string(make([]byte, MaxBody+1))
	refuse(t, s, big, 0, ErrTooLarge)
	post(t, s, human("B", Close))
	if s.CloseReason != "closed_by_B" {
		t.Fatalf("close reason %q", s.CloseReason)
	}
}

func TestReplay(t *testing.T) {
	s := NewState(NewRoom("t", 0))
	var log []Message
	for _, m := range []Message{agent("A", Propose), human("B", Note), res(agent("B", Counter))} {
		log = append(log, post(t, s, m))
	}
	got, err := Replay(s.Room, log)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeq != 3 || got.Resolution != 3 || got.ResolutionBody() != "x" {
		t.Fatalf("replay mismatch: %+v", got)
	}
	log[1].Seq = 7
	if _, err := Replay(s.Room, log); err == nil {
		t.Fatal("replay must reject a gap in seq")
	}
}
