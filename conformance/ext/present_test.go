package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The checker agrees every feature rhun 0.18's hello agrees, with rhun's pairings.
func TestAgreedFeatures(t *testing.T) {
	rhun := []string{"views", "complete", "signature", "definition", "references", "symbols", "actions", "rename", "format",
		"commands", "save-actions", "snippets", "snippet-variants", "snippet-transforms", "workspace_watch", "command-args",
		"context-keys", "git-data", "commit-message", "quickdiff", "scm", "tests", "agents", "edit-review", "tools",
		"agent-prompts", "view-tree", "terminals", "tasks", "query-stream", "provider-scope", "complete-items",
		"signature-list", "navigation", "partial-format", "code-actions", "prepare-rename", "file-diagnostics", "hierarchy",
		"hover-query", "selection-ranges", "workspace-symbols", "document-links", "decorations", "workspace-edits",
		"file-operations", "selections", "reveal", "diff", "linked-editing", "status-items", "idle", "secrets", "urls",
		"uris", "files", "scoped-settings", "workspace-folders", "diagnostic-tags", "folding-ranges", "semantic-tokens",
		"inlay-hints", "code-lens", "inline-complete", "document-colors", "document-highlights", "notebooks", "view-log",
		"notify", "progress", "view-keys", "view-images", "debug", "custom-editors", "view-form", "view-table",
		"quick-input", "view-doc", "markdown"}
	all := map[string]bool{"nonsense": true}
	for _, f := range rhun {
		all[f] = true
	}
	said := agreedFeatures(all)
	for _, f := range rhun {
		if !slices.Contains(said, f) {
			t.Errorf("%s is not agreed", f)
		}
	}
	if len(said) != len(rhun) {
		t.Errorf("agreed %d features, rhun %d: %s", len(said), len(rhun), strings.Join(said, " "))
	}
	alone := map[string]bool{"view-form": true, "view-table": true, "view-doc": true, "view-log": true, "view-keys": true,
		"view-images": true, "tasks": true, "file-operations": true, "markdown": true, "quick-input": true}
	if got := strings.Join(agreedFeatures(alone), " "); got != "quick-input markdown" {
		t.Errorf("without their partners: %q", got)
	}
}

func TestDecorationsAndLegends(t *testing.T) {
	for _, good := range []string{"", "color warning\nbackground #20304080\n\nunderline squiggle error|#ff0000\n",
		"border 2 accent/40\nstrike -\nfaded\nbefore muted -\nafter fg bg\nwidth 0\ngutter diamond git_added\ngrow both\npriority 100\nline-background x"} {
		if why := checkDecorationType([]byte(good)); why != "" {
			t.Errorf("%q refused: %s", good, why)
		}
	}
	for _, bad := range []string{"colour red", "border 4 red", "underline wavy red", "priority 101", "faded 1", "gutter star red", "color #12345", "grow sideways"} {
		if why := checkDecorationType([]byte(bad)); !strings.HasPrefix(why, "line 1") {
			t.Errorf("%q passed (%q)", bad, why)
		}
	}
	h, _ := testHost(t)
	d := h.docs[1] // "package a\n\nfunc F() {}\n"
	rec := func(l1, c1, l2, c2 string, before, after, tip string) string {
		return strings.Join([]string{l1, c1, l2, c2, strconv.Itoa(len(before)), strconv.Itoa(len(after)), strconv.Itoa(len(tip)), "0"}, " ") + "\n" + before + after + tip
	}
	if code, why := checkDecorate([]byte(rec("1", "1", "1", "8", "", " ←", "the package")+rec("3", "1", "3", "5", "", "", "")), 2, d, true); code != "" {
		t.Fatal(code, why)
	}
	for _, c := range []struct {
		body string
		n    int
		code string
	}{
		{rec("1", "1", "1", "8", "", "", ""), 2, "syntax"},       // fewer records than N
		{rec("1", "1", "9", "1", "", "", ""), 1, "badpos"},       // past the text
		{rec("1", "5", "1", "2", "", "", ""), 1, "badpos"},       // ends before it starts
		{rec("1", "1", "1", "2", "a\tb", "", ""), 1, "syntax"},   // a control character
		{rec("1", "1", "1", "2", "", "", "") + "x", 1, "syntax"}, // trailing bytes
		{"1 1 1 2 0 0 5 0\nab", 1, "syntax"},                     // a tip past the body
		{rec("1", "1", "1", "2", strings.Repeat("a", 201), "", ""), 1, "toobig"},
	} {
		if code, _ := checkDecorate([]byte(c.body), c.n, d, true); code != c.code {
			t.Errorf("%q: %q, want %q", c.body, code, c.code)
		}
	}
	// an older version's positions are not checked against the current text
	if code, why := checkDecorate([]byte(rec("9", "1", "9", "2", "", "", "")), 1, d, false); code != "" {
		t.Fatal(code, why)
	}
	if code, why := checkSemanticLegend([]byte("function variable macro\ndeclaration mutable=underline\n")); code != "" {
		t.Fatal(code, why)
	}
	if code, why := checkSemanticLegend([]byte("function\n\n")); code != "" {
		t.Fatal("no modifiers:", code, why)
	}
	for _, bad := range []string{"function", "a b\nc\nd\n", "fun!ction\n\n", "a=b=c\n\n"} {
		if code, _ := checkSemanticLegend([]byte(bad)); code != "syntax" {
			t.Errorf("%q: %q", bad, code)
		}
	}
	if code, _ := checkSemanticLegend([]byte(strings.Repeat("t ", 257) + "\n\n")); code != "toobig" {
		t.Error("257 types passed")
	}
}

func TestDiagnosticTags(t *testing.T) {
	for _, c := range []struct {
		sev        string
		tags, want bool
	}{
		{"hint", false, true}, {"hint+unnecessary", false, false}, {"hint+unnecessary", true, true},
		{"warning+deprecated+later", true, true}, {"warning+", true, false}, {"fatal+deprecated", true, false}, {"note", true, false},
	} {
		if diagSeverity(c.sev, c.tags) != c.want {
			t.Errorf("%s with tags %v: want %v", c.sev, c.tags, c.want)
		}
	}
}
