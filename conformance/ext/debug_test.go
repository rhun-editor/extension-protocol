package main

import (
	"strings"
	"testing"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func TestCheckerStartsAndStopsDebugSessions(t *testing.T) {
	h, _ := testHost(t)
	out := &sink{}
	h.stdin = out
	h.m.needs = map[string]string{"debug": "yes"}
	h.m.debugger = map[string]map[string]string{"go": {}}
	h.features = map[string]bool{"debug": true}
	var replies []string
	reply := func(s string) { replies = append(replies, s) }
	rerr := func(code, text string) { replies = append(replies, "error "+code+" "+text) }
	var c rhunext.DebugConfig
	c.Set("type", rhunext.DebugStr("go"))
	b, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte("Run"), b...)
	h.debugStart(1, []string{"3", itoaLen(body)}, message{body: body}, reply, rerr)
	h.debugStop(2, []string{"1"}, reply, rerr)
	if len(h.fails) != 0 || strings.Join(replies, "|") != "ok 1 1|ok 2" {
		t.Fatal(h.fails, replies)
	}
	if !strings.Contains(out.String(), "0 debug-ended 1 0\n") {
		t.Fatal(out.String())
	}
	h.debugStop(3, []string{"2"}, reply, rerr)
	if replies[len(replies)-1] != "error refused not a session you started" || len(h.fails) != 1 {
		t.Fatal(replies, h.fails)
	}
	for _, args := range [][]string{{}, {"x"}, {"1", "2"}, {"-1"}} {
		h.fails = nil
		h.debugStop(4, args, reply, rerr)
		if replies[len(replies)-1] != "error syntax cannot parse the line" || len(h.fails) != 1 {
			t.Fatal(args, replies, h.fails)
		}
	}
	h.features = map[string]bool{}
	h.debugStop(5, []string{"1"}, reply, rerr)
	if replies[len(replies)-1] != "error unknown no such request" {
		t.Fatal(replies)
	}
}
