package main

import "testing"

func TestPackProblem(t *testing.T) {
	m := &manifest{owner: "rhun-editor", name: "essentials"}
	for list, bad := range map[string]bool{
		"rhun-editor/lsp rhun-editor/format": false,
		"a/b":                                false,
		"rhun-editor/essentials":             true,
		"lsp":                                true,
		"a/b/c":                              true,
		".a/b":                               true,
		"a/.b":                               true,
		"a/b c/d e/f g/h i/j k/l m/n o/p q/r s/t u/v w/x y/z aa/b bb/c cc/d dd/e": true, // 17
	} {
		m.pack = list
		if got := packProblem(m) != ""; got != bad {
			t.Errorf("pack = %s: problem %v, want %v (%q)", list, got, bad, packProblem(m))
		}
	}
}
