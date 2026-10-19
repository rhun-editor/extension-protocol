package main

import "testing"

func TestGlobs(t *testing.T) {
	g, bad := parseGlobs([]byte("+*.{go,rs}\n-vendor\n"))
	if bad != "" || len(g) != 3 {
		t.Fatal(g, bad)
	}
	for path, want := range map[string]bool{"main.go": true, "a/b.rs": true, "vendor/x.go": false, "a.txt": false} {
		if globsList(g, path) != want {
			t.Errorf("%s", path)
		}
	}
	for _, c := range []struct {
		p, s string
		ok   bool
	}{{"src/**", "src", true}, {"src/**", "src/a/b", true}, {"**/lib.rs", "src/lib.rs", true}, {"src/*.rs", "src/a/b.rs", false},
		{"[!a]x", "bx", true}, {"m?in.rs", "main.rs", true}} {
		if globMatch(c.p, c.s) != c.ok {
			t.Errorf("%s %s", c.p, c.s)
		}
	}
	if _, bad := parseGlobs([]byte("+a{b\n")); bad != "syntax" {
		t.Error("unclosed brace")
	}
}
