package main

import (
	"strings"
	"testing"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func TestCheckScmUpdate(t *testing.T) {
	p := rhunext.ScmPanel{Status: "default", Placeholder: "Message",
		Actions: []rhunext.ScmAction{{Icon: "check", Command: "commit", Label: "Commit"}},
		Groups: []rhunext.ScmGroup{{ID: "changes", Label: "Changes", Resources: []rhunext.ScmResource{
			{Path: "a.txt", Code: 'M', Color: "modified", Click: rhunext.ScmAction{Command: "open_change", Args: []byte("a.txt")},
				Actions: []rhunext.ScmAction{{Command: "revert", Args: []byte("a.txt")}}, Tip: "changed"}}}}}
	body, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := checkScmUpdate(body, map[string]bool{"commit": true, "open_change": true})
	if err != nil || len(unknown) != 1 || unknown[0] != "revert" {
		t.Fatal(unknown, err)
	}
	for _, bad := range []string{
		"0 0 0 0",   // no LF
		"0 0 5 0\n", // five buttons
		"0 0 0 1\ngroup g 1 0 1\nGres M pink 1 0\na",                           // an unknown color
		"0 0 0 1\ngroup g 1 0 1\nGres M - 2 0\n/a",                             // an absolute path
		"0 0 0 1\ngroup g 1 0 1\nGres M - 1 2\naaction - x 0 0\ncommand y 0\n", // the click after an action
		"0 0 0 0\nextra", // trailing bytes
		"3 0 0 0\na\nb",  // a status with LF
	} {
		if _, err := checkScmUpdate([]byte(bad), nil); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// an unknown record is skipped by its length
	if _, err := checkScmUpdate([]byte("0 0 0 1\ngroup g 1 0 1\nGres M - 1 1\nalater 1 2 3\nabc"), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "tip 7\nchanged") {
		t.Fatal("tip")
	}
}
