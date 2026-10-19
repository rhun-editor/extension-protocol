package main

import "testing"

// agent-ask cards are read as rhun reads them (feature agent-prompts)
func TestAgentAskGrammar(t *testing.T) {
	many := "Q"
	for i := 0; i < 17; i++ {
		many += "\no"
	}
	cases := []struct{ name, sid, body, code string }{
		{"one option", "s1", "Allow?\nYes", ""},
		{"last LF", "s1", "Allow?\nYes\nNo\n", ""},
		{"no options", "s1", "Allow?\n", "syntax"},
		{"no LF", "s1", "Allow?", "syntax"},
		{"empty option", "s1", "Allow?\nYes\n\nNo", "syntax"},
		{"sixteen", "s1", many[:len(many)-2], ""},
		{"seventeen", "s1", many, "toobig"},
		{"unlisted session", "s9", "Allow?\nYes", "refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := testHost(t)
			h.stdin = &sink{}
			h.features = map[string]bool{"agent-prompts": true}
			aiState.sessions = []string{"s1"}
			code, answered := "", ""
			h.aiRequest(7, "agent-ask", []string{tc.sid, "0"}, []byte(tc.body), true,
				func(s string) { answered = s }, func(c, s string) { code = c })
			if code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
			if tc.code == "" && answered != "ok 7 1" {
				t.Fatalf("answer %q", answered)
			}
			if (len(h.fails) > 0) != (tc.code != "") {
				t.Fatalf("fails %v", h.fails)
			}
		})
	}
}

// [provides] agents and agents.title are manifest keys of the agents feature
func TestAgentsProvidesKeys(t *testing.T) {
	cases := []struct {
		k, v          string
		handled, fail bool
	}{
		{"agents", "yes", true, false},
		{"agents", "no", true, false},
		{"agents", "maybe", true, true},
		{"agents.title", "Gemini CLI", true, false},
		{"agents.title", string(make([]byte, 81)), true, true},
		{"complete", "*.go", false, false},
	}
	for _, c := range cases {
		handled, why := agentsProvidesProblem(c.k, c.v)
		if handled != c.handled || (why != "") != c.fail {
			t.Fatalf("%s = %q: handled %v, why %q", c.k, c.v, handled, why)
		}
	}
}
