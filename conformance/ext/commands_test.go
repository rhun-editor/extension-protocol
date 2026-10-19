package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestCommandRequests(t *testing.T) {
	cases := []struct {
		line, body, want string
		features         string
	}{
		{"1 run-command goto_line 2", "12", "ok 1 0", "command-args"},
		{"2 run-command rhun.find 3", "abc", "ok 2 0", "command-args"},
		{"3 run-command undo 1", "x", "error 3 syntax", "command-args"},
		{"4 run-command demo.go 0", "", "error 4 refused", "command-args"},
		{"5 run-command alice/demo.go 0", "", "error 5 refused", "command-args"},
		{"6 run-command bob/x.y 0", "", "error 6 refused", "command-args"},
		{"7 run-command goto_line 2", "12", "error 7 unknown", ""},
		{"8 set-context mode insert here", "", "ok 8", "context-keys"},
		{"9 set-context Mode x", "", "error 9 syntax", "context-keys"},
		{"10 set-context mode " + strings.Repeat("v", 257), "", "error 10 toobig", "context-keys"},
		{"11 set-context mode a‮b", "", "error 11 syntax", "context-keys"},
		{"12 status-item go 16", "right 0 2 0 0\nGo", "ok 12", "status-items"},
		{"13 status-item go 22", "right 0 2 0 6\nGocolor", "error 13 syntax", "status-items"},
		{"14 status-item go 30", "left 5 2 0 15\nGocolor pink 0\n", "error 14 syntax", "status-items"},
		{"15 status-item go 38", "left 5 2 0 22\nGocommand run_task 3\nall", "ok 15", "status-items"},
		{"16 status-item Go 16", "right 0 2 0 0\nGo", "error 16 syntax", "status-items"},
		{"17 status-remove go", "", "ok 17", "status-items"},
		{"18 status-remove go", "", "error 18 unknown", "context-keys"},
	}
	for _, c := range cases {
		h, _ := testHost(t)
		h.m.name, h.m.owner, h.m.commands = "demo", "alice", []string{"go"}
		h.contextKeys, h.statusItems = map[string]bool{}, map[string]bool{}
		h.features = map[string]bool{}
		for _, f := range strings.Fields(c.features) {
			h.features[f] = true
		}
		f := strings.Split(c.line, " ")
		got := ""
		id, _ := strconv.ParseInt(f[0], 10, 64)
		h.commandRequest(id, f[1], f[2:], message{line: c.line, body: []byte(c.body)},
			func(s string) { got = s }, func(code, text string) { got = "error " + f[0] + " " + code })
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%q: %q, want %q", c.line, got, c.want)
		}
	}
}

func TestStatusBodies(t *testing.T) {
	good := []string{"right 0 1 0 0\nx", "left -1000 0 3 19\ntipwhen 11\nprojectOpen", "right 1000 0 0 19\nbackground error 0\n",
		"right 0 0 0 11\nfuture 1 0\n"}
	for _, b := range good {
		if why := checkStatusBody([]byte(b)); why != "" {
			t.Errorf("%q: %s", b, why)
		}
	}
	bad := []string{"right 0 1 0 0", "up 0 1 0 0\nx", "right 1001 1 0 0\nx", "right 0 2 0 0\nx", "right 0 1 0 0\n\t",
		"right 0 0 0 12\ncommand X 0\n", "right 0 0 0 8\nwhen 3\na\nb"}
	for _, b := range bad {
		if why := checkStatusBody([]byte(b)); why == "" {
			t.Errorf("%q accepted", b)
		}
	}
}
