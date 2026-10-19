package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveApplyInvocationAndVersionGuards(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		body, code string
		mutate     func(*host)
	}{
		{name: "empty", args: []string{"24", "1", "0"}, body: "0\n"},
		{name: "edit", args: []string{"24", "1", "0"}, body: wsBody(grp("#1", "0", ed(1, 1, 1, 1, "x")))},
		{name: "wrong request", args: []string{"25", "1", "0"}, body: "0\n", code: "refused"},
		{name: "foreign document", args: []string{"24", "2", "3"}, body: "0\n", code: "refused"},
		{name: "wrong version", args: []string{"24", "1", "1"}, body: "0\n", code: "stale"},
		{name: "outside change", args: []string{"24", "1", "0"}, body: "0\n", code: "stale", mutate: func(h *host) { h.docs[1].version++ }},
		{name: "retired", args: []string{"24", "1", "0"}, body: "0\n", code: "refused", mutate: func(h *host) { h.saveScope = nil }},
		{name: "signed request", args: []string{"+24", "1", "0"}, body: "0\n", code: "syntax"},
		{name: "overflow request", args: []string{"2147483648", "1", "0"}, body: "0\n", code: "syntax"},
		{name: "overflow version", args: []string{"24", "1", "9223372036854775808"}, body: "0\n", code: "syntax"},
		{name: "foreign group", args: []string{"24", "1", "0"}, body: wsBody(grp("#1", "0", ed(1, 1, 1, 1, "x")), grp("#2", "3", ed(1, 1, 1, 1, "x"))), code: "nofile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := testHost(t)
			h.stdin = &sink{}
			h.features = map[string]bool{"save-actions": true}
			h.saveScope = &saveScope{24, 1, 0}
			if tc.mutate != nil {
				tc.mutate(h)
			}
			before, version := string(h.docs[1].text), h.docs[1].version
			result, code := "", ""
			h.saveApplyRequest(tc.args, []byte(tc.body), func(s string) { result = s }, func(c, s string) { code = c }, 101)
			if code != tc.code {
				t.Fatalf("code=%q want=%q, reply=%q", code, tc.code, result)
			}
			if tc.code != "" {
				if string(h.docs[1].text) != before || h.docs[1].version != version {
					t.Fatal("rejected edit changed document")
				}
				return
			}
			if result != fmt.Sprintf("ok 101 %d", h.docs[1].version) || h.saveScope.version != h.docs[1].version {
				t.Fatal(result)
			}
			if tc.name == "empty" && h.docs[1].version != version {
				t.Fatal("empty edit advanced version")
			}
			if tc.name == "edit" && !strings.HasPrefix(string(h.docs[1].text), "xpackage") {
				t.Fatal("edit missing")
			}
		})
	}
}

func TestSaveProviderManifestMappingAndForeignUnopenedFile(t *testing.T) {
	h, dir := testHost(t)
	h.features = map[string]bool{"save-actions": true}
	h.m.provides["save_actions"] = "*.go"
	if h.patterns("save-actions") != "*.go" || !h.provider(h.docs[1]) {
		t.Fatal("manifest save_actions did not provide save-actions")
	}
	body := wsBody(grp(filepath.Join(dir, "does-not-exist.go"), "-", ed(1, 1, 1, 1, "x")))
	_, code, why := h.checkWorkspaceEdit([]byte(body), h.docs[1])
	if code != "nofile" || !strings.Contains(why, "not the document asked about") {
		t.Fatal(code, why)
	}
}

func TestEmptySaveActionsReplyIsConforming(t *testing.T) {
	h, _ := testHost(t)
	if !h.emptyAnswer("save-actions", message{line: "ok 24 0"}) || len(h.warns) != 0 || len(h.oks) != 1 {
		t.Fatalf("warnings=%v, successes=%v", h.warns, h.oks)
	}
}
