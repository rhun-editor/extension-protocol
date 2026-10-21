package main

import "testing"

func TestWhereProblem(t *testing.T) {
	for _, c := range []struct {
		where, project, home string
		bad                  bool
	}{
		{"", "read", "read", false},
		{"workspace", "write", "", false},
		{"local", "", "", false},
		{"local", "no", "no", false},
		{"local", "read", "", true},
		{"local", "", "read", true},
		{"host", "", "", true},
	} {
		m := &manifest{where: c.where, needs: map[string]string{"project": c.project, "home": c.home}}
		if got := whereProblem(m) != ""; got != c.bad {
			t.Errorf("where = %q project = %q home = %q: problem %v, want %v", c.where, c.project, c.home, got, c.bad)
		}
	}
}
