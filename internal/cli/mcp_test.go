package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type mcpClient struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
	id  int
}

func startMCP(t *testing.T, dir, side string) *mcpClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	env := Env{Stdin: inR, Stdout: outW, Stderr: io.Discard, Dir: dir, IsTTY: func() bool { return false }}
	args := []string{"mcp"}
	if side != "" {
		args = append(args, "--side", side)
	}
	go func() {
		Run(ctx, env, args)
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close(); cancel() })
	return &mcpClient{t: t, in: inW, out: bufio.NewScanner(outR)}
}

func (c *mcpClient) send(method string, params any) int {
	c.id++
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	fmt.Fprintf(c.in, "%s\n", data)
	return c.id
}

func (c *mcpClient) notify(method string, params any) {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	fmt.Fprintf(c.in, "%s\n", data)
}

type rpcResp struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *mcpClient) recv() rpcResp {
	c.t.Helper()
	done := make(chan bool, 1)
	var r rpcResp
	go func() {
		ok := c.out.Scan()
		if ok {
			json.Unmarshal(c.out.Bytes(), &r)
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if !ok {
			c.t.Fatal("mcp server closed its output")
		}
	case <-time.After(5 * time.Second):
		c.t.Fatal("no response from mcp server")
	}
	return r
}

type toolResult struct {
	Content []struct{ Text string } `json:"content"`
	IsError bool                    `json:"isError"`
}

func (c *mcpClient) call(name string, args map[string]any) toolResult {
	c.t.Helper()
	id := c.send("tools/call", map[string]any{"name": name, "arguments": args})
	r := c.recv()
	if r.ID != id || r.Error != nil {
		c.t.Fatalf("call %s: %+v", name, r)
	}
	var tr toolResult
	json.Unmarshal(r.Result, &tr)
	return tr
}

func TestMCP(t *testing.T) {
	dir := t.TempDir()
	tm := &term{t: t, dir: dir}
	tm.run(0, "open", "--topic", "Orders")
	tm.run(0, "join")

	a := startMCP(t, dir, "A")
	a.send("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t"}})
	init := a.recv()
	if !strings.Contains(string(init.Result), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize: %s", init.Result)
	}
	a.notify("notifications/initialized", nil)

	a.send("tools/list", nil)
	list := string(a.recv().Result)
	for _, name := range []string{"coop_open", "coop_join", "coop_status", "coop_read", "coop_say", "coop_wait", "coop_hand"} {
		if !strings.Contains(list, `"`+name+`"`) {
			t.Fatalf("tools/list misses %s", name)
		}
	}

	// A body starting with "-" stays a body, not a flag.
	r := a.call("coop_say", map[string]any{"kind": "propose", "body": "--resolution maybe"})
	if r.IsError || !strings.Contains(r.Content[0].Text, "posted #1 propose") {
		t.Fatalf("say: %+v", r)
	}
	r = a.call("coop_say", map[string]any{"kind": "ask", "body": "again"})
	if !r.IsError || !strings.Contains(r.Content[0].Text, "not_your_turn") {
		t.Fatalf("out of turn must be an error result: %+v", r)
	}

	// B waits through MCP; A's human posts meanwhile.
	b := startMCP(t, dir, "B")
	b.send("initialize", map[string]any{"protocolVersion": "1999-01-01"})
	b.recv()
	// An unread message is returned at once.
	r = b.call("coop_wait", map[string]any{"timeout_seconds": 5})
	if !strings.Contains(r.Content[0].Text, "--resolution maybe") {
		t.Fatalf("wait: %+v", r)
	}
	// Then wait blocks until something new, without blocking other requests.
	waitID := b.send("tools/call", map[string]any{"name": "coop_wait", "arguments": map[string]any{"timeout_seconds": 5}})
	b.send("ping", nil)
	if p := b.recv(); p.ID != waitID+1 {
		t.Fatalf("ping must not wait behind a running wait: %+v", p)
	}
	tm.run(0, "say", "--side", "A", "keep it simple")
	w := b.recv()
	var wr toolResult
	json.Unmarshal(w.Result, &wr)
	if w.ID != waitID || !strings.Contains(wr.Content[0].Text, `actor="human"`) || !strings.Contains(wr.Content[0].Text, "keep it simple") {
		t.Fatalf("wait result: %s", w.Result)
	}

	// A cancelled wait gets no response; the next request still works.
	waitID = b.send("tools/call", map[string]any{"name": "coop_wait", "arguments": map[string]any{"timeout_seconds": 5}})
	b.notify("notifications/cancelled", map[string]any{"requestId": waitID})
	r = b.call("coop_status", nil)
	if !strings.Contains(r.Content[0].Text, "you: side B, agent") {
		t.Fatalf("status: %+v", r)
	}
}

func (c *mcpClient) recvRaw() map[string]any {
	c.t.Helper()
	done := make(chan map[string]any, 1)
	go func() {
		var m map[string]any
		if c.out.Scan() {
			json.Unmarshal(c.out.Bytes(), &m)
		}
		done <- m
	}()
	select {
	case m := <-done:
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("no message from mcp server")
	}
	return nil
}

func (c *mcpClient) answer(id any, result any) {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Fprintf(c.in, "%s\n", data)
}

func TestMCPHumanTools(t *testing.T) {
	dir := t.TempDir()
	tm := &term{t: t, dir: dir}
	tm.run(0, "open", "--topic", "Orders")
	tm.run(0, "join")
	tm.run(0, "say", "--agent", "--side", "A", "--kind", "propose", "--resolution", "REST, 100 per page")

	b := startMCP(t, dir, "B")
	b.send("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}})
	b.recv()
	b.send("tools/list", nil)
	list := string(b.recv().Result)
	for _, n := range []string{"coop_approve", "coop_resume", "coop_close", "coop_say_human"} {
		if !strings.Contains(list, `"`+n+`"`) {
			t.Fatalf("missing %s", n)
		}
	}
	// Nothing to approve yet: the rules still apply to human actions.
	r := b.call("coop_approve", nil)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "no_resolution") {
		t.Fatalf("approve without agreement: %+v", r)
	}
	tm.run(0, "say", "--agent", "--side", "B", "--kind", "accept", "--re", "1", "ok")
	r = b.call("coop_approve", nil)
	if r.IsError || !strings.Contains(r.Content[0].Text, "done as your human") {
		t.Fatalf("approve: %+v", r)
	}
	r = b.call("coop_say_human", map[string]any{"text": "спасибо"})
	if r.IsError {
		t.Fatalf("say_human: %+v", r)
	}
	log := tm.run(0, "read", "--side", "A", "--all")
	mustContain(t, log, `actor="human" seq="3" kind="approve"`)
	mustContain(t, log, `side="B" actor="human" seq="4" kind="note"`)
}
